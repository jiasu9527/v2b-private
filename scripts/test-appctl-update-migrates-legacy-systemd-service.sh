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
exit 0
EOF
chmod +x "${TMP_DIR}/.local/go/bin/go"

cat > "${TMP_DIR}/fake-systemctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${SYSTEMCTL_LOG}"
if [[ "${1:-}" == "is-active" && "${2:-}" == "--quiet" && "${3:-}" == "forest" ]]; then
  exit 1
fi
if [[ "${1:-}" == "is-active" && "${2:-}" == "--quiet" && "${3:-}" == "forest-go-api" ]]; then
  exit 0
fi
if [[ "${1:-}" == "show" && "${2:-}" == "-p" && "${3:-}" == "MainPID" && "${4:-}" == "--value" && "${5:-}" == "forest" ]]; then
  echo "43210"
  exit 0
fi
exit 0
EOF
chmod +x "${TMP_DIR}/fake-systemctl"

LEGACY_SERVICE_FILE="${TMP_DIR}/forest-go-api.service"
cat > "${LEGACY_SERVICE_FILE}" <<'EOF'
[Unit]
Description=forest-go-api
EOF

GO_LOG="${TMP_DIR}/go.log"
SYSTEMCTL_LOG="${TMP_DIR}/systemctl.log"
export GO_LOG SYSTEMCTL_LOG

LEGACY_FOREST_SERVICE_FILE_PATH="${LEGACY_SERVICE_FILE}" \
FOREST_SERVICE_FILE_PATH="${TMP_DIR}/forest.service" \
SYSTEMCTL_BIN="${TMP_DIR}/fake-systemctl" \
PATH="/usr/bin:/bin" \
"${TMP_DIR}/scripts/appctl" update >/tmp/test-appctl-update-legacy-systemd.out 2>/tmp/test-appctl-update-legacy-systemd.err

EXPECTED_GO=$'run ./cmd/ops migrate-config --target-root ..\nrun ./cmd/ops update --sql ../database/update.pgsql.sql --dsn host=127.0.0.1 port=5432 user=updateuser dbname=forest sslmode=disable password=updatepass\nmod tidy\nbuild -o '"${TMP_DIR}"'/go-api/bin/forest ./cmd/server'
ACTUAL_GO="$(cat "${GO_LOG}")"
if [[ "${ACTUAL_GO}" != "${EXPECTED_GO}" ]]; then
  echo "unexpected update command order in legacy systemd mode"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED_GO}" "${ACTUAL_GO}"
  exit 1
fi

EXPECTED_SYSTEMCTL=$'is-active --quiet forest-go-api\nis-active --quiet forest-go-api\nstop forest-go-api\ndisable --now forest-go-api\ndaemon-reload\ndaemon-reload\nis-active --quiet forest\nenable --now forest\nshow -p MainPID --value forest'
ACTUAL_SYSTEMCTL="$(cat "${SYSTEMCTL_LOG}")"
if [[ "${ACTUAL_SYSTEMCTL}" != "${EXPECTED_SYSTEMCTL}" ]]; then
  echo "unexpected legacy migration systemctl call order for update"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED_SYSTEMCTL}" "${ACTUAL_SYSTEMCTL}"
  exit 1
fi

if [[ -e "${LEGACY_SERVICE_FILE}" ]]; then
  echo "expected legacy service file to be removed"
  exit 1
fi

if [[ ! -f "${TMP_DIR}/forest.service" ]]; then
  echo "expected new forest service file to be created"
  exit 1
fi

if ! rg -n '已通过 systemd 启动并设置开机自启，PID 43210' /tmp/test-appctl-update-legacy-systemd.out >/dev/null 2>&1; then
  echo "expected legacy systemd service to migrate and start forest"
  cat /tmp/test-appctl-update-legacy-systemd.out
  exit 1
fi

echo "appctl update legacy systemd migration test passed"
