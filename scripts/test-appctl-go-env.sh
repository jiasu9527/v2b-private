#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/fake-go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
{
  printf 'cmd=%s\n' "$*"
  printf 'GOPATH=%s\n' "${GOPATH:-}"
  printf 'GOMODCACHE=%s\n' "${GOMODCACHE:-}"
  printf 'GOCACHE=%s\n' "${GOCACHE:-}"
} >> "${GO_LOG}"
if [[ "${1:-}" == "build" ]]; then
  for ((i = 1; i <= $#; i++)); do
    if [[ "${!i}" == "-o" ]]; then
      j=$((i + 1))
      mkdir -p "$(dirname "${!j}")"
      printf 'fake binary\n' > "${!j}"
    fi
  done
fi
exit 0
EOF
chmod +x "${TMP_DIR}/fake-go"

GO_LOG="${TMP_DIR}/go.log"
export GO_LOG

env -i PATH="/usr/bin:/bin" GO_BIN="${TMP_DIR}/fake-go" GO_LOG="${GO_LOG}" bash "${TMP_DIR}/scripts/appctl" build

if ! rg -n "^GOPATH=${TMP_DIR}/.local/gopath$" "${GO_LOG}" >/dev/null 2>&1; then
  echo "expected GOPATH fallback for empty environment"
  cat "${GO_LOG}"
  exit 1
fi

if ! rg -n "^GOMODCACHE=${TMP_DIR}/.local/gomodcache$" "${GO_LOG}" >/dev/null 2>&1; then
  echo "expected GOMODCACHE fallback for empty environment"
  cat "${GO_LOG}"
  exit 1
fi

if ! rg -n "^GOCACHE=${TMP_DIR}/.local/gocache$" "${GO_LOG}" >/dev/null 2>&1; then
  echo "expected GOCACHE fallback for empty environment"
  cat "${GO_LOG}"
  exit 1
fi

echo "appctl go env fallback test passed"
