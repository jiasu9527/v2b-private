#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts"
cp "${REPO_ROOT}/menu.sh" "${TMP_DIR}/menu.sh"
chmod +x "${TMP_DIR}/menu.sh"

cat > "${TMP_DIR}/scripts/appctl" <<'APPCTL'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "env-file" ]]; then
  echo "/tmp/fake.env.go"
  exit 0
fi
printf '%s\n' "$*" >> "${APPCTL_LOG}"
APPCTL
chmod +x "${TMP_DIR}/scripts/appctl"

APPCTL_LOG="${TMP_DIR}/appctl.log"
export APPCTL_LOG

printf '2\n1\n\n0\n0\n' | APPCTL_BIN="${TMP_DIR}/scripts/appctl" bash "${TMP_DIR}/menu.sh" >/tmp/test-menu-routing-install.out 2>/tmp/test-menu-routing-install.err
if [[ "$(cat "${APPCTL_LOG}")" != "install" ]]; then
  echo "expected install action"
  cat "${APPCTL_LOG}"
  exit 1
fi

: > "${APPCTL_LOG}"
printf '4\n2\n\n0\n0\n' | APPCTL_BIN="${TMP_DIR}/scripts/appctl" bash "${TMP_DIR}/menu.sh" >/tmp/test-menu-routing-migrate.out 2>/tmp/test-menu-routing-migrate.err
if [[ "$(cat "${APPCTL_LOG}")" != "migrate-mysql" ]]; then
  echo "expected migrate-mysql action"
  cat "${APPCTL_LOG}"
  exit 1
fi

: > "${APPCTL_LOG}"
printf '1\n3\n\n0\n0\n' | APPCTL_BIN="${TMP_DIR}/scripts/appctl" bash "${TMP_DIR}/menu.sh" >/tmp/test-menu-routing-restart.out 2>/tmp/test-menu-routing-restart.err
if [[ "$(cat "${APPCTL_LOG}")" != $'stop\nstart' ]]; then
  echo "expected restart actions"
  cat "${APPCTL_LOG}"
  exit 1
fi

: > "${APPCTL_LOG}"
printf '2\n3\n/legacy/site\n\n0\n0\n' | APPCTL_BIN="${TMP_DIR}/scripts/appctl" bash "${TMP_DIR}/menu.sh" >/tmp/test-menu-routing-install-legacy.out 2>/tmp/test-menu-routing-install-legacy.err
if [[ "$(cat "${APPCTL_LOG}")" != "install-legacy /legacy/site" ]]; then
  echo "expected install-legacy action"
  cat "${APPCTL_LOG}"
  exit 1
fi

: > "${APPCTL_LOG}"
printf '4\n9\n\n0\n0\n' | APPCTL_BIN="${TMP_DIR}/scripts/appctl" bash "${TMP_DIR}/menu.sh" >/tmp/test-menu-routing-uninstall.out 2>/tmp/test-menu-routing-uninstall.err
if [[ "$(cat "${APPCTL_LOG}")" != "uninstall" ]]; then
  echo "expected uninstall action"
  cat "${APPCTL_LOG}"
  exit 1
fi

echo "menu routing test passed"
