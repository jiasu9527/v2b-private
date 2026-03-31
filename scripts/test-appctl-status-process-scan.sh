#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api" "${TMP_DIR}/.local/go/bin"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/.env.go" <<'EOF'
DB_HOST=127.0.0.1
DB_PORT=5432
DB_DATABASE=forest
DB_USERNAME=statususer
DB_PASSWORD=statuspass
EOF

cat > "${TMP_DIR}/.local/go/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GO_LOG}"
if [[ "${1:-}" == "build" && "${2:-}" == "-o" ]]; then
  out="${3}"
  mkdir -p "$(dirname "${out}")"
  cat > "${out}" <<'APP'
#!/usr/bin/env bash
trap 'exit 0' TERM INT
while true; do
  sleep 1
done
APP
  chmod +x "${out}"
fi
exit 0
EOF
chmod +x "${TMP_DIR}/.local/go/bin/go"

GO_LOG="${TMP_DIR}/go.log"
export GO_LOG

PATH="/usr/bin:/bin" "${TMP_DIR}/scripts/appctl" start >/tmp/test-appctl-status-process-start.out 2>/tmp/test-appctl-status-process-start.err
OLD_PID="$(cat "${TMP_DIR}/go-api/run/forest.pid")"
rm -f "${TMP_DIR}/go-api/run/forest.pid"

cat > "${TMP_DIR}/fake-pgrep" <<EOF
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "\$*" >> "${TMP_DIR}/pgrep.log"
echo "${OLD_PID}"
EOF
chmod +x "${TMP_DIR}/fake-pgrep"

PGREP_BIN="${TMP_DIR}/fake-pgrep" "${TMP_DIR}/scripts/appctl" status >/tmp/test-appctl-status-process.out 2>/tmp/test-appctl-status-process.err

if ! rg -n "运行中，PID ${OLD_PID}（进程扫描）" /tmp/test-appctl-status-process.out >/dev/null 2>&1; then
  echo "expected process-scan status with pid"
  cat /tmp/test-appctl-status-process.out
  exit 1
fi

kill "${OLD_PID}" 2>/dev/null || true
wait "${OLD_PID}" 2>/dev/null || true

echo "appctl process-scan status test passed"
