#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
SERVER_TOKEN="${SERVER_TOKEN:-}"
NODE_ID="${NODE_ID:-}"
NODE_TYPE="${NODE_TYPE:-vmess}"
LOCAL_PORT="${LOCAL_PORT:-12345}"
TIMEOUT="${TIMEOUT:-8}"
CHECK_LEGACY_COMPAT="${CHECK_LEGACY_COMPAT:-1}"
CURL_BIN="${CURL_BIN:-curl}"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

BASE_URL="${BASE_URL%/}"

usage() {
  cat <<'EOF'
Read-only smoke test for the Go node APIs.

Required env:
  BASE_URL
  SERVER_TOKEN
  NODE_ID
  NODE_TYPE

Optional env:
  LOCAL_PORT=12345
  TIMEOUT=8
  CHECK_LEGACY_COMPAT=1
  CURL_BIN=curl

Example:
  BASE_URL=http://127.0.0.1:8080 \
  SERVER_TOKEN=secret \
  NODE_ID=1 \
  NODE_TYPE=vmess \
  ./scripts/smoke-node-api.sh
EOF
}

fail() {
  local message="$1"
  local body_file="${2:-}"
  printf '[fail] %s\n' "${message}" >&2
  if [[ -n "${body_file}" && -f "${body_file}" ]]; then
    printf '%s\n' '----- response body -----' >&2
    cat "${body_file}" >&2
    printf '%s\n' '-------------------------' >&2
  fi
  exit 1
}

pass() {
  printf '[ok] %s\n' "$1"
}

require_cmd() {
  local cmd="$1"
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    fail "missing required command: ${cmd}"
  fi
}

normalize_node_type() {
  case "$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')" in
    v2ray) printf 'vmess\n' ;;
    hysteria2) printf 'hysteria\n' ;;
    *) printf '%s\n' "$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')" ;;
  esac
}

urlencode() {
  local input="$1"
  local output=""
  local i ch hex

  for ((i = 0; i < ${#input}; i++)); do
    ch="${input:i:1}"
    case "${ch}" in
      [a-zA-Z0-9.~_-])
        output+="${ch}"
        ;;
      *)
        printf -v hex '%%%02X' "'${ch}"
        output+="${hex}"
        ;;
    esac
  done

  printf '%s\n' "${output}"
}

request() {
  local name="$1"
  local method="$2"
  local path="$3"
  local expect_status="$4"
  local expect_pattern="$5"
  local body="${6:-}"
  local body_file="${TMP_DIR}/$(printf '%s' "${name}" | tr -cs 'a-zA-Z0-9._-' '_').body"
  local http_code
  local args=(
    -sS
    --max-time "${TIMEOUT}"
    -X "${method}"
    -o "${body_file}"
    -w "%{http_code}"
  )

  if [[ -n "${body}" ]]; then
    args+=(-H "Content-Type: application/json" --data "${body}")
  fi

  if ! http_code="$("${CURL_BIN}" "${args[@]}" "${BASE_URL}${path}")"; then
    fail "${name}: curl request failed" "${body_file}"
  fi

  if [[ "${http_code}" != "${expect_status}" ]]; then
    fail "${name}: expected HTTP ${expect_status}, got ${http_code}" "${body_file}"
  fi

  if [[ -n "${expect_pattern}" ]] && ! grep -Fq "${expect_pattern}" "${body_file}"; then
    fail "${name}: response does not contain ${expect_pattern}" "${body_file}"
  fi

  pass "${name}"
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

require_cmd "${CURL_BIN}"

if [[ -z "${SERVER_TOKEN}" || -z "${NODE_ID}" || -z "${NODE_TYPE}" ]]; then
  usage
  fail "SERVER_TOKEN, NODE_ID, and NODE_TYPE are required"
fi

NORMALIZED_NODE_TYPE="$(normalize_node_type "${NODE_TYPE}")"
TOKEN_QUERY="$(urlencode "${SERVER_TOKEN}")"
NODE_ID_QUERY="$(urlencode "${NODE_ID}")"
NODE_TYPE_QUERY="$(urlencode "${NODE_TYPE}")"
LOCAL_PORT_QUERY="$(urlencode "${LOCAL_PORT}")"
NODE_QUERY="token=${TOKEN_QUERY}&node_id=${NODE_ID_QUERY}&node_type=${NODE_TYPE_QUERY}"

request "healthz" "GET" "/healthz" "200" '"status":"ok"'
request "readyz" "GET" "/readyz" "200" '"status":"ready"'
request "runtime-meta" "GET" "/api/_meta/runtime" "200" '"admin_path"'
request "queue-monitor" "GET" "/monitor/api/stats" "200" '"status":"running"'

request "uniproxy-user" "GET" "/api/v1/server/UniProxy/user?${NODE_QUERY}" "200" '"users"'
request "uniproxy-config" "GET" "/api/v1/server/UniProxy/config?${NODE_QUERY}" "200" '"base_config"'
request "uniproxy-alivelist" "GET" "/api/v1/server/UniProxy/alivelist?${NODE_QUERY}" "200" '"alive"'

if [[ "${CHECK_LEGACY_COMPAT}" == "1" ]]; then
  case "${NORMALIZED_NODE_TYPE}" in
    vmess)
      request "deepbwork-user" "GET" "/api/v1/server/Deepbwork/user?token=${TOKEN_QUERY}&node_id=${NODE_ID_QUERY}" "200" '"msg":"ok"'
      request "deepbwork-config" "GET" "/api/v1/server/Deepbwork/config?token=${TOKEN_QUERY}&node_id=${NODE_ID_QUERY}&local_port=${LOCAL_PORT_QUERY}" "200" "\"port\":${LOCAL_PORT},\"protocol\":\"dokodemo-door\""
      ;;
    shadowsocks)
      request "shadowsocks-user" "GET" "/api/v1/server/ShadowsocksTidalab/user?token=${TOKEN_QUERY}&node_id=${NODE_ID_QUERY}" "200" '"data"'
      ;;
    trojan)
      request "trojan-user" "GET" "/api/v1/server/TrojanTidalab/user?token=${TOKEN_QUERY}&node_id=${NODE_ID_QUERY}" "200" '"msg":"ok"'
      request "trojan-config" "GET" "/api/v1/server/TrojanTidalab/config?token=${TOKEN_QUERY}&node_id=${NODE_ID_QUERY}&local_port=${LOCAL_PORT_QUERY}" "200" "\"api_port\":${LOCAL_PORT}"
      ;;
    *)
      printf '[skip] no legacy compatibility routes for node type %s\n' "${NORMALIZED_NODE_TYPE}"
      ;;
  esac
fi

pass "node smoke finished"
