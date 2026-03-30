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
cp "${REPO_ROOT}/scripts/verify-demo-api.sh" "${TMP_DIR}/scripts/verify-demo-api.sh"
chmod +x "${TMP_DIR}/scripts/verify-demo-api.sh"

cat > "${TMP_DIR}/stub_server.py" <<'PY'
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

port_file = sys.argv[1]

ADMIN_AUTH = "admin-auth-token"
USER_AUTH = "user-auth-token"
STAFF_AUTH = "staff-auth-token"


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

    def _auth(self):
        token = self.path.split("auth_data=", 1)[1].split("&", 1)[0] if "auth_data=" in self.path else ""
        return token

    def do_POST(self):
        if self.path != "/api/v1/passport/auth/login":
            self._write(404, {"message": "not found"})
            return

        payload = self._read_json()
        email = payload.get("email")
        password = payload.get("password")

        if email == "admin@example.com" and password == "Admin123456":
            self._write(200, {"data": {"auth_data": ADMIN_AUTH}})
            return
        if email == "seed-demo-owner@example.com" and password == "Seed123456":
            self._write(200, {"data": {"auth_data": USER_AUTH}})
            return
        if email == "seed-demo-staff@example.com" and password == "Seed123456":
            self._write(200, {"data": {"auth_data": STAFF_AUTH}})
            return

        self._write(403, {"message": "invalid credentials"})

    def do_GET(self):
        path = self.path
        auth = self._auth()

        if path == "/api/_meta/runtime":
            self._write(200, {"admin_path": "localadmin"})
            return
        if path == "/monitor/api/stats":
            self._write(200, {"status": "running", "current_jobs": 0, "workers": 4})
            return

        if path == f"/api/v1/localadmin/config/fetch?auth_data={ADMIN_AUTH}":
            self._write(200, {"data": {"secure_path": "localadmin"}})
            return
        if path == f"/api/v1/localadmin/server/manage/getNodes?auth_data={ADMIN_AUTH}":
            self._write(200, {"data": [{"id": 3, "type": "vmess", "name": "[seed-demo] VMess"}]})
            return
        if path == f"/api/v1/localadmin/coupon/fetch?auth_data={ADMIN_AUTH}":
            self._write(200, {"data": [{"code": "SEEDDEMOCOUPON00000000000000001"}], "total": 1})
            return
        if path == f"/api/v1/localadmin/giftcard/fetch?auth_data={ADMIN_AUTH}":
            self._write(200, {"data": [{"code": "SEEDDEMOGIFTCARD00000000000001"}], "total": 1})
            return
        if path == f"/api/v1/localadmin/notice/fetch?auth_data={ADMIN_AUTH}":
            self._write(200, {"data": [{"title": "[seed-demo] Notice Visible"}]})
            return
        if path == f"/api/v1/localadmin/payment/fetch?auth_data={ADMIN_AUTH}":
            self._write(200, {"data": [{"name": "[seed-demo] EPay"}]})
            return
        if path == f"/api/v1/localadmin/knowledge/fetch?auth_data={ADMIN_AUTH}":
            self._write(200, {"data": [{"title": "[seed-demo] FAQ"}]})
            return
        if path == f"/api/v1/localadmin/ticket/fetch?auth_data={ADMIN_AUTH}":
            self._write(200, {"data": [{"subject": "[seed-demo] Ticket"}], "total": 1})
            return
        if path == f"/api/v1/localadmin/order/fetch?auth_data={ADMIN_AUTH}":
            self._write(200, {"data": [{"trade_no": "seed-demo-order-paid-01"}], "total": 1})
            return
        if path == f"/api/v1/localadmin/invite/campaign/fetch?auth_data={ADMIN_AUTH}":
            self._write(200, {"data": [{"invite_code": "22223333444455556666777788889999"}], "total": 1})
            return

        if path == f"/api/v1/user/checkLogin?auth_data={USER_AUTH}":
            self._write(200, {"data": {"is_login": True}})
            return
        if path == f"/api/v1/user/ticket/fetch?auth_data={USER_AUTH}":
            self._write(200, {"data": [{"subject": "[seed-demo] Ticket"}]})
            return
        if path == f"/api/v1/user/order/fetch?auth_data={USER_AUTH}":
            self._write(200, {"data": [{"trade_no": "seed-demo-order-paid-01"}]})
            return
        if path == f"/api/v1/user/invite/campaign/fetch?auth_data={USER_AUTH}":
            self._write(200, {"data": {"invite_code": "22223333444455556666777788889999"}, "enabled": True, "settings": {"limit": 1}})
            return
        if path == f"/api/v1/user/knowledge/fetch?auth_data={USER_AUTH}&language=en-US":
            self._write(200, {"data": {"faq": [{"title": "[seed-demo] FAQ"}]}})
            return

        if path == f"/api/v1/staff/ticket/fetch?auth_data={STAFF_AUTH}":
            self._write(200, {"data": [{"subject": "[seed-demo] Ticket"}], "total": 1})
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
OUTPUT="$(
  BASE_URL="http://127.0.0.1:${PORT}" \
  "${TMP_DIR}/scripts/verify-demo-api.sh"
)"

if [[ "${OUTPUT}" != *"[ok] admin login"* ]]; then
  echo "expected admin login check output"
  echo "${OUTPUT}"
  exit 1
fi

if [[ "${OUTPUT}" != *"[ok] user invite campaign"* ]]; then
  echo "expected user invite campaign check output"
  echo "${OUTPUT}"
  exit 1
fi

if [[ "${OUTPUT}" != *"[ok] demo api verification finished"* ]]; then
  echo "expected final success output"
  echo "${OUTPUT}"
  exit 1
fi

echo "verify-demo-api test passed"
