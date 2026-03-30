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

OUTPUT1="$("${TMP_DIR}/scripts/appctl" doctor 2>&1)"

if [[ "${OUTPUT1}" != *"legacy_env_ignored=true"* ]]; then
  echo "expected doctor to flag ignored legacy env"
  echo "${OUTPUT1}"
  exit 1
fi

if [[ "${OUTPUT1}" != *"env_exists=false"* ]]; then
  echo "expected doctor to report missing active env"
  echo "${OUTPUT1}"
  exit 1
fi

if [[ "${OUTPUT1}" != *"postgres_configured=false"* ]]; then
  echo "expected doctor to report missing postgres config"
  echo "${OUTPUT1}"
  exit 1
fi

cat > "${TMP_DIR}/.env.go" <<'EOF'
POSTGRES_DSN=postgres://postgres:secret@127.0.0.1:5432/forest?sslmode=disable
ADMIN_EMAIL=admin@example.com
EOF

OUTPUT2="$("${TMP_DIR}/scripts/appctl" doctor 2>&1)"

if [[ "${OUTPUT2}" != *"env_exists=true"* ]]; then
  echo "expected doctor to report active env file"
  echo "${OUTPUT2}"
  exit 1
fi

if [[ "${OUTPUT2}" != *"postgres_configured=true"* ]]; then
  echo "expected doctor to report postgres configured"
  echo "${OUTPUT2}"
  exit 1
fi

if [[ "${OUTPUT2}" != *"admin_email_configured=true"* ]]; then
  echo "expected doctor to report admin email configured"
  echo "${OUTPUT2}"
  exit 1
fi

echo "doctor test passed"
