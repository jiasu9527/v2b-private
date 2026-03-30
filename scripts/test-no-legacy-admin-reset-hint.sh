#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGET="${REPO_ROOT}/public/assets/admin/umi.js"

if [[ ! -f "${TARGET}" ]]; then
  echo "missing ${TARGET}"
  exit 1
fi

if rg -n "php artisan reset:password" "${TARGET}" >/dev/null 2>&1; then
  echo "found legacy php artisan reset hint in admin bundle"
  exit 1
fi

if ! rg -n "\\./scripts/appctl create-admin" "${TARGET}" >/dev/null 2>&1; then
  echo "missing go appctl reset hint in admin bundle"
  exit 1
fi

echo "admin reset hint test passed"
