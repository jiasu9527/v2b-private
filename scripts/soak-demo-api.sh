#!/usr/bin/env bash
set -euo pipefail

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required"
  exit 1
fi

BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@example.com}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-Admin123456}"
USER_EMAIL="${USER_EMAIL:-seed-demo-owner@example.com}"
USER_PASSWORD="${USER_PASSWORD:-Seed123456}"
DURATION_SEC="${DURATION_SEC:-15}"
CONCURRENCY="${CONCURRENCY:-8}"
RSS_SAMPLING="${RSS_SAMPLING:-1}"
TARGET_PID="${TARGET_PID:-}"
SOAK_PATHS="${SOAK_PATHS:-}"
SUMMARY_JSON="${SUMMARY_JSON:-}"
MAX_P95_MS="${MAX_P95_MS:-0}"
MAX_RSS_DELTA_KB="${MAX_RSS_DELTA_KB:-0}"

BASE_URL="${BASE_URL%/}"

BASE_URL="${BASE_URL}" \
ADMIN_EMAIL="${ADMIN_EMAIL}" \
ADMIN_PASSWORD="${ADMIN_PASSWORD}" \
USER_EMAIL="${USER_EMAIL}" \
USER_PASSWORD="${USER_PASSWORD}" \
DURATION_SEC="${DURATION_SEC}" \
CONCURRENCY="${CONCURRENCY}" \
RSS_SAMPLING="${RSS_SAMPLING}" \
TARGET_PID="${TARGET_PID}" \
SOAK_PATHS="${SOAK_PATHS}" \
SUMMARY_JSON="${SUMMARY_JSON}" \
MAX_P95_MS="${MAX_P95_MS}" \
MAX_RSS_DELTA_KB="${MAX_RSS_DELTA_KB}" \
python3 - <<'PY'
import json
import os
import queue
import subprocess
import sys
import threading
import time
import urllib.parse
from typing import Optional


base_url = os.environ["BASE_URL"]
admin_email = os.environ["ADMIN_EMAIL"]
admin_password = os.environ["ADMIN_PASSWORD"]
user_email = os.environ["USER_EMAIL"]
user_password = os.environ["USER_PASSWORD"]
duration_sec = max(1, int(os.environ["DURATION_SEC"]))
concurrency = max(1, int(os.environ["CONCURRENCY"]))
rss_sampling = os.environ["RSS_SAMPLING"] != "0"
target_pid = os.environ["TARGET_PID"].strip()
soak_paths_raw = os.environ["SOAK_PATHS"].strip()
summary_json = os.environ["SUMMARY_JSON"].strip()
max_p95_ms = float(os.environ["MAX_P95_MS"] or "0")
max_rss_delta_kb = int(float(os.environ["MAX_RSS_DELTA_KB"] or "0"))


def fail(message: str) -> None:
    print(message, file=sys.stderr)
    raise SystemExit(1)


def ok(message: str) -> None:
    print(f"[ok] {message}")


def curl_json(path: str, method: str = "GET", payload: Optional[dict] = None) -> dict:
    url = path if path.startswith("http://") or path.startswith("https://") else base_url + path
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
    else:
        command = ["curl", "-fsS", url]
    try:
        raw = subprocess.check_output(command, text=True)
    except subprocess.CalledProcessError as exc:
        fail(f"request failed: {' '.join(command)}\n{exc.output}")
    return json.loads(raw)


def login(email: str, password: str) -> str:
    payload = curl_json(
        "/api/v1/passport/auth/login",
        method="POST",
        payload={"email": email, "password": password},
    )
    data = payload.get("data")
    if not isinstance(data, dict):
        fail("login: data missing")
    auth_data = data.get("auth_data", "")
    if not isinstance(auth_data, str) or not auth_data:
        fail("login: auth_data missing")
    return auth_data


def infer_pid() -> str:
    if target_pid:
        return target_pid
    if not rss_sampling:
        return ""
    parsed = urllib.parse.urlparse(base_url)
    port = parsed.port
    if port is None:
        port = 443 if parsed.scheme == "https" else 80
    try:
        raw = subprocess.check_output(
            ["bash", "-lc", f"lsof -tiTCP:{port} -sTCP:LISTEN | head -n1"],
            text=True,
        ).strip()
    except subprocess.CalledProcessError:
        return ""
    return raw


def parse_paths(raw: str, admin_auth: str, user_auth: str) -> list[str]:
    if raw:
        parsed = []
        for item in raw.replace("\n", ",").split(","):
            path = item.strip()
            if path:
                parsed.append(path)
        if not parsed:
            fail("SOAK_PATHS was provided but no valid paths were parsed")
        return parsed
    return [
        "/healthz",
        "/monitor/api/stats",
        f"/api/v1/localadmin/config/fetch?auth_data={admin_auth}",
        f"/api/v1/localadmin/system/getQueueWorkload?auth_data={admin_auth}",
        f"/api/v1/user/checkLogin?auth_data={user_auth}",
        f"/api/v1/user/order/fetch?auth_data={user_auth}",
    ]


def sample_rss(pid: str, stop_event: threading.Event, samples: list[int]) -> None:
    while not stop_event.is_set():
        try:
            raw = subprocess.check_output(["ps", "-o", "rss=", "-p", pid], text=True).strip()
            if raw:
                samples.append(int(raw))
        except Exception:
            pass
        stop_event.wait(1.0)


admin_auth = login(admin_email, admin_password)
user_auth = login(user_email, user_password)
ok("soak login")

paths = parse_paths(soak_paths_raw, admin_auth, user_auth)

latencies_ms: list[float] = []
errors: "queue.Queue[str]" = queue.Queue()
request_count = [0]
count_lock = threading.Lock()
start = time.time()
deadline = start + duration_sec
rss_samples: list[int] = []
rss_stop = threading.Event()
pid = infer_pid()

rss_thread = None
if pid and rss_sampling:
    rss_thread = threading.Thread(target=sample_rss, args=(pid, rss_stop, rss_samples), daemon=True)
    rss_thread.start()


def worker(worker_id: int) -> None:
    index = worker_id % len(paths)
    while time.time() < deadline and errors.empty():
        path = paths[index % len(paths)]
        index += 1
        began = time.time()
        try:
            raw = subprocess.check_output(["curl", "-fsS", base_url + path], text=True)
            json.loads(raw)
        except Exception as exc:
            errors.put(f"{path}: {exc}")
            return
        elapsed = (time.time() - began) * 1000
        with count_lock:
            request_count[0] += 1
            latencies_ms.append(elapsed)


threads = [threading.Thread(target=worker, args=(i,), daemon=True) for i in range(concurrency)]
for thread in threads:
    thread.start()
for thread in threads:
    thread.join()

rss_stop.set()
if rss_thread is not None:
    rss_thread.join(timeout=2)

if not errors.empty():
    fail(errors.get())

if request_count[0] == 0:
    fail("soak summary: no requests executed")

latencies_ms.sort()
p50 = latencies_ms[len(latencies_ms) // 2]
p95 = latencies_ms[min(len(latencies_ms) - 1, max(0, int(len(latencies_ms) * 0.95) - 1))]
summary_payload = {
    "requests": request_count[0],
    "duration_sec": duration_sec,
    "concurrency": concurrency,
    "p50_ms": round(p50, 1),
    "p95_ms": round(p95, 1),
    "paths": paths,
    "pid": pid or None,
}
summary = f"requests={request_count[0]} duration={duration_sec}s concurrency={concurrency} p50_ms={p50:.1f} p95_ms={p95:.1f}"
if rss_samples:
    rss_min = min(rss_samples)
    rss_max = max(rss_samples)
    rss_delta = rss_max - rss_min
    summary_payload["rss_kb_min"] = rss_min
    summary_payload["rss_kb_max"] = rss_max
    summary_payload["rss_kb_delta"] = rss_delta
    summary += f" rss_kb_min={rss_min} rss_kb_max={rss_max}"

if summary_json:
    with open(summary_json, "w", encoding="utf-8") as handle:
        json.dump(summary_payload, handle, ensure_ascii=True, separators=(",", ":"))
        handle.write("\n")

if max_p95_ms > 0 and p95 > max_p95_ms:
    fail(f"p95_ms threshold exceeded: actual={p95:.1f} limit={max_p95_ms:.1f}")

if max_rss_delta_kb > 0:
    if not rss_samples:
        fail("rss_kb delta threshold requested but RSS sampling did not produce any samples")
    rss_delta = max(rss_samples) - min(rss_samples)
    if rss_delta > max_rss_delta_kb:
        fail(f"rss_kb delta threshold exceeded: actual={rss_delta} limit={max_rss_delta_kb}")

ok(f"soak summary {summary}")
PY
