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

cat > "${TMP_DIR}/merge-mysql.env" <<'PROFILE'
MERGE_MYSQL_SOURCE_HOST=31.22.111.209
MERGE_MYSQL_SOURCE_PORT=3306
MERGE_MYSQL_SOURCE_DATABASE=v2board
MERGE_MYSQL_SOURCE_USERNAME=v2user
MERGE_MYSQL_SOURCE_PASSWORD=V2UserPass123456!
MERGE_MYSQL_SOURCE_CHARSET=utf8mb4
MERGE_MYSQL_PLAN_MAP=11:1,12:1,13:2,14:1,15:1,16:2,19:1,20:3,23:3,29:1,31:3,32:1,33:1
PROFILE

cat > "${TMP_DIR}/.local/go/bin/go" <<'GOEOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GO_LOG}"
if [[ "$*" == *"inspect-merge-mysql"* ]]; then
  cat <<'OUT'
source_users_total=7
source_users_without_plan=1
source_plan	11	旧轻量	3
source_plan	20	旧旗舰	2
source_plan	23	旧旗舰加速	1
source_plan	33	旧不限时	1
target_plan	1	轻量套餐	5
target_plan	2	高级套餐	8
target_plan	3	旗舰套餐	3
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

INPUT=$'y\n'
FOREST_MERGE_MYSQL_PROFILE="${TMP_DIR}/merge-mysql.env" \
  PATH="/usr/bin:/bin" \
  "${TMP_DIR}/scripts/appctl" merge-mysql >/tmp/test-appctl-merge-mysql-profile.out 2>/tmp/test-appctl-merge-mysql-profile.err <<< "${INPUT}"

EXPECTED_INSPECT='run ./cmd/ops inspect-merge-mysql --source-host 31.22.111.209 --source-port 3306 --source-database v2board --source-username v2user --source-password V2UserPass123456! --source-charset utf8mb4 --target-dsn host=127.0.0.1 port=5432 user=mergeuser dbname=forest sslmode=disable password=mergepass'
EXPECTED_MERGE='run ./cmd/ops merge-mysql --source-host 31.22.111.209 --source-port 3306 --source-database v2board --source-username v2user --source-password V2UserPass123456! --source-charset utf8mb4 --plan-map 11:1,20:3,23:3,33:1 --target-dsn host=127.0.0.1 port=5432 user=mergeuser dbname=forest sslmode=disable password=mergepass'

if ! grep -Fx "${EXPECTED_INSPECT}" "${GO_LOG}" >/dev/null 2>&1; then
  echo "expected inspect command from merge profile"
  cat "${GO_LOG}"
  exit 1
fi

if ! grep -Fx "${EXPECTED_MERGE}" "${GO_LOG}" >/dev/null 2>&1; then
  echo "expected merge command with saved plan map"
  cat "${GO_LOG}"
  exit 1
fi

if ! rg -n '已加载 MySQL 合并预设' /tmp/test-appctl-merge-mysql-profile.out >/dev/null 2>&1; then
  echo "expected merge profile notice"
  cat /tmp/test-appctl-merge-mysql-profile.out
  exit 1
fi

if ! rg -n 'merge_status=applied' /tmp/test-appctl-merge-mysql-profile.out >/dev/null 2>&1; then
  echo "expected merge output"
  cat /tmp/test-appctl-merge-mysql-profile.out
  exit 1
fi

echo "appctl merge-mysql profile test passed"
