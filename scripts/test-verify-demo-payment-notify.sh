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
cp "${REPO_ROOT}/scripts/verify-demo-payment-notify.sh" "${TMP_DIR}/scripts/verify-demo-payment-notify.sh"
chmod +x "${TMP_DIR}/scripts/verify-demo-payment-notify.sh"

cat > "${TMP_DIR}/stub_server.py" <<'PY'
import hashlib
import hmac
import json
import sys
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, HTTPServer

port_file = sys.argv[1]
ADMIN_AUTH = "admin-auth-token"
USER_AUTH = "user-auth-token"
PAYMENTS = [
    {
        "payment": "EPay",
        "name": "[seed-demo] EPay",
        "config": {"key": "seed-demo-key"},
        "notify_url": "https://notify.example.com/api/v1/guest/payment/notify/EPay/uuid-epay",
    },
    {
        "payment": "CoinPayments",
        "name": "[seed-demo] CoinPayments",
        "config": {
            "coinpayments_merchant_id": "merchant-1001",
            "coinpayments_ipn_secret": "coinpayments-secret",
        },
        "notify_url": "https://notify.example.com/api/v1/guest/payment/notify/CoinPayments/uuid-coinpayments",
    },
    {
        "payment": "StripeCheckout",
        "name": "[seed-demo] StripeCheckout",
        "config": {
            "stripe_webhook_key": "whsec_seed_demo",
        },
        "notify_url": "https://notify.example.com/api/v1/guest/payment/notify/StripeCheckout/uuid-stripe-checkout",
    },
]
ORDER_STATUS = {
    "seed-demo-order-pending-01": 0,
    "seed-demo-order-cpay-pending-01": 0,
    "seed-demo-order-stchk-pending-01": 0,
}


def decoded_query(params):
    return urllib.parse.unquote(urllib.parse.urlencode(params))


def stripe_signature(secret, body):
    timestamp = str(int(time.time()))
    signature = hmac.new(
        secret.encode("utf-8"),
        (timestamp + "." + body).encode("utf-8"),
        hashlib.sha256,
    ).hexdigest()
    return f"t={timestamp},v1={signature}"


def encoded_query(params):
    return urllib.parse.urlencode(sorted(params.items()))


def verify_stripe_signature(secret, header, body):
    timestamp = ""
    signature = ""
    for part in header.split(","):
        part = part.strip()
        if part.startswith("t="):
            timestamp = part[2:]
        if part.startswith("v1="):
            signature = part[3:]
    if not timestamp or not signature:
        return False
    expected = hmac.new(
        secret.encode("utf-8"),
        (timestamp + "." + body).encode("utf-8"),
        hashlib.sha256,
    ).hexdigest()
    return hmac.compare_digest(signature, expected)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        return

    def _read_json(self):
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length).decode("utf-8")
        if not raw:
            return {}
        return json.loads(raw)

    def _write_json(self, status, payload):
        body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _write_text(self, status, body):
        encoded = body.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def do_POST(self):
        if self.path == "/api/v1/passport/auth/login":
            payload = self._read_json()
            email = payload.get("email")
            password = payload.get("password")
            if email == "admin@example.com" and password == "Admin123456":
                self._write_json(200, {"data": {"auth_data": ADMIN_AUTH}})
                return
            if email == "seed-demo-owner@example.com" and password == "Seed123456":
                self._write_json(200, {"data": {"auth_data": USER_AUTH}})
                return
            self._write_json(403, {"message": "invalid credentials"})
            return

        if self.path == "/api/v1/guest/payment/notify/EPay/uuid-epay":
            length = int(self.headers.get("Content-Length", "0"))
            body = self.rfile.read(length).decode("utf-8")
            params = dict(urllib.parse.parse_qsl(body))
            provided = params.get("sign", "")
            sign_params = {k: v for k, v in params.items() if k not in ("sign", "sign_type")}
            expected = hashlib.md5((decoded_query(sign_params) + "seed-demo-key").encode("utf-8")).hexdigest()
            if provided != expected:
                self._write_text(500, "fail")
                return
            ORDER_STATUS[params.get("out_trade_no", "")] = 3
            self._write_text(200, "success")
            return

        if self.path == "/api/v1/guest/payment/notify/CoinPayments/uuid-coinpayments":
            length = int(self.headers.get("Content-Length", "0"))
            body = self.rfile.read(length).decode("utf-8")
            params = dict(urllib.parse.parse_qsl(body))
            payload = encoded_query(params)
            provided = self.headers.get("Hmac", "")
            expected = hmac.new(
                b"coinpayments-secret",
                payload.encode("utf-8"),
                hashlib.sha512,
            ).hexdigest()
            if provided != expected or params.get("merchant") != "merchant-1001":
                self._write_text(500, "fail")
                return
            if params.get("status") not in ("100", "2"):
                self._write_text(200, "IPN OK: pending")
                return
            ORDER_STATUS[params.get("item_number", "")] = 3
            self._write_text(200, "IPN OK")
            return

        if self.path == "/api/v1/guest/payment/notify/StripeCheckout/uuid-stripe-checkout":
            length = int(self.headers.get("Content-Length", "0"))
            body = self.rfile.read(length).decode("utf-8")
            signature = self.headers.get("Stripe-Signature", "")
            if not verify_stripe_signature("whsec_seed_demo", signature, body):
                self._write_text(500, '{"message":"bad signature"}')
                return
            payload = json.loads(body)
            obj = payload.get("data", {}).get("object", {})
            ORDER_STATUS[obj.get("client_reference_id", "")] = 3
            self._write_text(200, "success")
            return

        self._write_json(404, {"message": "not found"})

    def do_GET(self):
        if self.path == f"/api/v1/localadmin/payment/fetch?auth_data={ADMIN_AUTH}":
            self._write_json(200, {"data": PAYMENTS})
            return
        for trade_no, status in ORDER_STATUS.items():
            if self.path == f"/api/v1/user/order/check?auth_data={USER_AUTH}&trade_no={trade_no}":
                self._write_json(200, {"data": status})
                return
        self._write_json(404, {"message": "not found", "path": self.path})


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
run_case() {
  local gateway="$1"
  local trade_no="$2"
  local callback_no="$3"
  local output
  output="$(
    BASE_URL="http://127.0.0.1:${PORT}" \
    PAYMENT_GATEWAY="${gateway}" \
    PENDING_TRADE_NO="${trade_no}" \
    CALLBACK_NO="${callback_no}" \
    "${TMP_DIR}/scripts/verify-demo-payment-notify.sh"
  )"

  if [[ "${output}" != *"[ok] pre-notify order status"* ]]; then
    echo "expected pre-notify output for ${gateway}"
    echo "${output}"
    exit 1
  fi

  if [[ "${output}" != *"[ok] payment notify"* ]]; then
    echo "expected payment notify output for ${gateway}"
    echo "${output}"
    exit 1
  fi

  if [[ "${output}" != *"[ok] demo payment notify verification finished"* ]]; then
    echo "expected final success output for ${gateway}"
    echo "${output}"
    exit 1
  fi
}

run_case "EPay" "seed-demo-order-pending-01" "seed-demo-callback-01"
run_case "CoinPayments" "seed-demo-order-cpay-pending-01" "seed-demo-callback-cpay-01"
run_case "StripeCheckout" "seed-demo-order-stchk-pending-01" "seed-demo-callback-stchk-01"

echo "verify-demo-payment-notify test passed"
