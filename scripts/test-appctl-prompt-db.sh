#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/.env.go.example" <<'EOF'
APP_KEY=seed-key
POSTGRES_DSN=
DB_HOST=127.0.0.1
DB_PORT=5432
DB_DATABASE=forest
DB_USERNAME=postgres
DB_PASSWORD=
DB_SSLMODE=disable
EOF

cat > "${TMP_DIR}/.env.go" <<'EOF'
APP_KEY=seed-key
POSTGRES_DSN=postgres://old-user:old-pass@10.0.0.8:5432/olddb?sslmode=disable
DB_HOST=10.0.0.8
DB_PORT=5432
DB_DATABASE=olddb
DB_USERNAME=old-user
DB_PASSWORD=old-pass
DB_SSLMODE=disable
EOF

printf 'db.internal\n5433\nforestgo\nforest_user\nforest_pass\nrequire\n' | "${TMP_DIR}/scripts/appctl" prompt-db >/tmp/test-appctl-prompt-db.out 2>/tmp/test-appctl-prompt-db.err

if ! rg -n '^DB_HOST=db.internal$' "${TMP_DIR}/.env.go" >/dev/null 2>&1; then
  echo "expected DB_HOST to be updated"
  cat "${TMP_DIR}/.env.go"
  exit 1
fi

if ! rg -n '^DB_PORT=5433$' "${TMP_DIR}/.env.go" >/dev/null 2>&1; then
  echo "expected DB_PORT to be updated"
  cat "${TMP_DIR}/.env.go"
  exit 1
fi

if ! rg -n '^DB_DATABASE=forestgo$' "${TMP_DIR}/.env.go" >/dev/null 2>&1; then
  echo "expected DB_DATABASE to be updated"
  cat "${TMP_DIR}/.env.go"
  exit 1
fi

if ! rg -n '^DB_USERNAME=forest_user$' "${TMP_DIR}/.env.go" >/dev/null 2>&1; then
  echo "expected DB_USERNAME to be updated"
  cat "${TMP_DIR}/.env.go"
  exit 1
fi

if ! rg -n '^DB_PASSWORD=forest_pass$' "${TMP_DIR}/.env.go" >/dev/null 2>&1; then
  echo "expected DB_PASSWORD to be updated"
  cat "${TMP_DIR}/.env.go"
  exit 1
fi

if ! rg -n '^DB_SSLMODE=require$' "${TMP_DIR}/.env.go" >/dev/null 2>&1; then
  echo "expected DB_SSLMODE to be updated"
  cat "${TMP_DIR}/.env.go"
  exit 1
fi

if ! rg -n '^POSTGRES_DSN=host=db.internal port=5433 user=forest_user dbname=forestgo sslmode=require password=forest_pass$' "${TMP_DIR}/.env.go" >/dev/null 2>&1; then
  echo "expected POSTGRES_DSN to be regenerated"
  cat "${TMP_DIR}/.env.go"
  exit 1
fi

cat > "${TMP_DIR}/.env.go" <<'EOF'
APP_KEY=seed-key
DB_HOST=keep-host
DB_PORT=5432
DB_DATABASE=keepdb
DB_USERNAME=keepuser
DB_PASSWORD=keeppass
DB_SSLMODE=disable
POSTGRES_DSN=host=keep-host port=5432 user=keepuser dbname=keepdb sslmode=disable password=keeppass
EOF

printf 'n\n' | "${TMP_DIR}/scripts/appctl" prompt-db --optional >/tmp/test-appctl-prompt-db-skip.out 2>/tmp/test-appctl-prompt-db-skip.err

if ! rg -n '^DB_HOST=keep-host$' "${TMP_DIR}/.env.go" >/dev/null 2>&1; then
  echo "expected optional prompt to preserve env when skipped"
  cat "${TMP_DIR}/.env.go"
  exit 1
fi

echo "prompt-db test passed"
