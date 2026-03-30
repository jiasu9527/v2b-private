#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/.env.example" <<'EOF'
POSTGRES_DSN=
ADMIN_EMAIL=admin@example.com
EOF

cat > "${TMP_DIR}/.env" <<'EOF'
DB_CONNECTION=mysql
DB_HOST=127.0.0.1
DB_PORT=3306
REDIS_HOST=127.0.0.1
QUEUE_CONNECTION=sync
EOF

OUTPUT1="$("${TMP_DIR}/scripts/appctl" env-file 2>&1)"
if [[ "${OUTPUT1}" != "${TMP_DIR}/.env.go" ]]; then
  echo "expected legacy .env to be ignored in favor of .env.go, got ${OUTPUT1}"
  exit 1
fi

cat > "${TMP_DIR}/.env.go" <<'EOF'
POSTGRES_DSN=postgres://postgres:secret@127.0.0.1:5432/forest?sslmode=disable
EOF

OUTPUT2="$("${TMP_DIR}/scripts/appctl" env-file 2>&1)"
if [[ "${OUTPUT2}" != "${TMP_DIR}/.env.go" ]]; then
  echo "expected .env.go to take precedence, got ${OUTPUT2}"
  exit 1
fi

rm -f "${TMP_DIR}/.env.go" "${TMP_DIR}/.env"
cat > "${TMP_DIR}/.env" <<'EOF'
POSTGRES_DSN=postgres://postgres:secret@127.0.0.1:5432/forest?sslmode=disable
DB_HOST=127.0.0.1
DB_PORT=5432
EOF

OUTPUT3="$("${TMP_DIR}/scripts/appctl" env-file 2>&1)"
if [[ "${OUTPUT3}" != "${TMP_DIR}/.env" ]]; then
  echo "expected go-compatible .env to be used, got ${OUTPUT3}"
  exit 1
fi

echo "env-file test passed"
