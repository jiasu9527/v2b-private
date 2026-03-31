#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api" "${TMP_DIR}/database"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/.env.go" <<'EOF'
APP_KEY=base64:fixed-app-key
DB_HOST=127.0.0.1
DB_PORT=5432
DB_DATABASE=forest
DB_USERNAME=installuser
DB_PASSWORD=installpass
ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD=install-secret
EOF

cat > "${TMP_DIR}/fake-go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GO_LOG}"
exit 0
EOF
chmod +x "${TMP_DIR}/fake-go"

cat > "${TMP_DIR}/fake-systemctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${SYSTEMCTL_LOG}"
if [[ "${1:-}" == "is-active" && "${2:-}" == "--quiet" && "${3:-}" == "forest" ]]; then
  exit 1
fi
if [[ "${1:-}" == "show" && "${2:-}" == "-p" && "${3:-}" == "MainPID" && "${4:-}" == "--value" && "${5:-}" == "forest" ]]; then
  echo "43210"
  exit 0
fi
exit 0
EOF
chmod +x "${TMP_DIR}/fake-systemctl"

GO_LOG="${TMP_DIR}/go.log"
SYSTEMCTL_LOG="${TMP_DIR}/systemctl.log"
SERVICE_FILE="${TMP_DIR}/forest.service"
export GO_LOG SYSTEMCTL_LOG

FOREST_SERVICE_FILE_PATH="${SERVICE_FILE}" \
GO_BIN="${TMP_DIR}/fake-go" \
SYSTEMCTL_BIN="${TMP_DIR}/fake-systemctl" \
"${TMP_DIR}/scripts/appctl" install >/tmp/test-appctl-install-systemd.out 2>/tmp/test-appctl-install-systemd.err

EXPECTED_GO=$'run ./cmd/ops migrate-config --target-root ..\nrun ./cmd/ops install --sql ../database/install.pgsql.sql --admin-email admin@example.com --admin-password install-secret --dsn host=127.0.0.1 port=5432 user=installuser dbname=forest sslmode=disable password=installpass\nmod tidy\nbuild -o '"${TMP_DIR}"'/go-api/bin/forest ./cmd/server'
ACTUAL_GO="$(cat "${GO_LOG}")"
if [[ "${ACTUAL_GO}" != "${EXPECTED_GO}" ]]; then
  echo "unexpected install command order"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED_GO}" "${ACTUAL_GO}"
  exit 1
fi

EXPECTED_SYSTEMCTL=$'daemon-reload\nis-active --quiet forest\nenable --now forest\nshow -p MainPID --value forest'
ACTUAL_SYSTEMCTL="$(cat "${SYSTEMCTL_LOG}")"
if [[ "${ACTUAL_SYSTEMCTL}" != "${EXPECTED_SYSTEMCTL}" ]]; then
  echo "unexpected systemctl call order for install"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED_SYSTEMCTL}" "${ACTUAL_SYSTEMCTL}"
  exit 1
fi

if [[ ! -f "${SERVICE_FILE}" ]]; then
  echo "expected service file to be written"
  exit 1
fi

if ! rg -n "ExecStart=${TMP_DIR}/scripts/appctl run" "${SERVICE_FILE}" >/dev/null 2>&1; then
  echo "expected service file to use appctl run"
  cat "${SERVICE_FILE}"
  exit 1
fi

if ! rg -n '已通过 systemd 启动并设置开机自启，PID 43210' /tmp/test-appctl-install-systemd.out >/dev/null 2>&1; then
  echo "expected systemd install/start message"
  cat /tmp/test-appctl-install-systemd.out
  exit 1
fi

echo "appctl install systemd enable test passed"
