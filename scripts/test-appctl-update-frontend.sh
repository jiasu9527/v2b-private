#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TASK_TMP="$(mktemp -d)"
trap 'rm -rf "${TASK_TMP}"' EXIT
mkdir -p "${TASK_TMP}/scripts" "${TASK_TMP}/go-api" "${TASK_TMP}/admin-src" "${TASK_TMP}/public/assets/admin-new" "${TASK_TMP}/fakebin"
cp "${REPO_ROOT}/scripts/appctl" "${TASK_TMP}/scripts/appctl"
chmod +x "${TASK_TMP}/scripts/appctl"
printf '{"name":"fixture"}\n' > "${TASK_TMP}/admin-src/package.json"
printf '{"lockfileVersion":3}\n' > "${TASK_TMP}/admin-src/package-lock.json"
printf 'bundled old' > "${TASK_TMP}/public/assets/admin-new/admin.js"
printf 'bundled old css' > "${TASK_TMP}/public/assets/admin-new/index.css"
printf 'module fixture\n' > "${TASK_TMP}/go-api/go.mod"
printf 'checksum fixture\n' > "${TASK_TMP}/go-api/go.sum"
cat > "${TASK_TMP}/.env.go" <<'EOF'
DB_HOST=127.0.0.1
DB_PORT=5432
DB_DATABASE=forest
DB_USERNAME=test
DB_PASSWORD=fixture
ADMIN_EMAIL=admin@example.test
APP_KEY=fixture-app-key
EOF
cat > "${TASK_TMP}/fakebin/npm" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${NPM_LOG}"
if [[ "$*" == "run build -- --outDir "* ]]; then
  if [[ "${FAIL_NPM_BUILD:-0}" == "1" ]]; then
    exit 42
  fi
  output="${*: -1}"
  mkdir -p "${output}"
  printf 'compiled release %s' "${RUN_MARKER}" > "${output}/admin.js"
  printf 'compiled css %s' "${RUN_MARKER}" > "${output}/index.css"
  printf '<!doctype html>' > "${output}/index.html"
fi
EOF
chmod +x "${TASK_TMP}/fakebin/npm"
cat > "${TASK_TMP}/fake-go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GO_LOG}"
if [[ "${1:-}" == "build" && "${2:-}" == "-o" ]]; then
  mkdir -p "$(dirname "$3")"
  printf 'fixture binary' > "$3"
fi
EOF
chmod +x "${TASK_TMP}/fake-go"
cat > "${TASK_TMP}/fake-systemctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${SYSTEMCTL_LOG}"
if [[ "${FAIL_RESTART:-0}" == "1" && "$*" == "restart forest" ]]; then
  exit 42
fi
case "$*" in
  "is-active --quiet forest") exit 0 ;;
  "show -p MainPID --value forest") echo 1234 ;;
esac
EOF
chmod +x "${TASK_TMP}/fake-systemctl"
printf '[Unit]\nDescription=forest\n' > "${TASK_TMP}/forest.service"

export NPM_LOG="${TASK_TMP}/npm.log" GO_LOG="${TASK_TMP}/go.log" SYSTEMCTL_LOG="${TASK_TMP}/systemctl.log"
for marker in first second; do
  export RUN_MARKER="${marker}"
  GO_BIN="${TASK_TMP}/fake-go" SYSTEMCTL_BIN="${TASK_TMP}/fake-systemctl" FOREST_SERVICE_FILE_PATH="${TASK_TMP}/forest.service" \
    PATH="${TASK_TMP}/fakebin:/usr/bin:/bin" "${TASK_TMP}/scripts/appctl" update > "${TASK_TMP}/${marker}.out"
  current="${TASK_TMP}/.local/admin-new/current"
  [[ -L "${current}" ]]
  [[ "$(cat "${current}/admin.js")" == "compiled release ${marker}" ]]
  [[ "$(cat "${current}/index.css")" == "compiled css ${marker}" ]]
  [[ "$(cat "${TASK_TMP}/public/assets/admin-new/admin.js")" == "bundled old" ]]
done

RUN_MARKER=install GO_BIN="${TASK_TMP}/fake-go" SYSTEMCTL_BIN="${TASK_TMP}/fake-systemctl" FOREST_SERVICE_FILE_PATH="${TASK_TMP}/forest.service" \
  PATH="${TASK_TMP}/fakebin:/usr/bin:/bin" "${TASK_TMP}/scripts/appctl" install > "${TASK_TMP}/install.out"
[[ "$(cat "${TASK_TMP}/.local/admin-new/current/admin.js")" == 'compiled release install' ]]

before="$(readlink "${TASK_TMP}/.local/admin-new/current")"
restarts_before="$(grep -c '^restart forest$' "${SYSTEMCTL_LOG}")"
if FAIL_NPM_BUILD=1 RUN_MARKER=broken GO_BIN="${TASK_TMP}/fake-go" SYSTEMCTL_BIN="${TASK_TMP}/fake-systemctl" FOREST_SERVICE_FILE_PATH="${TASK_TMP}/forest.service" \
  PATH="${TASK_TMP}/fakebin:/usr/bin:/bin" "${TASK_TMP}/scripts/appctl" update > "${TASK_TMP}/broken.out" 2>&1; then
  echo 'frontend build failure unexpectedly succeeded' >&2
  exit 1
fi
[[ "$(readlink "${TASK_TMP}/.local/admin-new/current")" == "${before}" ]]
[[ "$(grep -c '^restart forest$' "${SYSTEMCTL_LOG}")" == "${restarts_before}" ]]
[[ "$(cat "${TASK_TMP}/public/assets/admin-new/admin.js")" == "bundled old" ]]

# A restart error must not leave the just-built admin release selected.
if FAIL_RESTART=1 RUN_MARKER=restart-broken GO_BIN="${TASK_TMP}/fake-go" SYSTEMCTL_BIN="${TASK_TMP}/fake-systemctl" FOREST_SERVICE_FILE_PATH="${TASK_TMP}/forest.service" \
  PATH="${TASK_TMP}/fakebin:/usr/bin:/bin" "${TASK_TMP}/scripts/appctl" update > "${TASK_TMP}/restart-broken.out" 2>&1; then
  echo 'service restart failure unexpectedly succeeded' >&2
  exit 1
fi
[[ "$(readlink "${TASK_TMP}/.local/admin-new/current")" == "${before}" ]]
[[ "$(cat "${TASK_TMP}/go-api/go.mod")" == 'module fixture' ]]
[[ "$(cat "${TASK_TMP}/go-api/go.sum")" == 'checksum fixture' ]]
! grep -q '^mod tidy$' "${GO_LOG}"

# Simulate a git pull replacing the running script. Direct appctl update must
# execute the new file rather than finishing with the old in-memory functions.
cat > "${TASK_TMP}/fakebin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GIT_LOG}"
case "$*" in
  *'rev-parse --is-inside-work-tree'*) echo true ;;
  *'rev-parse --short=7 HEAD'*) echo abcdef0 ;;
  *'pull --ff-only --quiet'*)
    printf '\necho reloaded > "${RELOAD_FILE}"\n' >> "${APPCTL_PATH}"
    ;;
esac
EOF
chmod +x "${TASK_TMP}/fakebin/git"
export GIT_LOG="${TASK_TMP}/git.log" APPCTL_PATH="${TASK_TMP}/scripts/appctl" RELOAD_FILE="${TASK_TMP}/reloaded"
RUN_MARKER=reloaded GO_BIN="${TASK_TMP}/fake-go" GIT_BIN="${TASK_TMP}/fakebin/git" SYSTEMCTL_BIN="${TASK_TMP}/fake-systemctl" FOREST_SERVICE_FILE_PATH="${TASK_TMP}/forest.service" \
  PATH="${TASK_TMP}/fakebin:/usr/bin:/bin" "${TASK_TMP}/scripts/appctl" update > "${TASK_TMP}/reloaded.out"
[[ -f "${RELOAD_FILE}" ]]
[[ "$(grep -c 'pull --ff-only --quiet' "${GIT_LOG}")" == 1 ]]

echo 'appctl staged admin update test passed'
