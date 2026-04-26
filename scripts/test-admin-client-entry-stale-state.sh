#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUNDLE="${ROOT_DIR}/public/assets/admin/umi.js"

python3 - "${BUNDLE}" <<'PY'
from pathlib import Path
import sys

bundle = Path(sys.argv[1]).read_text()

required = {
    "fetch clears stale route rows": 'fetchLoading: !0,\n                                                    routes: [],',
    "client entry modal opens from latest props": 'visible: !0,\n                                route: this.props.route || {},',
}

missing = [name for name, needle in required.items() if needle not in bundle]
if missing:
    for name in missing:
        print(f"missing: {name}", file=sys.stderr)
    raise SystemExit(1)
PY

echo "admin client-entry stale state bundle test passed"
