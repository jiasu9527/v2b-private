#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT_PATH="${ROOT_DIR}/node/install.sh"

if [[ ! -f "${SCRIPT_PATH}" ]]; then
  echo "missing node/install.sh"
  exit 1
fi

bash -n "${SCRIPT_PATH}"
echo "v2node install script test passed"
