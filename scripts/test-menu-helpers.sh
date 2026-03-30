#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api/run"
cp "${REPO_ROOT}/menu.sh" "${TMP_DIR}/menu.sh"
chmod +x "${TMP_DIR}/menu.sh"
touch "${TMP_DIR}/go-api/run/forest-go-api.log"

cat > "${TMP_DIR}/scripts/appctl" <<'APPCTL'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "env-file" ]]; then
  printf '%s\n' "$*" >> "${APPCTL_LOG}"
  echo "/tmp/fake.env.go"
  exit 0
fi
printf '%s\n' "$*" >> "${APPCTL_LOG}"
if [[ "${1:-}" == "status" ]]; then
  echo "运行中，PID 12345"
fi
APPCTL
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/fake-tail" <<'TAIL'
#!/usr/bin/env bash
set -euo pipefail
printf 'tail %s\n' "$*" >> "${HELPER_LOG}"
TAIL
chmod +x "${TMP_DIR}/fake-tail"

cat > "${TMP_DIR}/fake-ps" <<'PS'
#!/usr/bin/env bash
set -euo pipefail
printf 'ps %s\n' "$*" >> "${HELPER_LOG}"
PS
chmod +x "${TMP_DIR}/fake-ps"

cat > "${TMP_DIR}/fake-ss" <<'SS'
#!/usr/bin/env bash
set -euo pipefail
printf 'ss %s\n' "$*" >> "${HELPER_LOG}"
SS
chmod +x "${TMP_DIR}/fake-ss"

APPCTL_LOG="${TMP_DIR}/appctl.log"
HELPER_LOG="${TMP_DIR}/helper.log"
export APPCTL_LOG HELPER_LOG

printf '3\n1\n\n0\n0\n' | APPCTL_BIN="${TMP_DIR}/scripts/appctl" TAIL_BIN="${TMP_DIR}/fake-tail" PS_BIN="${TMP_DIR}/fake-ps" SS_BIN="${TMP_DIR}/fake-ss" bash "${TMP_DIR}/menu.sh" >/tmp/test-menu-helper-doctor.out 2>/tmp/test-menu-helper-doctor.err
if [[ "$(grep -v '^env-file$' "${APPCTL_LOG}")" != "doctor" ]]; then
  echo "expected doctor action"
  cat "${APPCTL_LOG}"
  exit 1
fi

: > "${APPCTL_LOG}"
: > "${HELPER_LOG}"
printf '3\n2\n\n0\n0\n' | APPCTL_BIN="${TMP_DIR}/scripts/appctl" TAIL_BIN="${TMP_DIR}/fake-tail" PS_BIN="${TMP_DIR}/fake-ps" SS_BIN="${TMP_DIR}/fake-ss" LOG_PATH="${TMP_DIR}/go-api/run/forest-go-api.log" bash "${TMP_DIR}/menu.sh" >/tmp/test-menu-helper-log.out 2>/tmp/test-menu-helper-log.err
if [[ "$(cat "${HELPER_LOG}")" != "tail -n 200 ${TMP_DIR}/go-api/run/forest-go-api.log" ]]; then
  echo "expected tail helper"
  cat "${HELPER_LOG}"
  exit 1
fi

: > "${APPCTL_LOG}"
: > "${HELPER_LOG}"
printf '4\n6\n\n0\n0\n' | APPCTL_BIN="${TMP_DIR}/scripts/appctl" TAIL_BIN="${TMP_DIR}/fake-tail" PS_BIN="${TMP_DIR}/fake-ps" SS_BIN="${TMP_DIR}/fake-ss" bash "${TMP_DIR}/menu.sh" >/tmp/test-menu-helper-env-file.out 2>/tmp/test-menu-helper-env-file.err
if ! grep -q '^env-file$' "${APPCTL_LOG}"; then
  echo "expected env-file action"
  cat "${APPCTL_LOG}"
  exit 1
fi

: > "${APPCTL_LOG}"
: > "${HELPER_LOG}"
printf '3\n5\n\n0\n0\n' | APPCTL_BIN="${TMP_DIR}/scripts/appctl" TAIL_BIN="${TMP_DIR}/fake-tail" PS_BIN="${TMP_DIR}/fake-ps" SS_BIN="${TMP_DIR}/fake-ss" PID_PATH="${TMP_DIR}/go-api/run/forest-go-api.pid" bash "${TMP_DIR}/menu.sh" >/tmp/test-menu-helper-ps.out 2>/tmp/test-menu-helper-ps.err
if [[ "$(grep -v '^env-file$' "${APPCTL_LOG}")" != "status" ]]; then
  echo "expected status action before ps"
  cat "${APPCTL_LOG}"
  exit 1
fi
if [[ "$(cat "${HELPER_LOG}")" != "ps -fp 12345" ]]; then
  echo "expected ps helper"
  cat "${HELPER_LOG}"
  exit 1
fi

: > "${APPCTL_LOG}"
: > "${HELPER_LOG}"
printf '4\n8\n\n0\n0\n' | APPCTL_BIN="${TMP_DIR}/scripts/appctl" TAIL_BIN="${TMP_DIR}/fake-tail" PS_BIN="${TMP_DIR}/fake-ps" SS_BIN="${TMP_DIR}/fake-ss" bash "${TMP_DIR}/menu.sh" >/tmp/test-menu-helper-install-link.out 2>/tmp/test-menu-helper-install-link.err
if [[ "$(grep -v '^env-file$' "${APPCTL_LOG}")" != "install-link" ]]; then
  echo "expected install-link action"
  cat "${APPCTL_LOG}"
  exit 1
fi

echo "menu helpers test passed"
