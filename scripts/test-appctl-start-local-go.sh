#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api" "${TMP_DIR}/.local/go/bin"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/.env.go" <<'EOF'
DB_HOST=127.0.0.1
DB_PORT=5432
DB_DATABASE=forest
DB_USERNAME=startuser
DB_PASSWORD=startpass
EOF

cat > "${TMP_DIR}/.local/go/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GO_LOG}"
if [[ "${1:-}" == "build" && "${2:-}" == "-o" ]]; then
  out="${3}"
  mkdir -p "$(dirname "${out}")"
  cat > "${out}" <<'APP'
#!/usr/bin/env bash
trap 'exit 0' TERM INT
while true; do
  sleep 1
done
APP
  chmod +x "${out}"
fi
exit 0
EOF
chmod +x "${TMP_DIR}/.local/go/bin/go"

GO_LOG="${TMP_DIR}/go.log"
export GO_LOG

PATH="/usr/bin:/bin" "${TMP_DIR}/scripts/appctl" start >/tmp/test-appctl-start-local-go.out 2>/tmp/test-appctl-start-local-go.err

EXPECTED=$'run ./cmd/ops migrate-config --target-root ..\nmod tidy\nbuild -o '"${TMP_DIR}"'/go-api/bin/forest ./cmd/server'
ACTUAL="$(cat "${GO_LOG}")"
if [[ "${ACTUAL}" != "${EXPECTED}" ]]; then
  echo "expected start to use cached local go binary for migrate-config and build"
  printf 'expected:\n%s\nactual:\n%s\n' "${EXPECTED}" "${ACTUAL}"
  exit 1
fi

if ! rg -n '已启动，PID ' /tmp/test-appctl-start-local-go.out >/dev/null 2>&1; then
  echo "expected chinese start success message"
  cat /tmp/test-appctl-start-local-go.out
  exit 1
fi

if [[ ! -f "${TMP_DIR}/go-api/run/forest.pid" ]]; then
  echo "expected pid file to be created"
  exit 1
fi

PATH="/usr/bin:/bin" "${TMP_DIR}/scripts/appctl" stop >/tmp/test-appctl-start-local-go-stop.out 2>/tmp/test-appctl-start-local-go-stop.err

echo "start local go test passed"
