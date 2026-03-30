#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/.env.go.example" <<'EOF'
APP_NAME=forest-go-api
APP_KEY=
APP_URL=http://localhost
POSTGRES_DSN=
DB_HOST=localhost
DB_PORT=5432
DB_DATABASE=forest
DB_USERNAME=postgres
DB_PASSWORD=
DB_SSLMODE=disable
ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD=
EOF

cat > "${TMP_DIR}/.env" <<'EOF'
APP_KEY=base64:legacy-secret
APP_URL=https://legacy.example.com
ADMIN_EMAIL=legacy-admin@example.com
DB_CONNECTION=mysql
DB_HOST=127.0.0.1
DB_PORT=3306
DB_DATABASE=legacy_db
DB_USERNAME=legacy_user
DB_PASSWORD=legacy_pass
REDIS_HOST=127.0.0.1
EOF

printf 'pg.internal\n5432\nforest_go\npg_user\npg_pass\ndisable\n' | FORCE_INTERACTIVE_DB_CONFIG=1 "${TMP_DIR}/scripts/appctl" prompt-db >/tmp/test-appctl-legacy-env-import.out 2>/tmp/test-appctl-legacy-env-import.err

for expected in \
  "APP_KEY=base64:legacy-secret" \
  "APP_URL=https://legacy.example.com" \
  "ADMIN_EMAIL=legacy-admin@example.com" \
  "POSTGRES_DSN=host=pg.internal port=5432 user=pg_user dbname=forest_go sslmode=disable password=pg_pass"
do
  if ! rg -n -F "${expected}" "${TMP_DIR}/.env.go" >/dev/null 2>&1; then
    echo "missing migrated env value: ${expected}"
    cat "${TMP_DIR}/.env.go"
    exit 1
  fi
done

echo "legacy env import test passed"
