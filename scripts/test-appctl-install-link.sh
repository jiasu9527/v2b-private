#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/bin"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
cp "${REPO_ROOT}/menu.sh" "${TMP_DIR}/menu.sh"
chmod +x "${TMP_DIR}/scripts/appctl" "${TMP_DIR}/menu.sh"

cat > "${TMP_DIR}/fake-appctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${APPCTL_LOG}"
EOF
chmod +x "${TMP_DIR}/fake-appctl"

APPCTL_LOG="${TMP_DIR}/appctl.log"
export APPCTL_LOG

OUTPUT="$("${TMP_DIR}/scripts/appctl" install-link "${TMP_DIR}/bin" 2>&1)"

if [[ ! -L "${TMP_DIR}/bin/forest" ]]; then
  echo "expected forest symlink"
  ls -la "${TMP_DIR}/bin"
  exit 1
fi

TARGET="$(readlink "${TMP_DIR}/bin/forest")"
if [[ "${TARGET}" != "${TMP_DIR}/menu.sh" ]]; then
  echo "unexpected forest symlink target"
  printf 'target=%s\n' "${TARGET}"
  exit 1
fi

if [[ "${OUTPUT}" != *"已安装全局命令"* ]]; then
  echo "expected install-link output"
  printf '%s\n' "${OUTPUT}"
  exit 1
fi

printf '1\n1\n\n0\n0\n' | APPCTL_BIN="${TMP_DIR}/fake-appctl" "${TMP_DIR}/bin/forest" >/tmp/test-appctl-install-link.out 2>/tmp/test-appctl-install-link.err

if [[ "$(cat "${APPCTL_LOG}")" != "install" ]]; then
  echo "expected forest command to route into menu/appctl"
  cat "${APPCTL_LOG}"
  exit 1
fi

: > "${APPCTL_LOG}"
APPCTL_BIN="${TMP_DIR}/fake-appctl" "${TMP_DIR}/bin/forest" install-legacy /legacy/site >/tmp/test-appctl-install-link-cli.out 2>/tmp/test-appctl-install-link-cli.err

if [[ "$(cat "${APPCTL_LOG}")" != "install-legacy /legacy/site" ]]; then
  echo "expected forest cli passthrough to appctl"
  cat "${APPCTL_LOG}"
  exit 1
fi

echo "install-link test passed"
