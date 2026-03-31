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

go 1.25.0
EOF

cat > "${TMP_DIR}/fakebin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${CURL_LOG}"
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
exit 0
GOEOF
chmod +x "${dest}/go/bin/go"
EOF
chmod +x "${TMP_DIR}/fakebin/tar"

CURL_LOG="${TMP_DIR}/curl.log"
TAR_LOG="${TMP_DIR}/tar.log"
GO_LOG="${TMP_DIR}/go.log"
export CURL_LOG TAR_LOG GO_LOG

PATH="${TMP_DIR}/fakebin:/usr/bin:/bin" "${TMP_DIR}/scripts/appctl" build

if [[ ! -s "${CURL_LOG}" ]]; then
  echo "expected bootstrap download to run curl"
  exit 1
fi

if [[ ! -s "${TAR_LOG}" ]]; then
  echo "expected bootstrap download to extract archive"
  exit 1
fi

EXPECTED=$'mod tidy\nbuild -o '"${TMP_DIR}"'/go-api/bin/forest ./cmd/server'
ACTUAL="$(cat "${GO_LOG}")"
if [[ "${ACTUAL}" != "${EXPECTED}" ]]; then
  echo "expected bootstrapped go binary to be used"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED}" "${ACTUAL}"
  exit 1
fi

echo "bootstrap go test passed"
