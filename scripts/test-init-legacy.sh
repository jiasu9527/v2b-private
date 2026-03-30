#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

cp "${REPO_ROOT}/init.sh" "${TMP_DIR}/init.sh"
chmod +x "${TMP_DIR}/init.sh"
mkdir -p "${TMP_DIR}/scripts"

cat > "${TMP_DIR}/scripts/appctl" <<'EOFAPP'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${APPCTL_LOG}"
EOFAPP
chmod +x "${TMP_DIR}/scripts/appctl"

APPCTL_LOG="${TMP_DIR}/appctl.log"
export APPCTL_LOG

bash "${TMP_DIR}/init.sh" "/data/legacy-v2board"

if [[ "$(cat "${APPCTL_LOG}")" != "install-legacy /data/legacy-v2board" ]]; then
  echo "expected init.sh to delegate to install-legacy"
  cat "${APPCTL_LOG}"
  exit 1
fi

echo "init legacy entry test passed"
