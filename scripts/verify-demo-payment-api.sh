#!/usr/bin/env bash
set -euo pipefail

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required"
  exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required"
  exit 1
fi

BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@example.com}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-Admin123456}"
USER_EMAIL="${USER_EMAIL:-seed-demo-owner@example.com}"
USER_PASSWORD="${USER_PASSWORD:-Seed123456}"
PENDING_TRADE_NO="${PENDING_TRADE_NO:-seed-demo-order-pending-01}"
PAYMENT_GATEWAY="${PAYMENT_GATEWAY:-EPay}"

BASE_URL="${BASE_URL%/}"

BASE_URL="${BASE_URL}" \
ADMIN_EMAIL="${ADMIN_EMAIL}" \
ADMIN_PASSWORD="${ADMIN_PASSWORD}" \
USER_EMAIL="${USER_EMAIL}" \
USER_PASSWORD="${USER_PASSWORD}" \
PENDING_TRADE_NO="${PENDING_TRADE_NO}" \
PAYMENT_GATEWAY="${PAYMENT_GATEWAY}" \
python3 - <<'PY'
import json
import os
import subprocess
import sys
from typing import Optional


base_url = os.environ["BASE_URL"]
admin_email = os.environ["ADMIN_EMAIL"]
admin_password = os.environ["ADMIN_PASSWORD"]
user_email = os.environ["USER_EMAIL"]
user_password = os.environ["USER_PASSWORD"]
pending_trade_no = os.environ["PENDING_TRADE_NO"]
payment_gateway = os.environ["PAYMENT_GATEWAY"]


def fail(message: str) -> None:
    print(message, file=sys.stderr)
    raise SystemExit(1)


def ok(label: str) -> None:
    print(f"[ok] {label}")


def curl_json(path: str, method: str = "GET", payload: Optional[dict] = None) -> dict:
    url = path if path.startswith("http://") or path.startswith("https://") else base_url + path
    command = ["curl", "-fsS", url]
    if method != "GET":
        command = [
            "curl",
            "-fsS",
            "-X",
            method,
            url,
            "-H",
            "Content-Type: application/json",
            "-d",
            json.dumps(payload or {}, separators=(",", ":")),
        ]
    try:
        raw = subprocess.check_output(command, text=True)
    except subprocess.CalledProcessError as exc:
        fail(f"request failed: {' '.join(command)}\n{exc.output}")
    try:
        return json.loads(raw)
    except json.JSONDecodeError as exc:
        fail(f"invalid json from {url}: {exc}")


def require(condition: bool, message: str) -> None:
    if not condition:
        fail(message)


def login(email: str, password: str, label: str) -> str:
    payload = curl_json(
        "/api/v1/passport/auth/login",
        method="POST",
        payload={"email": email, "password": password},
    )
    require(isinstance(payload.get("data"), dict), f"{label}: login data missing")
    auth_data = payload["data"].get("auth_data", "")
    require(isinstance(auth_data, str) and auth_data != "", f"{label}: auth_data missing")
    ok(f"{label} login")
    return auth_data


admin_auth = login(admin_email, admin_password, "admin")
user_auth = login(user_email, user_password, "user")

admin_payment_methods_payload = curl_json(f"/api/v1/localadmin/payment/getPaymentMethods?auth_data={admin_auth}")
admin_payment_methods = admin_payment_methods_payload.get("data")
require(isinstance(admin_payment_methods, list), "admin payment methods: expected data list")
require(payment_gateway in admin_payment_methods, f"admin payment methods: missing {payment_gateway}")
ok("admin payment methods")

admin_payment_form_payload = curl_json(
    f"/api/v1/localadmin/payment/getPaymentForm?auth_data={admin_auth}&payment={payment_gateway}"
)
admin_payment_form = admin_payment_form_payload.get("data")
require(isinstance(admin_payment_form, dict), "admin payment form: expected data object")
for field in ("url", "pid", "key"):
    require(field in admin_payment_form, f"admin payment form: missing field {field}")
ok("admin payment form")

user_payment_methods_payload = curl_json(f"/api/v1/user/order/getPaymentMethod?auth_data={user_auth}")
user_payment_methods = user_payment_methods_payload.get("data")
require(isinstance(user_payment_methods, list), "user payment methods: expected data list")
gateway_method = None
for item in user_payment_methods:
    if isinstance(item, dict) and item.get("payment") == payment_gateway:
        gateway_method = item
        break
require(isinstance(gateway_method, dict), f"user payment methods: missing {payment_gateway}")
method_id = gateway_method.get("id")
require(isinstance(method_id, int), "user payment methods: gateway id missing")
ok("user payment methods")

order_detail_payload = curl_json(f"/api/v1/user/order/detail?auth_data={user_auth}&trade_no={pending_trade_no}")
order_detail = order_detail_payload.get("data")
require(isinstance(order_detail, dict), "order detail: expected data object")
require(order_detail.get("trade_no") == pending_trade_no, "order detail: trade_no mismatch")
require(order_detail.get("status") == 0, "order detail: expected pending order status=0")
ok("order detail")

checkout_payload = curl_json(
    "/api/v1/user/order/checkout",
    method="POST",
    payload={
        "auth_data": user_auth,
        "trade_no": pending_trade_no,
        "method": method_id,
    },
)
require(checkout_payload.get("type") == 1, "order checkout: expected type=1 redirect result")
checkout_url = checkout_payload.get("data")
require(isinstance(checkout_url, str) and checkout_url != "", "order checkout: redirect url missing")
require(pending_trade_no in checkout_url, "order checkout: missing trade_no in redirect url")
require(payment_gateway.lower() in checkout_url.lower() or "pay.example.com" in checkout_url.lower(), "order checkout: unexpected redirect url")
ok("order checkout")

print("[ok] demo payment api verification finished")
PY
