#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

SOURCE_REPO="${TMP_DIR}/source-repo"
TARGET_DIR="${TMP_DIR}/target-app"
mkdir -p "${SOURCE_REPO}/scripts"

cat > "${SOURCE_REPO}/scripts/appctl" <<'APPCTL'
#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
printf '%s\n' "$*" >> "${APPCTL_LOG}"
case "${1:-}" in
  init-env)
    cat > "${ROOT_DIR}/.env.go" <<'ENV'
POSTGRES_DSN=
ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD=
ENV
    ;;
  env-file)
    echo "${ROOT_DIR}/.env.go"
    ;;
  install-link|install)
    ;;
  install-legacy)
    ;;
  *)
    echo "unexpected appctl command: $*" >&2
    exit 1
    ;;
esac
APPCTL
chmod +x "${SOURCE_REPO}/scripts/appctl"

cat > "${SOURCE_REPO}/menu.sh" <<'MENU'
#!/usr/bin/env bash
exit 0
MENU
chmod +x "${SOURCE_REPO}/menu.sh"

(
  cd "${SOURCE_REPO}"
  git init -b master >/dev/null 2>&1
  git add scripts/appctl menu.sh
  git -c user.name=test -c user.email=test@example.com commit -m 'init' >/dev/null 2>&1
)

APPCTL_LOG="${TMP_DIR}/appctl.log"
export APPCTL_LOG

FOREST_REPO_URL="${SOURCE_REPO}" \
FOREST_INSTALL_DIR="${TARGET_DIR}" \
FOREST_POSTGRES_DSN='postgres://postgres:pass@127.0.0.1:5432/forest?sslmode=disable' \
FOREST_ADMIN_EMAIL='ops@example.com' \
FOREST_ADMIN_PASSWORD='secret-pass' \
FOREST_INSTALL_LINK=1 \
bash "${REPO_ROOT}/install.sh" >/tmp/test-online-install.out 2>/tmp/test-online-install.err

EXPECTED=$'init-env\nenv-file\ninstall-link\ninstall'
ACTUAL="$(cat "${APPCTL_LOG}")"
if [[ "${ACTUAL}" != "${EXPECTED}" ]]; then
  echo "unexpected online install appctl order"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED}" "${ACTUAL}"
  exit 1
fi

for expected in \
  "POSTGRES_DSN=postgres://postgres:pass@127.0.0.1:5432/forest?sslmode=disable" \
  "ADMIN_EMAIL=ops@example.com" \
  "ADMIN_PASSWORD=secret-pass"
do
  if ! rg -n -F "${expected}" "${TARGET_DIR}/.env.go" >/dev/null 2>&1; then
    echo "missing configured value: ${expected}"
    cat "${TARGET_DIR}/.env.go"
    exit 1
  fi
done

: > "${APPCTL_LOG}"
rm -rf "${TARGET_DIR}"

FOREST_REPO_URL="${SOURCE_REPO}" \
FOREST_INSTALL_DIR="${TARGET_DIR}" \
FOREST_POSTGRES_DSN='postgres://postgres:pass@127.0.0.1:5432/forest?sslmode=disable' \
FOREST_ADMIN_EMAIL='ops@example.com' \
FOREST_INSTALL_LINK=0 \
FOREST_LEGACY_ROOT='/legacy/site' \
bash "${REPO_ROOT}/install.sh" >/tmp/test-online-install-legacy.out 2>/tmp/test-online-install-legacy.err

EXPECTED=$'init-env\nenv-file\ninstall-legacy /legacy/site'
ACTUAL="$(cat "${APPCTL_LOG}")"
if [[ "${ACTUAL}" != "${EXPECTED}" ]]; then
  echo "unexpected online legacy install appctl order"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED}" "${ACTUAL}"
  exit 1
fi

echo "online install test passed"
