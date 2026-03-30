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

OUTPUT="$("${TMP_DIR}/scripts/appctl" service-template 2>&1)"

if [[ "${OUTPUT}" != *"[Unit]"* ]]; then
  echo "expected systemd unit header"
  echo "${OUTPUT}"
  exit 1
fi

if [[ "${OUTPUT}" != *"EnvironmentFile=-${TMP_DIR}/.env.go"* ]]; then
  echo "expected service template to reference .env.go"
  echo "${OUTPUT}"
  exit 1
fi

if [[ "${OUTPUT}" != *"ExecStart=${TMP_DIR}/scripts/appctl run"* ]]; then
  echo "expected service template to use foreground run command"
  echo "${OUTPUT}"
  exit 1
fi

if [[ "${OUTPUT}" != *"WorkingDirectory=${TMP_DIR}"* ]]; then
  echo "expected service working directory"
  echo "${OUTPUT}"
  exit 1
fi

echo "service-template test passed"
