#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOAK_BIN="${SOAK_BIN:-${SCRIPT_DIR}/soak-demo-api.sh}"
GO_BASE_URL="${GO_BASE_URL:-}"
LEGACY_BASE_URL="${LEGACY_BASE_URL:-}"
GO_LABEL="${GO_LABEL:-Go版}"
LEGACY_LABEL="${LEGACY_LABEL:-旧版}"
DURATION_SEC="${DURATION_SEC:-15}"
CONCURRENCY="${CONCURRENCY:-8}"
RSS_SAMPLING="${RSS_SAMPLING:-1}"
SOAK_PATHS="${SOAK_PATHS:-}"

if [[ -z "${GO_BASE_URL}" ]]; then
  echo "缺少 GO_BASE_URL" >&2
  exit 1
fi

if [[ -z "${LEGACY_BASE_URL}" ]]; then
  echo "缺少 LEGACY_BASE_URL" >&2
  exit 1
fi

if [[ ! -x "${SOAK_BIN}" ]]; then
  echo "压测脚本不可执行: ${SOAK_BIN}" >&2
  exit 1
fi

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required" >&2
  exit 1
fi

TMP_DIR="$(mktemp -d)"
cleanup() {
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

resolve_prefixed_value() {
  local prefix="$1"
  local name="$2"
  local prefixed="${prefix}_${name}"
  if [[ -n "${!prefixed:-}" ]]; then
    printf '%s' "${!prefixed}"
    return 0
  fi
  printf '%s' "${!name:-}"
}

run_profile() {
  local prefix="$1"
  local label="$2"
  local base_url="$3"
  local summary_json="$4"

  local admin_email admin_password user_email user_password target_pid
  admin_email="$(resolve_prefixed_value "${prefix}" "ADMIN_EMAIL")"
  admin_password="$(resolve_prefixed_value "${prefix}" "ADMIN_PASSWORD")"
  user_email="$(resolve_prefixed_value "${prefix}" "USER_EMAIL")"
  user_password="$(resolve_prefixed_value "${prefix}" "USER_PASSWORD")"
  target_pid="$(resolve_prefixed_value "${prefix}" "TARGET_PID")"

  local output
  output="$(
    BASE_URL="${base_url}" \
    ADMIN_EMAIL="${admin_email}" \
    ADMIN_PASSWORD="${admin_password}" \
    USER_EMAIL="${user_email}" \
    USER_PASSWORD="${user_password}" \
    DURATION_SEC="${DURATION_SEC}" \
    CONCURRENCY="${CONCURRENCY}" \
    RSS_SAMPLING="${RSS_SAMPLING}" \
    SOAK_PATHS="${SOAK_PATHS}" \
    TARGET_PID="${target_pid}" \
    SUMMARY_JSON="${summary_json}" \
    "${SOAK_BIN}"
  )"
  printf '%s\n' "${output}" >&2
  printf '%s\n' "${label} 压测完成" >&2
}

GO_SUMMARY="${TMP_DIR}/go.json"
LEGACY_SUMMARY="${TMP_DIR}/legacy.json"

run_profile "GO" "${GO_LABEL}" "${GO_BASE_URL}" "${GO_SUMMARY}"
run_profile "LEGACY" "${LEGACY_LABEL}" "${LEGACY_BASE_URL}" "${LEGACY_SUMMARY}"

python3 - "${GO_SUMMARY}" "${LEGACY_SUMMARY}" "${GO_LABEL}" "${LEGACY_LABEL}" <<'PY'
import json
import sys

go_summary_path, legacy_summary_path, go_label, legacy_label = sys.argv[1:]

with open(go_summary_path, "r", encoding="utf-8") as handle:
    go_data = json.load(handle)
with open(legacy_summary_path, "r", encoding="utf-8") as handle:
    legacy_data = json.load(handle)


def fnum(payload, key):
    return float(payload.get(key, 0) or 0)


def ior_none(payload, key):
    value = payload.get(key)
    if value is None:
        return "-"
    return str(value)


def safe_ratio(new_value, old_value):
    if old_value <= 0:
        return None
    return new_value / old_value


def safe_reduction(old_value, new_value):
    if old_value <= 0:
        return None
    return (old_value - new_value) / old_value * 100.0


go_requests = fnum(go_data, "requests")
legacy_requests = fnum(legacy_data, "requests")
go_p50 = fnum(go_data, "p50_ms")
legacy_p50 = fnum(legacy_data, "p50_ms")
go_p95 = fnum(go_data, "p95_ms")
legacy_p95 = fnum(legacy_data, "p95_ms")
go_rss = fnum(go_data, "rss_kb_delta")
legacy_rss = fnum(legacy_data, "rss_kb_delta")

print(f"{go_label} requests={int(go_requests)} p50_ms={go_p50:.1f} p95_ms={go_p95:.1f} rss_kb_delta={ior_none(go_data, 'rss_kb_delta')}")
print(f"{legacy_label} requests={int(legacy_requests)} p50_ms={legacy_p50:.1f} p95_ms={legacy_p95:.1f} rss_kb_delta={ior_none(legacy_data, 'rss_kb_delta')}")

throughput_ratio = safe_ratio(go_requests, legacy_requests)
if throughput_ratio is None:
    print("吞吐提升: 无法计算")
else:
    print(f"吞吐提升: {throughput_ratio:.2f}x")

p95_reduction = safe_reduction(legacy_p95, go_p95)
if p95_reduction is None:
    print("P95下降: 无法计算")
else:
    print(f"P95下降: {p95_reduction:.1f}%")

rss_reduction = safe_reduction(legacy_rss, go_rss)
if rss_reduction is None:
    print("RSS增量下降: 无法计算")
else:
    print(f"RSS增量下降: {rss_reduction:.1f}%")
PY
