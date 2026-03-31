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
if [[ "${1:-}" == "is-active" && "${2:-}" == "--quiet" && "${3:-}" == "forest-go-api" ]]; then
  exit 0
fi
if [[ "${1:-}" == "show" && "${2:-}" == "-p" && "${3:-}" == "MainPID" && "${4:-}" == "--value" && "${5:-}" == "forest-go-api" ]]; then
  echo "43210"
  exit 0
fi
exit 0
EOF
chmod +x "${TMP_DIR}/fake-systemctl"

SERVICE_FILE="${TMP_DIR}/forest-go-api.service"
cat > "${SERVICE_FILE}" <<'EOF'
[Unit]
Description=forest-go-api
EOF

GO_LOG="${TMP_DIR}/go.log"
SYSTEMCTL_LOG="${TMP_DIR}/systemctl.log"
export GO_LOG SYSTEMCTL_LOG

FOREST_SERVICE_FILE_PATH="${SERVICE_FILE}" \
SYSTEMCTL_BIN="${TMP_DIR}/fake-systemctl" \
PATH="/usr/bin:/bin" \
"${TMP_DIR}/scripts/appctl" update >/tmp/test-appctl-update-systemd.out 2>/tmp/test-appctl-update-systemd.err

EXPECTED_GO=$'run ./cmd/ops migrate-config --target-root ..\nrun ./cmd/ops update --sql ../database/update.pgsql.sql --dsn host=127.0.0.1 port=5432 user=updateuser dbname=forest sslmode=disable password=updatepass\nmod tidy\nbuild -o '"${TMP_DIR}"'/go-api/bin/forest-go-api ./cmd/server'
ACTUAL_GO="$(cat "${GO_LOG}")"
if [[ "${ACTUAL_GO}" != "${EXPECTED_GO}" ]]; then
  echo "unexpected update command order in systemd mode"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED_GO}" "${ACTUAL_GO}"
  exit 1
fi

EXPECTED_SYSTEMCTL=$'is-active --quiet forest-go-api\ndaemon-reload\nis-active --quiet forest-go-api\nrestart forest-go-api\nshow -p MainPID --value forest-go-api'
ACTUAL_SYSTEMCTL="$(cat "${SYSTEMCTL_LOG}")"
if [[ "${ACTUAL_SYSTEMCTL}" != "${EXPECTED_SYSTEMCTL}" ]]; then
  echo "unexpected systemctl call order for update"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED_SYSTEMCTL}" "${ACTUAL_SYSTEMCTL}"
  exit 1
fi

if ! rg -n '检测到 systemd 服务正在运行，已自动重启，PID 43210' /tmp/test-appctl-update-systemd.out >/dev/null 2>&1; then
  echo "expected systemd auto restart message after update"
  cat /tmp/test-appctl-update-systemd.out
  exit 1
fi

echo "appctl update systemd restart test passed"
