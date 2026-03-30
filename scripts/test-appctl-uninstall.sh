#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
SERVICE_PID=''
cleanup() {
  if [[ -n "${SERVICE_PID}" ]]; then
    kill "${SERVICE_PID}" 2>/dev/null || true
    wait "${SERVICE_PID}" 2>/dev/null || true
  fi
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api/run" "${TMP_DIR}/bin"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
cp "${REPO_ROOT}/menu.sh" "${TMP_DIR}/menu.sh"
chmod +x "${TMP_DIR}/scripts/appctl" "${TMP_DIR}/menu.sh"

cat > "${TMP_DIR}/fake-systemctl" <<'EOF_SYSTEMCTL'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${SYSTEMCTL_LOG}"
if [[ "${1:-}" == "is-active" && "${2:-}" == "--quiet" && "${3:-}" == "forest-go-api" ]]; then
  exit 0
fi
if [[ "${1:-}" == "stop" && "${2:-}" == "forest-go-api" ]]; then
  if [[ -n "${SERVICE_PID:-}" ]]; then
    kill "${SERVICE_PID}" 2>/dev/null || true
  fi
  exit 0
fi
if [[ "${1:-}" == "disable" && "${2:-}" == "--now" && "${3:-}" == "forest-go-api" ]]; then
  if [[ -n "${SERVICE_PID:-}" ]]; then
    kill "${SERVICE_PID}" 2>/dev/null || true
  fi
  exit 0
fi
EOF_SYSTEMCTL
chmod +x "${TMP_DIR}/fake-systemctl"

SERVICE_FILE="${TMP_DIR}/forest-go-api.service"
cat > "${SERVICE_FILE}" <<'EOF_SERVICE'
[Unit]
Description=forest-go-api
EOF_SERVICE

ln -s "${TMP_DIR}/menu.sh" "${TMP_DIR}/bin/forest"

sleep 30 >/dev/null 2>&1 &
SERVICE_PID=$!
disown "${SERVICE_PID}" 2>/dev/null || true
printf '%s\n' "${SERVICE_PID}" > "${TMP_DIR}/go-api/run/forest-go-api.pid"

SYSTEMCTL_LOG="${TMP_DIR}/systemctl.log"
export SYSTEMCTL_LOG SERVICE_PID

FOREST_LINK_PATH="${TMP_DIR}/bin/forest" \
FOREST_SERVICE_FILE_PATH="${SERVICE_FILE}" \
SYSTEMCTL_BIN="${TMP_DIR}/fake-systemctl" \
HOME="${TMP_DIR}" \
bash "${TMP_DIR}/scripts/appctl" uninstall >/tmp/test-appctl-uninstall.out 2>/tmp/test-appctl-uninstall.err
wait "${SERVICE_PID}" 2>/dev/null || true

if [[ -f "${TMP_DIR}/go-api/run/forest-go-api.pid" ]]; then
  echo "expected pid file to be removed"
  exit 1
fi

if kill -0 "${SERVICE_PID}" 2>/dev/null; then
  echo "expected running process to be stopped"
  exit 1
fi
SERVICE_PID=''

if [[ -e "${SERVICE_FILE}" ]]; then
  echo "expected service file to be removed"
  exit 1
fi

if [[ -e "${TMP_DIR}/bin/forest" ]]; then
  echo "expected forest symlink to be removed"
  exit 1
fi

EXPECTED=$'is-active --quiet forest-go-api\nstop forest-go-api\ndisable --now forest-go-api\ndaemon-reload'
ACTUAL="$(cat "${SYSTEMCTL_LOG}")"
if [[ "${ACTUAL}" != "${EXPECTED}" ]]; then
  echo "unexpected systemctl call order"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED}" "${ACTUAL}"
  exit 1
fi

if ! rg -n '已卸载当前部署' /tmp/test-appctl-uninstall.out >/dev/null 2>&1; then
  echo "expected uninstall summary"
  cat /tmp/test-appctl-uninstall.out
  exit 1
fi

echo "appctl uninstall test passed"
