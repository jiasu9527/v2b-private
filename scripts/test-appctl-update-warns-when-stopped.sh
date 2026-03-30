#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/.env.go" <<'EOF'
DB_HOST=127.0.0.1
DB_PORT=5432
DB_DATABASE=forest
DB_USERNAME=warnuser
DB_PASSWORD=warnpass
EOF

cat > "${TMP_DIR}/fake-go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GO_LOG}"
exit 0
EOF
chmod +x "${TMP_DIR}/fake-go"

GO_LOG="${TMP_DIR}/go.log"
export GO_LOG

GO_BIN="${TMP_DIR}/fake-go" "${TMP_DIR}/scripts/appctl" update >/tmp/test-appctl-update-warns.out 2>/tmp/test-appctl-update-warns.err

EXPECTED=$'run ./cmd/ops migrate-config --target-root ..\nrun ./cmd/ops update --sql ../database/update.pgsql.sql --dsn host=127.0.0.1 port=5432 user=warnuser dbname=forest sslmode=disable password=warnpass\nmod tidy\nbuild -o '"${TMP_DIR}"'/go-api/bin/forest-go-api ./cmd/server'
ACTUAL="$(cat "${GO_LOG}")"
if [[ "${ACTUAL}" != "${EXPECTED}" ]]; then
  echo "unexpected update command order when service is stopped"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED}" "${ACTUAL}"
  exit 1
fi

if ! rg -n '当前 Go API 未运行，更新已完成，如需立即生效请执行 ./scripts/appctl start' /tmp/test-appctl-update-warns.out >/dev/null 2>&1; then
  echo "expected stopped-service warning after update"
  cat /tmp/test-appctl-update-warns.out
  exit 1
fi

echo "appctl update stopped warning test passed"
