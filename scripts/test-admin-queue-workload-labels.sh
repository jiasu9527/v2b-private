#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUNDLE="${REPO_ROOT}/public/assets/admin/umi.js"

if ! rg -n 'stat_refresh:' "${BUNDLE}" >/dev/null 2>&1; then
  echo 'expected admin queue monitor to map stat_refresh label'
  exit 1
fi

if ! rg -n 'maintenance_cleanup:' "${BUNDLE}" >/dev/null 2>&1; then
  echo 'expected admin queue monitor to map maintenance_cleanup label'
  exit 1
fi

if ! rg -n 'return t\[e\][[:space:]]*\|\|[[:space:]]*e' "${BUNDLE}" >/dev/null 2>&1; then
  echo 'expected admin queue monitor to fallback to raw queue name when label map misses'
  exit 1
fi

echo 'admin queue workload labels test passed'
