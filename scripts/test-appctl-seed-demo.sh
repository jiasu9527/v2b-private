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
DB_USERNAME=seeduser
DB_PASSWORD=seedpass
ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD=Seed123456
EOF

cat > "${TMP_DIR}/fake-go" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" > "${FAKE_GO_ARGS_FILE}"
EOF
chmod +x "${TMP_DIR}/fake-go"

ARGS_FILE="${TMP_DIR}/go-args.txt"
FAKE_GO_ARGS_FILE="${ARGS_FILE}" GO_BIN="${TMP_DIR}/fake-go" "${TMP_DIR}/scripts/appctl" seed-demo

if [[ ! -f "${ARGS_FILE}" ]]; then
  echo "expected fake go invocation output"
  exit 1
fi

OUTPUT="$(cat "${ARGS_FILE}")"

if [[ "${OUTPUT}" != *"run ./cmd/ops seed-demo"* ]]; then
  echo "expected seed-demo command invocation"
  echo "${OUTPUT}"
  exit 1
fi

if [[ "${OUTPUT}" != *"--admin-email admin@example.com"* ]]; then
  echo "expected admin email to be forwarded"
  echo "${OUTPUT}"
  exit 1
fi

if [[ "${OUTPUT}" != *"--admin-password Seed123456"* ]]; then
  echo "expected admin password to be forwarded"
  echo "${OUTPUT}"
  exit 1
fi

if [[ "${OUTPUT}" != *"--dsn host=127.0.0.1 port=5432 user=seeduser dbname=forest sslmode=disable password=seedpass"* ]]; then
  echo "expected resolved dsn to be forwarded"
  echo "${OUTPUT}"
  exit 1
fi

echo "seed-demo test passed"
