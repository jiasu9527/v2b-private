#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api/run"
cp "${REPO_ROOT}/menu.sh" "${TMP_DIR}/menu.sh"
chmod +x "${TMP_DIR}/menu.sh"

cat > "${TMP_DIR}/scripts/appctl" <<'APPCTL'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "env-file" ]]; then
  echo "/tmp/fake.env.go"
  exit 0
fi
if [[ "${1:-}" == "status" ]]; then
  echo "运行中，PID 43210（systemd）"
  exit 0
fi
exit 0
APPCTL
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/fake-git" <<'GIT'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "-C" ]]; then
  shift 2
fi
if [[ "${1:-}" == "rev-parse" && "${2:-}" == "--short=7" && "${3:-}" == "HEAD" ]]; then
  echo "18702c3"
  exit 0
fi
exit 1
GIT
chmod +x "${TMP_DIR}/fake-git"

printf '0\n' | FORCE_COLOR=1 GIT_BIN="${TMP_DIR}/fake-git" APPCTL_BIN="${TMP_DIR}/scripts/appctl" bash "${TMP_DIR}/menu.sh" >/tmp/test-menu-dashboard-systemd.out 2>/tmp/test-menu-dashboard-systemd.err

if ! grep -q $'\033\\[32m运行中' /tmp/test-menu-dashboard-systemd.out; then
  echo "expected green running status for systemd"
  cat /tmp/test-menu-dashboard-systemd.out
  exit 1
fi

if ! rg -n '^PID:[[:space:]]+43210$' /tmp/test-menu-dashboard-systemd.out >/dev/null 2>&1; then
  echo "expected systemd pid row"
  cat /tmp/test-menu-dashboard-systemd.out
  exit 1
fi

echo "menu dashboard systemd test passed"
