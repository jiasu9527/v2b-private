#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ $# -gt 0 ]]; then
  "${ROOT_DIR}/scripts/appctl" install-legacy "$1"
else
  "${ROOT_DIR}/scripts/appctl" install
fi
