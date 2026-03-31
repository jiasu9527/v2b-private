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
case "${1:-}" in
  config|fetch|pull|rev-parse)
    if [[ "${1:-}" == "rev-parse" ]]; then
      echo "0123456789abcdef0123456789abcdef01234567"
    fi
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
EOF
chmod +x "${TMP_DIR}/fakebin/git"

APPCTL_LOG="${TMP_DIR}/appctl.log"
export APPCTL_LOG

printf 'n\n' | PATH="${TMP_DIR}/fakebin:${PATH}" FORCE_INTERACTIVE_DB_CONFIG=1 bash "${TMP_DIR}/update.sh" >/tmp/test-update-interactive.out 2>/tmp/test-update-interactive.err

EXPECTED=$'prompt-db --optional\nupdate'
ACTUAL="$(cat "${APPCTL_LOG}")"
if [[ "${ACTUAL}" != "${EXPECTED}" ]]; then
  echo "unexpected appctl call order"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED}" "${ACTUAL}"
  exit 1
fi

if ! rg -n '正在同步代码' /tmp/test-update-interactive.out >/dev/null 2>&1; then
  echo "expected chinese sync message"
  cat /tmp/test-update-interactive.out
  exit 1
fi

if ! rg -n '代码已经是最新版本' /tmp/test-update-interactive.out >/dev/null 2>&1; then
  echo "expected chinese up-to-date message"
  cat /tmp/test-update-interactive.out
  exit 1
fi

echo "update interactive test passed"
