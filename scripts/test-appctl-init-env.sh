#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/.env.example" <<'EOF'
APP_NAME=legacy-template
EOF

cat > "${TMP_DIR}/.env.go.example" <<'EOF'
APP_NAME=forest-go-api
APP_KEY=
POSTGRES_DSN=
ADMIN_EMAIL=admin@example.com
EOF

cat > "${TMP_DIR}/.env" <<'EOF'
APP_KEY=base64:legacy-secret
DB_CONNECTION=mysql
DB_HOST=127.0.0.1
DB_PORT=3306
DB_DATABASE=legacy_db
DB_USERNAME=legacy_user
DB_PASSWORD=legacy_pass
REDIS_HOST=127.0.0.1
EOF

OUTPUT1="$("${TMP_DIR}/scripts/appctl" init-env 2>&1)"

if [[ "${OUTPUT1}" != *"${TMP_DIR}/.env.go"* ]]; then
  echo "expected init-env to report .env.go path, got ${OUTPUT1}"
  exit 1
fi

if [[ ! -f "${TMP_DIR}/.env.go" ]]; then
  echo "expected .env.go to be created"
  exit 1
fi

if ! rg -n "^APP_NAME=forest-go-api$" "${TMP_DIR}/.env.go" >/dev/null 2>&1; then
  echo "expected .env.go to be copied from .env.go.example"
  exit 1
fi

if ! rg -n "^APP_KEY=base64:legacy-secret$" "${TMP_DIR}/.env.go" >/dev/null 2>&1; then
  echo "expected init-env to import APP_KEY from legacy .env"
  exit 1
fi

printf 'APP_NAME=custom\n' > "${TMP_DIR}/.env.go"
OUTPUT2="$("${TMP_DIR}/scripts/appctl" init-env 2>&1)"

if ! rg -n "^APP_NAME=custom$" "${TMP_DIR}/.env.go" >/dev/null 2>&1; then
  echo "expected existing .env.go to be preserved"
  exit 1
fi

if [[ "${OUTPUT2}" != *"已存在，无需重复初始化"* ]]; then
  echo "expected existing-file message, got ${OUTPUT2}"
  exit 1
fi

echo "init-env test passed"
