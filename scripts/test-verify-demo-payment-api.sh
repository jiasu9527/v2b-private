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
cp "${REPO_ROOT}/scripts/verify-demo-payment-api.sh" "${TMP_DIR}/scripts/verify-demo-payment-api.sh"
chmod +x "${TMP_DIR}/scripts/verify-demo-payment-api.sh"

cat > "${TMP_DIR}/stub_server.py" <<'PY'
import json
import sys
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
        if self.path == "/api/v1/passport/auth/login":
            payload = self._read_json()
            email = payload.get("email")
            password = payload.get("password")
            if email == "admin@example.com" and password == "Admin123456":
                self._write(200, {"data": {"auth_data": ADMIN_AUTH}})
                return
            if email == "seed-demo-owner@example.com" and password == "Seed123456":
                self._write(200, {"data": {"auth_data": USER_AUTH}})
                return
            self._write(403, {"message": "invalid credentials"})
            return

        if self.path == "/api/v1/user/order/checkout":
            payload = self._read_json()
            if payload.get("auth_data") != USER_AUTH or str(payload.get("method")) != "7" or payload.get("trade_no") != "seed-demo-order-pending-01":
                self._write(400, {"message": "bad checkout payload"})
                return
            self._write(200, {"data": "https://pay.example.com/submit.php?out_trade_no=seed-demo-order-pending-01", "type": 1})
            return

        self._write(404, {"message": "not found"})

    def do_GET(self):
        if self.path == f"/api/v1/user/order/getPaymentMethod?auth_data={USER_AUTH}":
            self._write(200, {"data": [{"id": 7, "payment": "EPay", "name": "[seed-demo] EPay"}]})
            return
        if self.path == f"/api/v1/user/order/detail?auth_data={USER_AUTH}&trade_no=seed-demo-order-pending-01":
            self._write(200, {"data": {"trade_no": "seed-demo-order-pending-01", "status": 0, "total_amount": 1299}})
            return
        if self.path == f"/api/v1/localadmin/payment/getPaymentMethods?auth_data={ADMIN_AUTH}":
            self._write(200, {"data": ["EPay", "StripeCheckout", "WechatPayNative"]})
            return
        if self.path == f"/api/v1/localadmin/payment/getPaymentForm?auth_data={ADMIN_AUTH}&payment=EPay":
            self._write(200, {"data": {"url": {"type": "input"}, "pid": {"type": "input"}, "key": {"type": "input"}}})
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
  "${TMP_DIR}/scripts/verify-demo-payment-api.sh"
)"

if [[ "${OUTPUT}" != *"[ok] user payment methods"* ]]; then
  echo "expected user payment methods output"
  echo "${OUTPUT}"
  exit 1
fi

if [[ "${OUTPUT}" != *"[ok] order checkout"* ]]; then
  echo "expected order checkout output"
  echo "${OUTPUT}"
  exit 1
fi

if [[ "${OUTPUT}" != *"[ok] demo payment api verification finished"* ]]; then
  echo "expected final success output"
  echo "${OUTPUT}"
  exit 1
fi

echo "verify-demo-payment-api test passed"
