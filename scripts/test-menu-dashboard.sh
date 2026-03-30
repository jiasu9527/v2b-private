#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
cleanup() {
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api/run"
cp "${REPO_ROOT}/menu.sh" "${TMP_DIR}/menu.sh"
chmod +x "${TMP_DIR}/menu.sh"

cat > "${TMP_DIR}/scripts/appctl" <<'APPCTL'
#!/usr/bin/env bash
set -euo pipefail
exit 0
APPCTL
chmod +x "${TMP_DIR}/scripts/appctl"

touch "${TMP_DIR}/go-api/run/forest-go-api.log"
printf '%s\n' "$$" > "${TMP_DIR}/go-api/run/forest-go-api.pid"

printf '0\n' | PID_PATH="${TMP_DIR}/go-api/run/forest-go-api.pid" LOG_PATH="${TMP_DIR}/go-api/run/forest-go-api.log" APPCTL_BIN="${TMP_DIR}/scripts/appctl" bash "${TMP_DIR}/menu.sh" >/tmp/test-menu-dashboard.out 2>/tmp/test-menu-dashboard.err

if ! rg -n '当前状态:[[:space:]]*运行中' /tmp/test-menu-dashboard.out >/dev/null 2>&1; then
  echo "expected running status in dashboard"
  cat /tmp/test-menu-dashboard.out
  exit 1
fi

if ! rg -n '日志文件:' /tmp/test-menu-dashboard.out >/dev/null 2>&1; then
  echo "expected log path in dashboard"
  cat /tmp/test-menu-dashboard.out
  exit 1
fi

echo "menu dashboard test passed"
