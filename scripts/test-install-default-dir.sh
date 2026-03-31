#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if ! rg -n '"/etc/forest"' "${REPO_ROOT}/install.sh" >/dev/null 2>&1; then
  echo "expected install.sh to default to /etc/forest"
  exit 1
fi

if rg -n '"/www/wwwroot/forest"|"/opt/forest"' "${REPO_ROOT}/install.sh" >/dev/null 2>&1; then
  echo "expected install.sh old default install dirs to be removed"
  exit 1
fi

echo "install default dir test passed"
