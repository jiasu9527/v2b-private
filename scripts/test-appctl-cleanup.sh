#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/.env.example" <<'EOF'
ADMIN_EMAIL=admin@example.com
EOF

touch "${TMP_DIR}/workerman.log" "${TMP_DIR}/workerman.webman.php.pid"

OUTPUT="$("${TMP_DIR}/scripts/appctl" cleanup 2>&1)"

if [[ -e "${TMP_DIR}/workerman.log" ]]; then
  echo "expected workerman.log to be removed"
  echo "${OUTPUT}"
  exit 1
fi

if [[ -e "${TMP_DIR}/workerman.webman.php.pid" ]]; then
  echo "expected workerman.webman.php.pid to be removed"
  echo "${OUTPUT}"
  exit 1
fi

if [[ "${OUTPUT}" != *"removed 2 legacy runtime artifact"* ]]; then
  echo "unexpected cleanup output: ${OUTPUT}"
  exit 1
fi

echo "cleanup test passed"
