#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api" "${TMP_DIR}/database" "${TMP_DIR}/legacy/config"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/.env.go.example" <<'ENVGO'
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
ENVGO

cat > "${TMP_DIR}/legacy/.env" <<'ENVLEGACY'
DB_CONNECTION=mysql
DB_HOST=mysql.internal
DB_PORT=3306
DB_DATABASE=legacy_db
DB_USERNAME=legacy_user
DB_PASSWORD=legacy_pass
APP_KEY=legacy-key
APP_URL=https://legacy.example.com
ADMIN_EMAIL=legacy-admin@example.com
QUEUE_CONNECTION=sync
ENVLEGACY

cat > "${TMP_DIR}/legacy/config/v2board.php" <<'PHP'
<?php return ['app_name' => 'legacy'];
PHP

cat > "${TMP_DIR}/fake-go" <<'EOFGO'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GO_LOG}"
if [[ "${1:-}" == "run" && "${2:-}" == "./cmd/ops" && "${3:-}" == "gen-app-key" ]]; then
  echo 'base64:generated-app-key'
  exit 0
fi
if [[ "${1:-}" == "run" && "${2:-}" == "./cmd/ops" && "${3:-}" == "migrate-mysql" ]]; then
  echo 'migration_status=applied'
  exit 0
fi
exit 0
EOFGO
chmod +x "${TMP_DIR}/fake-go"

cat > "${TMP_DIR}/fake-systemctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${SYSTEMCTL_LOG}"
if [[ "${1:-}" == "is-active" && "${2:-}" == "--quiet" && "${3:-}" == "forest-go-api" ]]; then
  exit 1
fi
if [[ "${1:-}" == "show" && "${2:-}" == "-p" && "${3:-}" == "MainPID" && "${4:-}" == "--value" && "${5:-}" == "forest-go-api" ]]; then
  echo "43210"
  exit 0
fi
exit 0
EOF
chmod +x "${TMP_DIR}/fake-systemctl"

GO_LOG="${TMP_DIR}/go.log"
SYSTEMCTL_LOG="${TMP_DIR}/systemctl.log"
export GO_LOG SYSTEMCTL_LOG

printf 'pg.internal\n5432\nforest_go\npg_user\npg_pass\ndisable\n' | \
  GO_BIN="${TMP_DIR}/fake-go" \
  SYSTEMCTL_BIN="${TMP_DIR}/fake-systemctl" \
  FOREST_SERVICE_FILE_PATH="${TMP_DIR}/forest-go-api.service" \
  FORCE_INTERACTIVE_DB_CONFIG=1 \
  bash "${TMP_DIR}/scripts/appctl" install-legacy "${TMP_DIR}/legacy" >/tmp/test-appctl-install-legacy.out 2>/tmp/test-appctl-install-legacy.err

EXPECTED=$'run ./cmd/ops migrate-config --target-root .. --legacy-root ../legacy\nrun ./cmd/ops migrate-mysql --source-env ../legacy/.env --install-sql ../database/install.pgsql.sql --target-dsn host=pg.internal port=5432 user=pg_user dbname=forest_go sslmode=disable password=pg_pass\nmod tidy\nbuild -o '"${TMP_DIR}"'/go-api/bin/forest-go-api ./cmd/server'
ACTUAL="$(cat "${GO_LOG}")"
if [[ "${ACTUAL}" != "${EXPECTED}" ]]; then
  echo "unexpected install-legacy go command order"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED}" "${ACTUAL}"
  exit 1
fi

EXPECTED_SYSTEMCTL=$'daemon-reload\nis-active --quiet forest-go-api\nenable --now forest-go-api\nshow -p MainPID --value forest-go-api'
ACTUAL_SYSTEMCTL="$(cat "${SYSTEMCTL_LOG}")"
if [[ "${ACTUAL_SYSTEMCTL}" != "${EXPECTED_SYSTEMCTL}" ]]; then
  echo "unexpected install-legacy systemctl order"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED_SYSTEMCTL}" "${ACTUAL_SYSTEMCTL}"
  exit 1
fi

for expected in \
  'APP_KEY=legacy-key' \
  'APP_URL=https://legacy.example.com' \
  'ADMIN_EMAIL=legacy-admin@example.com' \
  'POSTGRES_DSN=host=pg.internal port=5432 user=pg_user dbname=forest_go sslmode=disable password=pg_pass'
do
  if ! rg -n -F "${expected}" "${TMP_DIR}/.env.go" >/dev/null 2>&1; then
    echo "missing legacy import value: ${expected}"
    cat "${TMP_DIR}/.env.go"
    exit 1
  fi
done

echo "install-legacy test passed"
