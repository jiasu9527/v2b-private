#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api" "${TMP_DIR}/.local/go/bin"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/.env.go" <<'ENVGO'
DB_HOST=127.0.0.1
DB_PORT=5432
DB_DATABASE=forest
DB_USERNAME=mergeuser
DB_PASSWORD=mergepass
ENVGO

cat > "${TMP_DIR}/.env" <<'LEGACYENV'
DB_CONNECTION=mysql
DB_HOST=legacy.mysql.local
DB_PORT=3307
DB_DATABASE=legacydb
DB_USERNAME=legacyuser
DB_PASSWORD=legacypass
DB_CHARSET=utf8mb4
LEGACYENV

cat > "${TMP_DIR}/.local/go/bin/go" <<'GOEOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GO_LOG}"
if [[ "$*" == *"inspect-merge-mysql"* ]]; then
  cat <<'OUT'
source_users_total=7
source_users_without_plan=1
source_plan	1	旧月付	3
source_plan	2	旧年付	2
target_plan	10	新月付	5
target_plan	20	新年付	8
OUT
  exit 0
fi
if [[ "$*" == *"merge-mysql"* ]]; then
  echo 'merge_status=applied'
  echo 'users_inserted=5'
  exit 0
fi
exit 0
GOEOF
chmod +x "${TMP_DIR}/.local/go/bin/go"

GO_LOG="${TMP_DIR}/go.log"
export GO_LOG

INPUT=$'\n\n\n\n\n\n10\n新年付\ny\n'
printf '%s' "${INPUT}" | PATH="/usr/bin:/bin" "${TMP_DIR}/scripts/appctl" merge-mysql >/tmp/test-appctl-merge-mysql.out 2>/tmp/test-appctl-merge-mysql.err

EXPECTED_INSPECT='run ./cmd/ops inspect-merge-mysql --source-host legacy.mysql.local --source-port 3307 --source-database legacydb --source-username legacyuser --source-password legacypass --source-charset utf8mb4 --target-dsn host=127.0.0.1 port=5432 user=mergeuser dbname=forest sslmode=disable password=mergepass'
EXPECTED_MERGE='run ./cmd/ops merge-mysql --source-host legacy.mysql.local --source-port 3307 --source-database legacydb --source-username legacyuser --source-password legacypass --source-charset utf8mb4 --plan-map 1:10,2:20 --target-dsn host=127.0.0.1 port=5432 user=mergeuser dbname=forest sslmode=disable password=mergepass'

if ! grep -Fx "${EXPECTED_INSPECT}" "${GO_LOG}" >/dev/null 2>&1; then
  echo "expected inspect command"
  cat "${GO_LOG}"
  exit 1
fi

if ! grep -Fx "${EXPECTED_MERGE}" "${GO_LOG}" >/dev/null 2>&1; then
  echo "expected merge command with plan map"
  cat "${GO_LOG}"
  exit 1
fi

if ! rg -n 'merge_status=applied' /tmp/test-appctl-merge-mysql.out >/dev/null 2>&1; then
  echo "expected merge output"
  cat /tmp/test-appctl-merge-mysql.out
  exit 1
fi

echo "appctl merge-mysql test passed"
