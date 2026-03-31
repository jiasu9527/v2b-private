#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/config"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
cp -R "${REPO_ROOT}/go-api" "${TMP_DIR}/go-api"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/config/site.php" <<'EOF'
<?php
 return array (
  'secure_path' => 'localadmin',
  'frontend_theme' => 'default',
  'commission_withdraw_method' =>
  array (
    0 => 'USDT',
    1 => '支付宝',
  ),
 ) ;
EOF

OUTPUT="$("${TMP_DIR}/scripts/appctl" migrate-config 2>&1)"

if [[ ! -f "${TMP_DIR}/config/admin.json" ]]; then
  echo "expected config/admin.json to be created"
  echo "${OUTPUT}"
  exit 1
fi

if ! rg -n '"secure_path"[[:space:]]*:[[:space:]]*"localadmin"' "${TMP_DIR}/config/admin.json" >/dev/null 2>&1; then
  echo "expected secure_path migrated into config/admin.json"
  cat "${TMP_DIR}/config/admin.json"
  exit 1
fi

if [[ "${OUTPUT}" != *"config migration finished: admin=1"* ]]; then
  echo "expected migrate-config output to report only admin migration"
  echo "${OUTPUT}"
  exit 1
fi

echo "migrate-config test passed"
