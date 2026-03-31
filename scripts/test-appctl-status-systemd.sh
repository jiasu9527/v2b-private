#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/fake-systemctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${SYSTEMCTL_LOG}"
if [[ "${1:-}" == "is-active" && "${2:-}" == "--quiet" && "${3:-}" == "forest" ]]; then
  exit 0
fi
if [[ "${1:-}" == "show" && "${2:-}" == "-p" && "${3:-}" == "MainPID" && "${4:-}" == "--value" && "${5:-}" == "forest" ]]; then
  echo "43210"
  exit 0
fi
exit 0
EOF
chmod +x "${TMP_DIR}/fake-systemctl"

SERVICE_FILE="${TMP_DIR}/forest.service"
cat > "${SERVICE_FILE}" <<'EOF'
[Unit]
Description=forest
EOF

SYSTEMCTL_LOG="${TMP_DIR}/systemctl.log"
export SYSTEMCTL_LOG

FOREST_SERVICE_FILE_PATH="${SERVICE_FILE}" \
SYSTEMCTL_BIN="${TMP_DIR}/fake-systemctl" \
"${TMP_DIR}/scripts/appctl" status >/tmp/test-appctl-status-systemd.out 2>/tmp/test-appctl-status-systemd.err

if ! rg -n '运行中，PID 43210（systemd）' /tmp/test-appctl-status-systemd.out >/dev/null 2>&1; then
  echo "expected systemd status with pid"
  cat /tmp/test-appctl-status-systemd.out
  exit 1
fi

EXPECTED=$'is-active --quiet forest\nshow -p MainPID --value forest'
ACTUAL="$(cat "${SYSTEMCTL_LOG}")"
if [[ "${ACTUAL}" != "${EXPECTED}" ]]; then
  echo "unexpected systemctl call order for status"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED}" "${ACTUAL}"
  exit 1
fi

echo "appctl systemd status test passed"
