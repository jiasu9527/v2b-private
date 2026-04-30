#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api" "${TMP_DIR}/.local/go/bin"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/.env.go" <<'EOF'
POSTGRES_DSN=postgres://tester:secret@127.0.0.1:5432/forest?sslmode=disable
EOF

cat > "${TMP_DIR}/.local/go/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GO_LOG}"
if [[ "${1:-}" == "run" && "${2:-}" == "./cmd/ops" && "${3:-}" == "reset-traffic" ]]; then
  echo "traffic reset finished: scanned=3 reset=1 marked_only=1 skipped=1"
  exit 0
fi
exit 0
EOF
chmod +x "${TMP_DIR}/.local/go/bin/go"

GO_LOG="${TMP_DIR}/go.log"
export GO_LOG

PATH="/usr/bin:/bin" "${TMP_DIR}/scripts/appctl" reset-traffic >/tmp/test-appctl-reset-traffic.out 2>/tmp/test-appctl-reset-traffic.err

EXPECTED="run ./cmd/ops reset-traffic --dsn postgres://tester:secret@127.0.0.1:5432/forest?sslmode=disable"
ACTUAL="$(cat "${GO_LOG}")"
if [[ "${ACTUAL}" != "${EXPECTED}" ]]; then
  echo "expected reset-traffic to call ops command"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED}" "${ACTUAL}"
  exit 1
fi

if ! rg -n 'traffic reset finished: scanned=3 reset=1 marked_only=1 skipped=1' /tmp/test-appctl-reset-traffic.out >/dev/null 2>&1; then
  echo "expected reset-traffic output"
  cat /tmp/test-appctl-reset-traffic.out
  exit 1
fi

echo "appctl reset-traffic test passed"
