#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/config/theme" "${TMP_DIR}/public/theme/default"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
cp -R "${REPO_ROOT}/go-api" "${TMP_DIR}/go-api"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/config/v2board.php" <<'EOF'
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

cat > "${TMP_DIR}/config/theme/default.php" <<'EOF'
<?php
 return array (
  'theme_color' => 'green',
  'custom_html' => 'hello',
 ) ;
EOF

cat > "${TMP_DIR}/public/theme/default/config.json" <<'EOF'
{
  "name": "default",
  "configs": [
    {"field_name": "theme_color", "default_value": "default"},
    {"field_name": "custom_html"}
  ]
}
EOF

OUTPUT="$("${TMP_DIR}/scripts/appctl" migrate-config 2>&1)"

if [[ ! -f "${TMP_DIR}/config/admin.json" ]]; then
  echo "expected config/admin.json to be created"
  echo "${OUTPUT}"
  exit 1
fi

if [[ ! -f "${TMP_DIR}/config/theme/default.json" ]]; then
  echo "expected config/theme/default.json to be created"
  echo "${OUTPUT}"
  exit 1
fi

if ! rg -n '"secure_path"[[:space:]]*:[[:space:]]*"localadmin"' "${TMP_DIR}/config/admin.json" >/dev/null 2>&1; then
  echo "expected secure_path migrated into config/admin.json"
  cat "${TMP_DIR}/config/admin.json"
  exit 1
fi

if ! rg -n '"theme_color"[[:space:]]*:[[:space:]]*"green"' "${TMP_DIR}/config/theme/default.json" >/dev/null 2>&1; then
  echo "expected theme_color migrated into config/theme/default.json"
  cat "${TMP_DIR}/config/theme/default.json"
  exit 1
fi

echo "migrate-config test passed"
