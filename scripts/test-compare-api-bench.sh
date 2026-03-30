#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

cp "${REPO_ROOT}/scripts/compare-api-bench.sh" "${TMP_DIR}/compare-api-bench.sh" 2>/dev/null || true

cat > "${TMP_DIR}/fake-soak.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${SUMMARY_JSON:-}" ]]; then
  echo "SUMMARY_JSON is required" >&2
  exit 1
fi

case "${BASE_URL:-}" in
  http://go.example.com)
    cat > "${SUMMARY_JSON}" <<'JSON'
{"requests":3000,"duration_sec":15,"concurrency":8,"p50_ms":12.0,"p95_ms":40.0,"rss_kb_delta":2048}
JSON
    ;;
  http://legacy.example.com)
    cat > "${SUMMARY_JSON}" <<'JSON'
{"requests":1200,"duration_sec":15,"concurrency":8,"p50_ms":40.0,"p95_ms":120.0,"rss_kb_delta":8192}
JSON
    ;;
  *)
    echo "unexpected BASE_URL=${BASE_URL:-}" >&2
    exit 1
    ;;
esac

echo "[ok] fake soak ${BASE_URL}"
EOF
chmod +x "${TMP_DIR}/fake-soak.sh"

OUTPUT="$(
  GO_BASE_URL="http://go.example.com" \
  LEGACY_BASE_URL="http://legacy.example.com" \
  SOAK_BIN="${TMP_DIR}/fake-soak.sh" \
  bash "${REPO_ROOT}/scripts/compare-api-bench.sh"
)"

if [[ "${OUTPUT}" != *"Go版 requests=3000 p50_ms=12.0 p95_ms=40.0 rss_kb_delta=2048"* ]]; then
  echo "expected go summary line"
  echo "${OUTPUT}"
  exit 1
fi

if [[ "${OUTPUT}" != *"旧版 requests=1200 p50_ms=40.0 p95_ms=120.0 rss_kb_delta=8192"* ]]; then
  echo "expected legacy summary line"
  echo "${OUTPUT}"
  exit 1
fi

if [[ "${OUTPUT}" != *"吞吐提升: 2.50x"* ]]; then
  echo "expected throughput improvement"
  echo "${OUTPUT}"
  exit 1
fi

if [[ "${OUTPUT}" != *"P95下降: 66.7%"* ]]; then
  echo "expected p95 reduction"
  echo "${OUTPUT}"
  exit 1
fi

if [[ "${OUTPUT}" != *"RSS增量下降: 75.0%"* ]]; then
  echo "expected rss reduction"
  echo "${OUTPUT}"
  exit 1
fi

echo "compare-api-bench test passed"
