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
STAFF_EMAIL="${STAFF_EMAIL:-seed-demo-staff@example.com}"
STAFF_PASSWORD="${STAFF_PASSWORD:-Seed123456}"
ADMIN_PATH="${ADMIN_PATH:-}"

BASE_URL="${BASE_URL%/}"

BASE_URL="${BASE_URL}" \
ADMIN_EMAIL="${ADMIN_EMAIL}" \
ADMIN_PASSWORD="${ADMIN_PASSWORD}" \
USER_EMAIL="${USER_EMAIL}" \
USER_PASSWORD="${USER_PASSWORD}" \
STAFF_EMAIL="${STAFF_EMAIL}" \
STAFF_PASSWORD="${STAFF_PASSWORD}" \
ADMIN_PATH="${ADMIN_PATH}" \
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
staff_email = os.environ["STAFF_EMAIL"]
staff_password = os.environ["STAFF_PASSWORD"]
admin_path = os.environ["ADMIN_PATH"].strip("/")


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


def require_list(payload: dict, label: str) -> list:
    data = payload.get("data")
    require(isinstance(data, list), f"{label}: expected data list")
    return data


def require_dict(payload: dict, label: str) -> dict:
    data = payload.get("data")
    require(isinstance(data, dict), f"{label}: expected data object")
    return data


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


runtime = curl_json("/api/_meta/runtime")
require(isinstance(runtime, dict), "runtime meta: expected object")
if not admin_path:
    runtime_admin_path = runtime.get("admin_path")
    require(isinstance(runtime_admin_path, str) and runtime_admin_path.strip() != "", "runtime meta: admin_path missing")
    admin_path = runtime_admin_path.strip("/")
ok("runtime meta")

admin_auth = login(admin_email, admin_password, "admin")
user_auth = login(user_email, user_password, "user")
staff_auth = login(staff_email, staff_password, "staff")

admin_config = curl_json(f"/api/v1/{admin_path}/config/fetch?auth_data={admin_auth}")
config_data = require_dict(admin_config, "admin config")
require("secure_path" in config_data or "site" in config_data or len(config_data) > 0, "admin config: expected config payload")
ok("admin config")

admin_nodes = require_list(curl_json(f"/api/v1/{admin_path}/server/manage/getNodes?auth_data={admin_auth}"), "admin nodes")
require(any(node.get("type") == "vmess" for node in admin_nodes if isinstance(node, dict)), "admin nodes: missing vmess node")
ok("admin nodes")

admin_coupon = require_list(curl_json(f"/api/v1/{admin_path}/coupon/fetch?auth_data={admin_auth}"), "admin coupon")
require(any("SEEDDEMOCOUPON" in str(item.get("code", "")) for item in admin_coupon if isinstance(item, dict)), "admin coupon: missing seeded coupon")
ok("admin coupon")

admin_giftcard = require_list(curl_json(f"/api/v1/{admin_path}/giftcard/fetch?auth_data={admin_auth}"), "admin giftcard")
require(any("SEEDDEMOGIFTCARD" in str(item.get("code", "")) for item in admin_giftcard if isinstance(item, dict)), "admin giftcard: missing seeded giftcard")
ok("admin giftcard")

admin_notice = require_list(curl_json(f"/api/v1/{admin_path}/notice/fetch?auth_data={admin_auth}"), "admin notice")
require(any("[seed-demo]" in str(item.get("title", "")) for item in admin_notice if isinstance(item, dict)), "admin notice: missing seeded notice")
ok("admin notice")

admin_payment = require_list(curl_json(f"/api/v1/{admin_path}/payment/fetch?auth_data={admin_auth}"), "admin payment")
require(any("[seed-demo]" in str(item.get("name", "")) for item in admin_payment if isinstance(item, dict)), "admin payment: missing seeded payment")
ok("admin payment")

admin_knowledge = require_list(curl_json(f"/api/v1/{admin_path}/knowledge/fetch?auth_data={admin_auth}"), "admin knowledge")
require(any("[seed-demo]" in str(item.get("title", "")) for item in admin_knowledge if isinstance(item, dict)), "admin knowledge: missing seeded knowledge")
ok("admin knowledge")

admin_ticket = require_list(curl_json(f"/api/v1/{admin_path}/ticket/fetch?auth_data={admin_auth}"), "admin ticket")
require(any("[seed-demo]" in str(item.get("subject", "")) for item in admin_ticket if isinstance(item, dict)), "admin ticket: missing seeded ticket")
ok("admin ticket")

admin_order = require_list(curl_json(f"/api/v1/{admin_path}/order/fetch?auth_data={admin_auth}"), "admin order")
require(any("seed-demo-order-" in str(item.get("trade_no", "")) for item in admin_order if isinstance(item, dict)), "admin order: missing seeded order")
ok("admin order")

admin_invite = require_list(curl_json(f"/api/v1/{admin_path}/invite/campaign/fetch?auth_data={admin_auth}"), "admin invite campaign")
require(any(str(item.get("invite_code", "")) != "" for item in admin_invite if isinstance(item, dict)), "admin invite campaign: missing invite code")
ok("admin invite campaign")

user_check = require_dict(curl_json(f"/api/v1/user/checkLogin?auth_data={user_auth}"), "user check login")
require(user_check.get("is_login") is True, "user check login: expected is_login=true")
ok("user check login")

user_ticket = require_list(curl_json(f"/api/v1/user/ticket/fetch?auth_data={user_auth}"), "user ticket")
require(any("[seed-demo]" in str(item.get("subject", "")) for item in user_ticket if isinstance(item, dict)), "user ticket: missing seeded ticket")
ok("user ticket")

user_order = require_list(curl_json(f"/api/v1/user/order/fetch?auth_data={user_auth}"), "user order")
require(any("seed-demo-order-" in str(item.get("trade_no", "")) for item in user_order if isinstance(item, dict)), "user order: missing seeded order")
ok("user order")

user_invite_payload = curl_json(f"/api/v1/user/invite/campaign/fetch?auth_data={user_auth}")
user_invite = require_dict(user_invite_payload, "user invite campaign")
require("invite_code" in user_invite, "user invite campaign: invite_code missing")
require("enabled" in user_invite_payload and "settings" in user_invite_payload, "user invite campaign: enabled/settings missing")
ok("user invite campaign")

user_knowledge = require_dict(curl_json(f"/api/v1/user/knowledge/fetch?auth_data={user_auth}&language=en-US"), "user knowledge")
require(any(isinstance(items, list) and len(items) > 0 for items in user_knowledge.values()), "user knowledge: expected category list")
ok("user knowledge")

staff_ticket = require_list(curl_json(f"/api/v1/staff/ticket/fetch?auth_data={staff_auth}"), "staff ticket")
require(any("[seed-demo]" in str(item.get("subject", "")) for item in staff_ticket if isinstance(item, dict)), "staff ticket: missing seeded ticket")
ok("staff ticket")

monitor = curl_json("/monitor/api/stats")
require(monitor.get("status") == "running", "queue monitor: expected running")
require("current_jobs" in monitor, "queue monitor: current_jobs missing")
ok("queue monitor")

print("[ok] demo api verification finished")
PY
