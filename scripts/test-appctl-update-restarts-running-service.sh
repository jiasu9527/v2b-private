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

PATH="/usr/bin:/bin" "${TMP_DIR}/scripts/appctl" start >/tmp/test-appctl-update-restarts-start.out 2>/tmp/test-appctl-update-restarts-start.err

OLD_PID="$(cat "${TMP_DIR}/go-api/run/forest.pid")"
if ! kill -0 "${OLD_PID}" 2>/dev/null; then
  echo "expected old service process to be running"
  exit 1
fi

: > "${GO_LOG}"
PATH="/usr/bin:/bin" "${TMP_DIR}/scripts/appctl" update >/tmp/test-appctl-update-restarts.out 2>/tmp/test-appctl-update-restarts.err

EXPECTED=$'run ./cmd/ops migrate-config --target-root ..\nrun ./cmd/ops update --sql ../database/update.pgsql.sql --dsn host=127.0.0.1 port=5432 user=updateuser dbname=forest sslmode=disable password=updatepass\nmod tidy\nbuild -o '"${TMP_DIR}"'/go-api/bin/forest ./cmd/server'
ACTUAL="$(cat "${GO_LOG}")"
if [[ "${ACTUAL}" != "${EXPECTED}" ]]; then
  echo "unexpected update command order"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED}" "${ACTUAL}"
  exit 1
fi

if ! rg -n '检测到 Go API 正在运行，已自动重启，PID ' /tmp/test-appctl-update-restarts.out >/dev/null 2>&1; then
  echo "expected auto restart message after update"
  cat /tmp/test-appctl-update-restarts.out
  exit 1
fi

NEW_PID="$(cat "${TMP_DIR}/go-api/run/forest.pid")"
if [[ "${NEW_PID}" == "${OLD_PID}" ]]; then
  echo "expected update to restart running service with a new pid"
  cat /tmp/test-appctl-update-restarts.out
  exit 1
fi

if ! kill -0 "${NEW_PID}" 2>/dev/null; then
  echo "expected restarted service process to be running"
  exit 1
fi

if kill -0 "${OLD_PID}" 2>/dev/null; then
  echo "expected old service process to be stopped"
  exit 1
fi

PATH="/usr/bin:/bin" "${TMP_DIR}/scripts/appctl" stop >/tmp/test-appctl-update-restarts-stop.out 2>/tmp/test-appctl-update-restarts-stop.err

echo "appctl update restart test passed"
