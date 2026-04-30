#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api" "${TMP_DIR}/.local/go/bin"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"
chmod +x "${TMP_DIR}/scripts/appctl"

cat > "${TMP_DIR}/merge-mysql.env" <<'PROFILE'
MERGE_MYSQL_SOURCE_HOST=47.236.105.189
MERGE_MYSQL_SOURCE_PORT=33306
MERGE_MYSQL_SOURCE_DATABASE=xier
MERGE_MYSQL_SOURCE_USERNAME=xier
MERGE_MYSQL_SOURCE_PASSWORD=legacy-pass
MERGE_MYSQL_SOURCE_CHARSET=utf8mb4
MERGE_MYSQL_TARGET_DSN=host=cloud.pg.internal port=5432 user=pg_user dbname=forest sslmode=disable password=pg_pass
MERGE_MYSQL_PLAN_MAP=2:13,3:1,6:5,36:13
PROFILE

cat > "${TMP_DIR}/.local/go/bin/go" <<'GOEOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GO_LOG}"
if [[ "$*" == *"inspect-merge-mysql"* ]]; then
  cat <<'OUT'
source_users_total=10452
source_users_without_plan=5582
source_plan	2	夜行	598
source_plan	3	割裂	567
source_plan	6	归刃	347
source_plan	36	免费套餐	2918
target_plan	1	轻量	25542
target_plan	5	不限时200G	491
target_plan	13	50G	2
OUT
  exit 0
fi
if [[ "$*" == *"merge-mysql"* ]]; then
  echo 'merge_status=applied'
  echo 'users_inserted=10133'
  exit 0
fi
exit 0
GOEOF
chmod +x "${TMP_DIR}/.local/go/bin/go"

GO_LOG="${TMP_DIR}/go.log"
export GO_LOG

FOREST_MERGE_MYSQL_PROFILE="${TMP_DIR}/merge-mysql.env" \
  PATH="/usr/bin:/bin" \
  "${TMP_DIR}/scripts/appctl" merge-mysql-apply >/tmp/test-appctl-merge-mysql-apply.out 2>/tmp/test-appctl-merge-mysql-apply.err

EXPECTED_INSPECT='run ./cmd/ops inspect-merge-mysql --source-host 47.236.105.189 --source-port 33306 --source-database xier --source-username xier --source-password legacy-pass --source-charset utf8mb4 --target-dsn host=cloud.pg.internal port=5432 user=pg_user dbname=forest sslmode=disable password=pg_pass'
EXPECTED_MERGE='run ./cmd/ops merge-mysql --source-host 47.236.105.189 --source-port 33306 --source-database xier --source-username xier --source-password legacy-pass --source-charset utf8mb4 --plan-map 2:13,3:1,6:5,36:13 --target-dsn host=cloud.pg.internal port=5432 user=pg_user dbname=forest sslmode=disable password=pg_pass'

if ! grep -Fx "${EXPECTED_INSPECT}" "${GO_LOG}" >/dev/null 2>&1; then
  echo "expected inspect command in apply mode"
  cat "${GO_LOG}"
  exit 1
fi

if ! grep -Fx "${EXPECTED_MERGE}" "${GO_LOG}" >/dev/null 2>&1; then
  echo "expected merge command in apply mode"
  cat "${GO_LOG}"
  exit 1
fi

if ! rg -n '使用预设直接执行 MySQL 合并' /tmp/test-appctl-merge-mysql-apply.out >/dev/null 2>&1; then
  echo "expected non-interactive apply notice"
  cat /tmp/test-appctl-merge-mysql-apply.out
  exit 1
fi

if ! rg -n 'merge_status=applied' /tmp/test-appctl-merge-mysql-apply.out >/dev/null 2>&1; then
  echo "expected merge output in apply mode"
  cat /tmp/test-appctl-merge-mysql-apply.out
  exit 1
fi

echo "appctl merge-mysql apply test passed"
