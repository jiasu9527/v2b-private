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
DB_USERNAME=updateuser
DB_PASSWORD=updatepass
EOF

cat > "${TMP_DIR}/.local/go/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GO_LOG}"
if [[ "${1:-}" == "run" && "${2:-}" == "./cmd/ops" && "${3:-}" == "update" ]]; then
  if [[ -f "${PID_PATH}" ]]; then
    old_pid="$(cat "${PID_PATH}")"
    if [[ -n "${old_pid}" ]] && kill -0 "${old_pid}" 2>/dev/null; then
      kill "${old_pid}" 2>/dev/null || true
      wait "${old_pid}" 2>/dev/null || true
    fi
  fi
fi
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
PID_PATH="${TMP_DIR}/go-api/run/forest.pid"
export GO_LOG PID_PATH

PATH="/usr/bin:/bin" "${TMP_DIR}/scripts/appctl" start >/tmp/test-appctl-update-restarts-previous-start.out 2>/tmp/test-appctl-update-restarts-previous-start.err

OLD_PID="$(cat "${PID_PATH}")"
if ! kill -0 "${OLD_PID}" 2>/dev/null; then
  echo "expected old service process to be running before update"
  exit 1
fi

: > "${GO_LOG}"
PATH="/usr/bin:/bin" "${TMP_DIR}/scripts/appctl" update >/tmp/test-appctl-update-restarts-previous.out 2>/tmp/test-appctl-update-restarts-previous.err

if ! rg -n -F '更新完成' /tmp/test-appctl-update-restarts-previous.out >/dev/null 2>&1; then
  echo "expected auto restart message for previously running service"
  cat /tmp/test-appctl-update-restarts-previous.out
  exit 1
fi

if ! rg -n -F '服务已重启完成' /tmp/test-appctl-update-restarts-previous.out >/dev/null 2>&1; then
  echo "expected auto restart message for previously running service"
  cat /tmp/test-appctl-update-restarts-previous.out
  exit 1
fi

NEW_PID="$(cat "${PID_PATH}")"
if [[ "${NEW_PID}" == "${OLD_PID}" ]]; then
  echo "expected update to replace the old pid after previous process exited"
  cat /tmp/test-appctl-update-restarts-previous.out
  exit 1
fi

if ! kill -0 "${NEW_PID}" 2>/dev/null; then
  echo "expected restarted service process to be running"
  exit 1
fi

PATH="/usr/bin:/bin" "${TMP_DIR}/scripts/appctl" stop >/tmp/test-appctl-update-restarts-previous-stop.out 2>/tmp/test-appctl-update-restarts-previous-stop.err

echo "appctl update previously-running restart test passed"
