#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
SERVER_PID=""

cleanup() {
  if [[ -n "${SERVER_PID}" ]] && kill -0 "${SERVER_PID}" 2>/dev/null; then
    kill "${SERVER_PID}" 2>/dev/null || true
    wait "${SERVER_PID}" 2>/dev/null || true
  fi
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

mkdir -p "${TMP_DIR}/scripts"
cp "${REPO_ROOT}/scripts/soak-demo-api.sh" "${TMP_DIR}/scripts/soak-demo-api.sh"
chmod +x "${TMP_DIR}/scripts/soak-demo-api.sh"

cat > "${TMP_DIR}/stub_server.py" <<'PY'
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

port_file = sys.argv[1]
ADMIN_AUTH = "admin-auth-token"
USER_AUTH = "user-auth-token"


class Handler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        return

    def _read_json(self):
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length).decode("utf-8")
        if not raw:
            return {}
        return json.loads(raw)

    def _write(self, status, payload):
        body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        if self.path != "/api/v1/passport/auth/login":
            self._write(404, {"message": "not found"})
            return
        payload = self._read_json()
        if payload.get("email") == "admin@example.com" and payload.get("password") == "Admin123456":
            self._write(200, {"data": {"auth_data": ADMIN_AUTH}})
            return
        if payload.get("email") == "seed-demo-owner@example.com" and payload.get("password") == "Seed123456":
            self._write(200, {"data": {"auth_data": USER_AUTH}})
            return
        self._write(403, {"message": "invalid credentials"})

    def do_GET(self):
        if self.path == "/healthz":
            self._write(200, {"status": "ok"})
            return
        if self.path == "/custom/health":
            self._write(200, {"status": "ok", "path": "custom"})
            return
        if self.path == "/custom/slow":
            time.sleep(0.05)
            self._write(200, {"status": "ok", "path": "slow"})
            return
        if self.path == "/monitor/api/stats":
            self._write(200, {"status": "running", "current_jobs": 0, "workers": 4})
            return
        if self.path == f"/api/v1/localadmin/config/fetch?auth_data={ADMIN_AUTH}":
            self._write(200, {"data": {"secure_path": "localadmin"}})
            return
        if self.path == f"/api/v1/localadmin/system/getQueueWorkload?auth_data={ADMIN_AUTH}":
            self._write(200, {"data": [{"name": "send_email", "processes": 0, "length": 0, "wait": 0}]})
            return
        if self.path == f"/api/v1/user/checkLogin?auth_data={USER_AUTH}":
            self._write(200, {"data": {"is_login": True}})
            return
        if self.path == f"/api/v1/user/order/fetch?auth_data={USER_AUTH}":
            self._write(200, {"data": [{"trade_no": "seed-demo-order-pending-01"}]})
            return
        self._write(404, {"message": "not found", "path": self.path})


server = HTTPServer(("127.0.0.1", 0), Handler)
with open(port_file, "w", encoding="utf-8") as handle:
    handle.write(str(server.server_address[1]))
server.serve_forever()
PY

python3 "${TMP_DIR}/stub_server.py" "${TMP_DIR}/port" &
SERVER_PID=$!

for _ in $(seq 1 50); do
  if [[ -s "${TMP_DIR}/port" ]]; then
    break
  fi
  sleep 0.1
done

if [[ ! -s "${TMP_DIR}/port" ]]; then
  echo "stub server did not start"
  exit 1
fi

PORT="$(cat "${TMP_DIR}/port")"
SUMMARY_JSON="${TMP_DIR}/summary.json"
OUTPUT="$(
  BASE_URL="http://127.0.0.1:${PORT}" \
  DURATION_SEC=1 \
  CONCURRENCY=2 \
  RSS_SAMPLING=0 \
  SOAK_PATHS="/custom/health,/monitor/api/stats" \
  SUMMARY_JSON="${SUMMARY_JSON}" \
  "${TMP_DIR}/scripts/soak-demo-api.sh"
)"

if [[ "${OUTPUT}" != *"[ok] soak login"* ]]; then
  echo "expected soak login output"
  echo "${OUTPUT}"
  exit 1
fi

if [[ "${OUTPUT}" != *"[ok] soak summary"* ]]; then
  echo "expected soak summary output"
  echo "${OUTPUT}"
  exit 1
fi

if [[ ! -s "${SUMMARY_JSON}" ]]; then
  echo "expected summary json file"
  exit 1
fi

python3 - "${SUMMARY_JSON}" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    payload = json.load(handle)

if payload.get("concurrency") != 2:
    raise SystemExit("expected concurrency=2 in summary json")

paths = payload.get("paths")
if paths != ["/custom/health", "/monitor/api/stats"]:
    raise SystemExit(f"unexpected paths in summary json: {paths!r}")

if int(payload.get("requests", 0)) <= 0:
    raise SystemExit("expected requests > 0 in summary json")
PY

set +e
FAIL_OUTPUT="$(
  BASE_URL="http://127.0.0.1:${PORT}" \
  DURATION_SEC=1 \
  CONCURRENCY=1 \
  RSS_SAMPLING=0 \
  SOAK_PATHS="/custom/slow" \
  MAX_P95_MS=10 \
  "${TMP_DIR}/scripts/soak-demo-api.sh" 2>&1
)"
FAIL_STATUS=$?
set -e

if [[ ${FAIL_STATUS} -eq 0 ]]; then
  echo "expected soak threshold failure"
  exit 1
fi

if [[ "${FAIL_OUTPUT}" != *"p95_ms threshold exceeded"* ]]; then
  echo "expected p95 threshold failure output"
  echo "${FAIL_OUTPUT}"
  exit 1
fi

echo "soak-demo-api test passed"
