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
CALLBACK_NO="${CALLBACK_NO:-seed-demo-callback-01}"

BASE_URL="${BASE_URL%/}"

BASE_URL="${BASE_URL}" \
ADMIN_EMAIL="${ADMIN_EMAIL}" \
ADMIN_PASSWORD="${ADMIN_PASSWORD}" \
USER_EMAIL="${USER_EMAIL}" \
USER_PASSWORD="${USER_PASSWORD}" \
PENDING_TRADE_NO="${PENDING_TRADE_NO}" \
PAYMENT_GATEWAY="${PAYMENT_GATEWAY}" \
CALLBACK_NO="${CALLBACK_NO}" \
python3 - <<'PY'
import hashlib
import hmac
import json
import os
import subprocess
import sys
import time
import urllib.parse
from typing import Optional


base_url = os.environ["BASE_URL"]
admin_email = os.environ["ADMIN_EMAIL"]
admin_password = os.environ["ADMIN_PASSWORD"]
user_email = os.environ["USER_EMAIL"]
user_password = os.environ["USER_PASSWORD"]
pending_trade_no = os.environ["PENDING_TRADE_NO"]
payment_gateway = os.environ["PAYMENT_GATEWAY"]
callback_no = os.environ["CALLBACK_NO"]


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


def curl_raw(
    path: str,
    method: str = "POST",
    *,
    body: str = "",
    content_type: str,
    headers: Optional[list[str]] = None,
) -> str:
    url = path if path.startswith("http://") or path.startswith("https://") else base_url + path
    command = [
        "curl",
        "-fsS",
        "-X",
        method,
        url,
        "-H",
        f"Content-Type: {content_type}",
    ]
    for header in headers or []:
        command.extend(["-H", header])
    command.extend(["--data", body])
    try:
        return subprocess.check_output(command, text=True).strip()
    except subprocess.CalledProcessError as exc:
        fail(f"request failed: {' '.join(command)}\n{exc.output}")


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


def decoded_query(params: dict) -> str:
    return urllib.parse.unquote(encoded_query(params))


def encoded_query(params: dict) -> str:
    return urllib.parse.urlencode(sorted(params.items()))


def config_string(config: dict, key: str) -> str:
    return str(config.get(key, "")).strip()


def post_epay_notify(notify_path: str, config: dict) -> None:
    payment_key = config_string(config, "key")
    require(payment_key != "", "admin payment fetch: key missing")

    notify_params = {
        "out_trade_no": pending_trade_no,
        "trade_no": callback_no,
    }
    notify_params["sign"] = hashlib.md5((decoded_query(notify_params) + payment_key).encode("utf-8")).hexdigest()
    notify_params["sign_type"] = "MD5"

    notify_raw = curl_raw(
        notify_path,
        body=urllib.parse.urlencode(notify_params),
        content_type="application/x-www-form-urlencoded",
    )
    require(notify_raw == "success", f"payment notify: expected success, got {notify_raw!r}")


def post_coinpayments_notify(notify_path: str, config: dict) -> None:
    merchant_id = config_string(config, "coinpayments_merchant_id")
    ipn_secret = config_string(config, "coinpayments_ipn_secret")
    require(merchant_id != "", "admin payment fetch: coinpayments_merchant_id missing")
    require(ipn_secret != "", "admin payment fetch: coinpayments_ipn_secret missing")

    notify_params = {
        "merchant": merchant_id,
        "status": "100",
        "txn_id": callback_no,
        "item_number": pending_trade_no,
        "currency1": "USD",
        "amount1": "12.99",
    }
    encoded = encoded_query(notify_params)
    signature = hmac.new(ipn_secret.encode("utf-8"), encoded.encode("utf-8"), hashlib.sha512).hexdigest()
    notify_raw = curl_raw(
        notify_path,
        body=encoded,
        content_type="application/x-www-form-urlencoded",
        headers=[f"Hmac: {signature}"],
    )
    require(notify_raw == "IPN OK", f"payment notify: expected IPN OK, got {notify_raw!r}")


def post_stripe_checkout_notify(notify_path: str, config: dict) -> None:
    webhook_key = config_string(config, "stripe_webhook_key")
    require(webhook_key != "", "admin payment fetch: stripe_webhook_key missing")

    event = {
        "type": "checkout.session.completed",
        "data": {
            "object": {
                "client_reference_id": pending_trade_no,
                "payment_intent": callback_no,
                "payment_status": "paid",
            }
        },
    }
    body = json.dumps(event, separators=(",", ":"))
    timestamp = str(int(time.time()))
    signature = hmac.new(webhook_key.encode("utf-8"), f"{timestamp}.{body}".encode("utf-8"), hashlib.sha256).hexdigest()
    notify_raw = curl_raw(
        notify_path,
        body=body,
        content_type="application/json",
        headers=[f"Stripe-Signature: t={timestamp},v1={signature}"],
    )
    require(notify_raw == "success", f"payment notify: expected success, got {notify_raw!r}")


def post_notify(payment_gateway: str, notify_path: str, config: dict) -> None:
    if payment_gateway == "EPay":
        post_epay_notify(notify_path, config)
        return
    if payment_gateway == "CoinPayments":
        post_coinpayments_notify(notify_path, config)
        return
    if payment_gateway == "StripeCheckout":
        post_stripe_checkout_notify(notify_path, config)
        return
    fail(f"unsupported PAYMENT_GATEWAY for verify-demo-payment-notify.sh: {payment_gateway}")


admin_auth = login(admin_email, admin_password, "admin")
user_auth = login(user_email, user_password, "user")

payments_payload = curl_json(f"/api/v1/localadmin/payment/fetch?auth_data={admin_auth}")
payments = payments_payload.get("data")
require(isinstance(payments, list), "admin payment fetch: expected data list")

seed_payment = None
for item in payments:
    if isinstance(item, dict) and item.get("payment") == payment_gateway and "[seed-demo]" in str(item.get("name", "")):
        seed_payment = item
        break
require(isinstance(seed_payment, dict), f"admin payment fetch: missing seeded {payment_gateway} payment")

config = seed_payment.get("config")
require(isinstance(config, dict), "admin payment fetch: config missing")

notify_url = str(seed_payment.get("notify_url", "")).strip()
require(notify_url != "", "admin payment fetch: notify_url missing")
notify_path = urllib.parse.urlparse(notify_url).path
require(notify_path.startswith("/api/v1/guest/payment/notify/"), "admin payment fetch: invalid notify path")
ok("admin payment fetch")

order_status_payload = curl_json(f"/api/v1/user/order/check?auth_data={user_auth}&trade_no={pending_trade_no}")
require(order_status_payload.get("data") == 0, "pre-notify order status: expected pending status=0")
ok("pre-notify order status")

post_notify(payment_gateway, notify_path, config)
ok("payment notify")

post_status_payload = curl_json(f"/api/v1/user/order/check?auth_data={user_auth}&trade_no={pending_trade_no}")
require(post_status_payload.get("data") == 3, "post-notify order status: expected paid status=3")
ok("post-notify order status")

print("[ok] demo payment notify verification finished")
PY
