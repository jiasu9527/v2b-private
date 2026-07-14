#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
page="$root/admin-src/src/pages/DNSFailoverPage.tsx"

grep -q '探针接入地址' "$page"
grep -q '查看安装命令' "$page"
grep -q '删除探针' "$page"
grep -q 'apiDelete(`${BASE}/probes/${p.id}`)' "$page"
grep -q 'apiPut(`${BASE}/settings`' "$page"
