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

cat > "${TMP_DIR}/stub_server.py" <<'PY'
import json
import os
import sys
import urllib.parse
from http.server import BaseHTTPRequestHandler, HTTPServer

port_file = sys.argv[1]
expected_token = "smoke+secret/=?"
expected_node_id = "7"
expected_node_type = "v2ray"
expected_local_port = "12345"


class Handler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        return

    def _write(self, status, payload):
        body = json.dumps(payload, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _validate_node_query(self, require_type=True, require_local_port=False):
        parsed = urllib.parse.urlparse(self.path)
        query = urllib.parse.parse_qs(parsed.query)
        if query.get("token", [""])[0] != expected_token:
            self._write(500, {"message": "token is error"})
            return False
        if query.get("node_id", [""])[0] != expected_node_id:
            self._write(500, {"message": "node_id is invalid"})
            return False
        if require_type and query.get("node_type", [""])[0] != expected_node_type:
            self._write(500, {"message": "node_type is invalid"})
            return False
        if require_local_port and query.get("local_port", [""])[0] != expected_local_port:
            self._write(500, {"message": "local_port is invalid"})
            return False
        return True

    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        if path == "/healthz":
            self._write(200, {"status": "ok", "app": "stub"})
            return
        if path == "/readyz":
            self._write(200, {"postgres_configured": True, "status": "ready"})
            return
        if path == "/api/_meta/runtime":
            self._write(200, {"admin_path": "localadmin"})
            return
        if path == "/monitor/api/stats":
            self._write(200, {"current_jobs": 0, "status": "running", "workers": 4})
            return
        if path == "/api/v1/server/UniProxy/user":
            if self._validate_node_query():
                self._write(200, {"users": []})
            return
        if path == "/api/v1/server/UniProxy/config":
            if self._validate_node_query():
                self._write(200, {"base_config": {"push_interval": 60, "pull_interval": 60}})
            return
        if path == "/api/v1/server/UniProxy/alivelist":
            if self._validate_node_query():
                self._write(200, {"alive": {}})
            return
        if path == "/api/v1/server/Deepbwork/user":
            if self._validate_node_query(require_type=False):
                self._write(200, {"msg": "ok", "data": []})
            return
        if path == "/api/v1/server/Deepbwork/config":
            if self._validate_node_query(require_type=False, require_local_port=True):
                self._write(
                    200,
                    {
                        "inbounds": [
                            {"port": 8443, "protocol": "vmess"},
                            {"port": int(expected_local_port), "protocol": "dokodemo-door"},
                        ]
                    },
                )
            return
        self._write(404, {"message": "not found"})


server = HTTPServer(("127.0.0.1", 0), Handler)
with open(port_file, "w", encoding="utf-8") as handle:
    handle.write(str(server.server_address[1]))
    handle.flush()
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
OUTPUT="$(
  BASE_URL="http://127.0.0.1:${PORT}" \
  SERVER_TOKEN='smoke+secret/=?' \
  NODE_ID=7 \
  NODE_TYPE=v2ray \
  ./scripts/smoke-node-api.sh
)"

if [[ "${OUTPUT}" != *"[ok] node smoke finished"* ]]; then
  echo "unexpected smoke output"
  echo "${OUTPUT}"
  exit 1
fi

echo "smoke-node-api test passed"
