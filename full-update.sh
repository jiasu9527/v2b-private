#!/usr/bin/env bash
set -euo pipefail

# Compatibility entrypoint. update.sh/appctl update own the single Git pull,
# database migration, Go build, staged admin build, and service restart.
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec bash "${ROOT_DIR}/update.sh" "$@"
