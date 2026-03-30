#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api" "${TMP_DIR}/database"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/.env.go.example" <<'EOF'
POSTGRES_DSN=host=pg.internal port=5432 user=pg_user dbname=forest_go sslmode=disable password=pg_pass
EOF

cat > "${TMP_DIR}/.env" <<'EOF'
DB_CONNECTION=mysql
DB_HOST=mysql.internal
DB_PORT=3307
DB_DATABASE=forest_legacy
DB_USERNAME=legacy_user
DB_PASSWORD=legacy_pass
REDIS_HOST=127.0.0.1
QUEUE_CONNECTION=sync
EOF

cat > "${TMP_DIR}/fake-go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GO_LOG}"
if [[ "${1:-}" == "run" && "${2:-}" == "./cmd/ops" && "${3:-}" == "migrate-mysql" ]]; then
  if [[ "$*" == *"--reset-target"* ]]; then
    echo "migration_status=applied"
  else
    echo "migration_status=skipped"
    echo "migration_reason=target_not_empty"
  fi
fi
exit 0
EOF
chmod +x "${TMP_DIR}/fake-go"

GO_LOG="${TMP_DIR}/go.log"
export GO_LOG

FORCE_MYSQL_OVERWRITE=1 GO_BIN="${TMP_DIR}/fake-go" "${TMP_DIR}/scripts/appctl" update >/tmp/test-appctl-auto-migrate-force-overwrite.out 2>/tmp/test-appctl-auto-migrate-force-overwrite.err

if ! rg -n '已启用 FORCE_MYSQL_OVERWRITE=1，开始清空 PostgreSQL 目标库并按 MySQL 全量覆盖' /tmp/test-appctl-auto-migrate-force-overwrite.out >/dev/null 2>&1; then
  echo "expected force-overwrite message"
  cat /tmp/test-appctl-auto-migrate-force-overwrite.out
  exit 1
fi

EXPECTED=$'run ./cmd/ops migrate-config --target-root ..\nrun ./cmd/ops migrate-mysql --source-env ../.env --install-sql ../database/install.pgsql.sql --target-dsn host=pg.internal port=5432 user=pg_user dbname=forest_go sslmode=disable password=pg_pass --reset-target\nmod tidy\nbuild -o '"${TMP_DIR}"'/go-api/bin/forest-go-api ./cmd/server'
ACTUAL="$(cat "${GO_LOG}")"
if [[ "${ACTUAL}" != "${EXPECTED}" ]]; then
  echo "unexpected force-overwrite command order"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED}" "${ACTUAL}"
  exit 1
fi

if rg -n -F "run ./cmd/ops update" "${GO_LOG}" >/dev/null 2>&1; then
  echo "unexpected plain postgres update after forced overwrite"
  cat "${GO_LOG}"
  exit 1
fi

echo "appctl auto-migrate force-overwrite test passed"
