#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUNDLE="${ROOT_DIR}/public/assets/admin/umi.js"

if python3 - "${BUNDLE}" <<'PY'
from pathlib import Path
import sys

bundle = Path(sys.argv[1]).read_text()
if '(S()(t.host),' in bundle:
    raise SystemExit(1)
if 'S()(t.host + ":" + t.port)' not in bundle:
    raise SystemExit(1)
PY
then
  echo "admin node address copy bundle test passed"
else
  echo "admin node address copy bundle test failed"
  exit 1
fi
