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
if [[ "${1:-}" == "env-file" ]]; then
  echo "/tmp/fake.env.go"
fi
exit 0
APPCTL
chmod +x "${TMP_DIR}/scripts/appctl"

touch "${TMP_DIR}/go-api/run/forest-go-api.log"
printf '%s\n' "$$" > "${TMP_DIR}/go-api/run/forest-go-api.pid"

cat > "${TMP_DIR}/fake-git" <<'GIT'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "-C" ]]; then
  shift 2
fi
if [[ "${1:-}" == "rev-parse" && "${2:-}" == "--short=7" && "${3:-}" == "HEAD" ]]; then
  echo "18702c3"
  exit 0
fi
exit 1
GIT
chmod +x "${TMP_DIR}/fake-git"

printf '0\n' | FORCE_COLOR=1 GIT_BIN="${TMP_DIR}/fake-git" PID_PATH="${TMP_DIR}/go-api/run/forest-go-api.pid" LOG_PATH="${TMP_DIR}/go-api/run/forest-go-api.log" APPCTL_BIN="${TMP_DIR}/scripts/appctl" bash "${TMP_DIR}/menu.sh" >/tmp/test-menu-dashboard.out 2>/tmp/test-menu-dashboard.err

if ! grep -q $'\033\\[32m运行中' /tmp/test-menu-dashboard.out; then
  echo "expected green running status in dashboard"
  cat /tmp/test-menu-dashboard.out
  exit 1
fi

if ! rg -n '^服务:[[:space:]]+' /tmp/test-menu-dashboard.out >/dev/null 2>&1; then
  echo "expected service row in dashboard"
  cat /tmp/test-menu-dashboard.out
  exit 1
fi

if ! rg -n '^PID:[[:space:]]+.+$' /tmp/test-menu-dashboard.out >/dev/null 2>&1; then
  echo "expected pid row in dashboard"
  cat /tmp/test-menu-dashboard.out
  exit 1
fi

if ! rg -n '^版本:[[:space:]]+18702c3$' /tmp/test-menu-dashboard.out >/dev/null 2>&1; then
  echo "expected version row in dashboard"
  cat /tmp/test-menu-dashboard.out
  exit 1
fi

if ! rg -n '^数据库:[[:space:]]+PostgreSQL$' /tmp/test-menu-dashboard.out >/dev/null 2>&1; then
  echo "expected database row in dashboard"
  cat /tmp/test-menu-dashboard.out
  exit 1
fi

if ! rg -n '^环境文件:[[:space:]]+fake\.env\.go$' /tmp/test-menu-dashboard.out >/dev/null 2>&1; then
  echo "expected env file row in dashboard"
  cat /tmp/test-menu-dashboard.out
  exit 1
fi

if rg -n 'Go API \+ PostgreSQL 单机版' /tmp/test-menu-dashboard.out >/dev/null 2>&1; then
  echo "expected subtitle to be removed"
  cat /tmp/test-menu-dashboard.out
  exit 1
fi

if ! rg -n '^1\. 服务管理$' /tmp/test-menu-dashboard.out >/dev/null 2>&1; then
  echo "expected service menu entry first"
  cat /tmp/test-menu-dashboard.out
  exit 1
fi

if ! rg -n '^2\. 安装更新$' /tmp/test-menu-dashboard.out >/dev/null 2>&1; then
  echo "expected install menu entry second"
  cat /tmp/test-menu-dashboard.out
  exit 1
fi

if rg -n '项目目录:|日志文件:' /tmp/test-menu-dashboard.out >/dev/null 2>&1; then
  echo "expected dashboard to stay concise"
  cat /tmp/test-menu-dashboard.out
  exit 1
fi

echo "menu dashboard test passed"
