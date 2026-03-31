#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/.env.go" <<'EOF'
DB_HOST=127.0.0.1
DB_PORT=5432
DB_DATABASE=forest
DB_USERNAME=gitpulluser
DB_PASSWORD=gitpullpass
EOF

cat > "${TMP_DIR}/fake-go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GO_LOG}"
exit 0
EOF
chmod +x "${TMP_DIR}/fake-go"

cat > "${TMP_DIR}/fake-git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GIT_LOG}"

if [[ "${1:-}" == "-C" ]]; then
  shift 2
fi

if [[ "${1:-}" == "rev-parse" && "${2:-}" == "--is-inside-work-tree" ]]; then
  echo "true"
  exit 0
fi

if [[ "${1:-}" == "rev-parse" && "${2:-}" == "--short=7" && "${3:-}" == "HEAD" ]]; then
  if [[ ! -f "${GIT_STATE_FILE}" ]]; then
    echo "1111111"
  else
    echo "2222222"
  fi
  exit 0
fi

if [[ "${1:-}" == "pull" && "${2:-}" == "--ff-only" && "${3:-}" == "--quiet" ]]; then
  : > "${GIT_STATE_FILE}"
  exit 0
fi

exit 1
EOF
chmod +x "${TMP_DIR}/fake-git"

GO_LOG="${TMP_DIR}/go.log"
GIT_LOG="${TMP_DIR}/git.log"
GIT_STATE_FILE="${TMP_DIR}/git-state"
export GO_LOG GIT_LOG GIT_STATE_FILE

GO_BIN="${TMP_DIR}/fake-go" GIT_BIN="${TMP_DIR}/fake-git" "${TMP_DIR}/scripts/appctl" update >/tmp/test-appctl-update-pulls.out 2>/tmp/test-appctl-update-pulls.err

EXPECTED_GIT="$(printf '%s\n' \
  "-C ${TMP_DIR} rev-parse --is-inside-work-tree" \
  "-C ${TMP_DIR} rev-parse --short=7 HEAD" \
  "-C ${TMP_DIR} pull --ff-only --quiet" \
  "-C ${TMP_DIR} rev-parse --short=7 HEAD")"
ACTUAL_GIT="$(cat "${GIT_LOG}" 2>/dev/null || true)"
if [[ "${ACTUAL_GIT}" != "${EXPECTED_GIT}" ]]; then
  echo "unexpected git update command order"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED_GIT}" "${ACTUAL_GIT}"
  exit 1
fi

EXPECTED_GO=$'run ./cmd/ops migrate-config --target-root ..\nrun ./cmd/ops update --sql ../database/update.pgsql.sql --dsn host=127.0.0.1 port=5432 user=gitpulluser dbname=forest sslmode=disable password=gitpullpass\nmod tidy\nbuild -o '"${TMP_DIR}"'/go-api/bin/forest ./cmd/server'
ACTUAL_GO="$(cat "${GO_LOG}")"
if [[ "${ACTUAL_GO}" != "${EXPECTED_GO}" ]]; then
  echo "unexpected go update command order"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED_GO}" "${ACTUAL_GO}"
  exit 1
fi

if ! rg -n '代码已更新 1111111 -> 2222222' /tmp/test-appctl-update-pulls.out >/dev/null 2>&1; then
  echo "expected git pull status message"
  cat /tmp/test-appctl-update-pulls.out
  exit 1
fi

echo "appctl update git pull test passed"
