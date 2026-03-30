#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/fakebin" "${TMP_DIR}/.git"
cp "${REPO_ROOT}/update.sh" "${TMP_DIR}/update.sh"
chmod +x "${TMP_DIR}/update.sh"

cat > "${TMP_DIR}/scripts/appctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${APPCTL_LOG}"
case "${1:-}" in
  prompt-db)
    cat >/dev/null || true
    ;;
  update|restart)
    ;;
  *)
    echo "unexpected appctl command: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/fakebin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
exit 0
EOF
chmod +x "${TMP_DIR}/fakebin/git"

APPCTL_LOG="${TMP_DIR}/appctl.log"
export APPCTL_LOG

printf 'n\n' | PATH="${TMP_DIR}/fakebin:${PATH}" FORCE_INTERACTIVE_DB_CONFIG=1 bash "${TMP_DIR}/update.sh"

EXPECTED=$'prompt-db --optional\nupdate\nrestart'
ACTUAL="$(cat "${APPCTL_LOG}")"
if [[ "${ACTUAL}" != "${EXPECTED}" ]]; then
  echo "unexpected appctl call order"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED}" "${ACTUAL}"
  exit 1
fi

echo "update interactive test passed"
