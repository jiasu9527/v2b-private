#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BAOTA_DOC="${REPO_ROOT}/docs/baota-go-single-machine.md"

[[ -f "${BAOTA_DOC}" ]] || { echo "missing ${BAOTA_DOC}"; exit 1; }

rg -n -F "./update.sh" "${BAOTA_DOC}" >/dev/null 2>&1 || { echo "missing update entry in baota doc"; exit 1; }
rg -n -F "install.sh" "${BAOTA_DOC}" >/dev/null 2>&1 || { echo "missing online install entry in baota doc"; exit 1; }
rg -n -F "./scripts/appctl prompt-db" "${BAOTA_DOC}" >/dev/null 2>&1 || { echo "missing prompt-db entry in baota doc"; exit 1; }
rg -n -F "./scripts/appctl install-link" "${BAOTA_DOC}" >/dev/null 2>&1 || { echo "missing install-link entry in baota doc"; exit 1; }
rg -n -F "127.0.0.1:8080" "${BAOTA_DOC}" >/dev/null 2>&1 || { echo "missing reverse proxy target in baota doc"; exit 1; }
rg -n -F "PostgreSQL" "${BAOTA_DOC}" >/dev/null 2>&1 || { echo "missing PostgreSQL note in baota doc"; exit 1; }

for file in \
  "${REPO_ROOT}/readme.md" \
  "${REPO_ROOT}/docs/pg-single-command.md" \
  "${REPO_ROOT}/docs/go-live-checklist.md"
do
  rg -n -F "docs/baota-go-single-machine.md" "${file}" >/dev/null 2>&1 || { echo "missing baota doc link in ${file}"; exit 1; }
done

rg -n -F "./scripts/appctl prompt-db" "${REPO_ROOT}/readme.md" >/dev/null 2>&1 || { echo "missing prompt-db entry in readme.md"; exit 1; }
rg -n -F "./scripts/appctl install-link" "${REPO_ROOT}/readme.md" >/dev/null 2>&1 || { echo "missing install-link entry in readme.md"; exit 1; }
rg -n -F "./scripts/appctl prompt-db" "${REPO_ROOT}/docs/pg-single-command.md" >/dev/null 2>&1 || { echo "missing prompt-db entry in docs/pg-single-command.md"; exit 1; }
rg -n -F "./scripts/appctl install-link" "${REPO_ROOT}/docs/pg-single-command.md" >/dev/null 2>&1 || { echo "missing install-link entry in docs/pg-single-command.md"; exit 1; }

echo "baota docs test passed"
