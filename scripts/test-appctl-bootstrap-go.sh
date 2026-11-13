#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api" "${TMP_DIR}/fakebin"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/go-api/go.mod" <<'EOF'
module bootstrap-test

go 1.27.3
EOF

cat > "${TMP_DIR}/fakebin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${CURL_LOG}"
[[ "${MOCK_DOWNLOAD_FAIL:-0}" == 0 ]] || exit 22
out=""
while [[ $# -gt 0 ]]; do
  case "${1}" in
    -o)
      out="${2}"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
[[ -n "${out}" ]] || exit 1
printf 'fake archive\n' > "${out}"
EOF
chmod +x "${TMP_DIR}/fakebin/curl"

cat > "${TMP_DIR}/fakebin/tar" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${TAR_LOG}"
[[ "${MOCK_EXTRACT_FAIL:-0}" == 0 ]] || exit 2
dest=""
while [[ $# -gt 0 ]]; do
  case "${1}" in
    -C)
      dest="${2}"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
[[ -n "${dest}" ]] || exit 1
mkdir -p "${dest}/go/bin"
cat > "${dest}/go/bin/go" <<'GOEOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GO_LOG}"
if [[ "${1:-}" == "build" && "${2:-}" == "-o" ]]; then
  mkdir -p "$(dirname "${3}")"
  printf 'fake binary\n' > "${3}"
fi
exit 0
GOEOF
chmod +x "${dest}/go/bin/go"
EOF
chmod +x "${TMP_DIR}/fakebin/tar"

CURL_LOG="${TMP_DIR}/curl.log"
TAR_LOG="${TMP_DIR}/tar.log"
GO_LOG="${TMP_DIR}/go.log"
export CURL_LOG TAR_LOG GO_LOG

if command -v sha256sum >/dev/null 2>&1; then
  DIGEST="$(printf 'fake archive\n' | sha256sum | awk '{print $1}')"
else
  DIGEST="$(printf 'fake archive\n' | shasum -a 256 | awk '{print $1}')"
fi
printf '%s  go1.27.3.linux-amd64.tar.gz\n' "${DIGEST}" > "${TMP_DIR}/scripts/go-toolchain.sha256"
export GO_BOOTSTRAP_OS=linux GO_BOOTSTRAP_ARCH=amd64
unset GO_BOOTSTRAP_VERSION GO_BOOTSTRAP_URL GO_BOOTSTRAP_SHA256

PATH="${TMP_DIR}/fakebin:/usr/bin:/bin" "${TMP_DIR}/scripts/appctl" build

if [[ ! -s "${CURL_LOG}" ]]; then
  echo "expected bootstrap download to run curl"
  exit 1
fi

if [[ ! -s "${TAR_LOG}" ]]; then
  echo "expected bootstrap download to extract archive"
  exit 1
fi

EXPECTED=$'build -o '"${TMP_DIR}"'/go-api/bin/forest ./cmd/server'
EXPECTED+=$'\nbuild -o '"${TMP_DIR}"'/storage/probe/forest-probe-linux-amd64 ./cmd/probe'
EXPECTED+=$'\nbuild -o '"${TMP_DIR}"'/storage/probe/forest-probe-linux-arm64 ./cmd/probe'
ACTUAL="$(cat "${GO_LOG}")"
if [[ "${ACTUAL}" != "${EXPECTED}" ]]; then
  echo "expected bootstrapped go binary to be used"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED}" "${ACTUAL}"
  exit 1
fi

# Exercise bootstrap directly against a cached toolchain. The ordinary command
# intentionally prefers an existing Go executable and lets go.mod enforce its
# minimum version (including automatic, checksum-verified toolchain selection).
awk '/^action="/ { exit } { print }' "${TMP_DIR}/scripts/appctl" > "${TMP_DIR}/scripts/bootstrap-only"
printf '\nbootstrap_local_go\n' >> "${TMP_DIR}/scripts/bootstrap-only"
chmod +x "${TMP_DIR}/scripts/bootstrap-only"
cat > "${TMP_DIR}/fakebin/mv" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${MOCK_ACTIVATE_FAIL:-0}" == 1 && "${1:-}" == */.go-install.*/go ]]; then
  exit 1
fi
exec /bin/mv "$@"
EOF
chmod +x "${TMP_DIR}/fakebin/mv"

reset_fixture() {
  rm -rf "${TMP_DIR}/.local/go"
  mkdir -p "${TMP_DIR}/.local/go"
  printf 'preserve old toolchain\n' > "${TMP_DIR}/.local/go/previous-marker"
  : > "${CURL_LOG}"
  : > "${TAR_LOG}"
}
expect_preserved_failure() {
  reset_fixture
  if env PATH="${TMP_DIR}/fakebin:/usr/bin:/bin" "$@" bash "${TMP_DIR}/scripts/bootstrap-only" > "${TMP_DIR}/failure.log" 2>&1; then
    echo "expected bootstrap rejection: $*"; exit 1
  fi
  [[ "$(cat "${TMP_DIR}/.local/go/previous-marker")" == 'preserve old toolchain' ]]
  [[ -z "$(find "${TMP_DIR}/.local" -maxdepth 1 -name '.go-install.*' -print)" ]]
}

expect_preserved_failure GO_BOOTSTRAP_VERSION=1.27.99
[[ ! -s "${CURL_LOG}" && ! -s "${TAR_LOG}" ]]
expect_preserved_failure GO_BOOTSTRAP_SHA256=not-a-digest
[[ ! -s "${CURL_LOG}" && ! -s "${TAR_LOG}" ]]
expect_preserved_failure GO_BOOTSTRAP_SHA256="$(printf '%064d' 0)"
[[ -s "${CURL_LOG}" && ! -s "${TAR_LOG}" ]]
expect_preserved_failure MOCK_DOWNLOAD_FAIL=1
[[ ! -s "${TAR_LOG}" ]]
expect_preserved_failure MOCK_EXTRACT_FAIL=1
[[ -s "${TAR_LOG}" ]]
expect_preserved_failure MOCK_ACTIVATE_FAIL=1
[[ -s "${TAR_LOG}" ]]

# An explicitly trusted digest supports a custom archive/version, without an
# integrity-check bypass. The previous directory disappears only on success.
reset_fixture
PATH="${TMP_DIR}/fakebin:/usr/bin:/bin" GO_BOOTSTRAP_VERSION=1.27.99 GO_BOOTSTRAP_URL=https://fixture.invalid/custom.tar.gz GO_BOOTSTRAP_SHA256="${DIGEST}" bash "${TMP_DIR}/scripts/bootstrap-only"
[[ -x "${TMP_DIR}/.local/go/bin/go" && ! -f "${TMP_DIR}/.local/go/previous-marker" ]]
[[ -z "$(find "${TMP_DIR}/.local" -maxdepth 1 -name '.go-install.*' -print)" ]]

echo "bootstrap go test passed"
