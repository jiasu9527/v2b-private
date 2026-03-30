#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_DOC="${REPO_ROOT}/docs/install.md"
UPDATE_DOC="${REPO_ROOT}/docs/update.md"

[[ -f "${INSTALL_DOC}" ]] || { echo "missing ${INSTALL_DOC}"; exit 1; }
[[ -f "${UPDATE_DOC}" ]] || { echo "missing ${UPDATE_DOC}"; exit 1; }

rg -n -F "./init.sh" "${INSTALL_DOC}" >/dev/null 2>&1 || { echo "missing init entry in install doc"; exit 1; }
rg -n -F "./scripts/appctl install-link" "${INSTALL_DOC}" >/dev/null 2>&1 || { echo "missing install-link entry in install doc"; exit 1; }
rg -n -F "PostgreSQL" "${INSTALL_DOC}" >/dev/null 2>&1 || { echo "missing PostgreSQL note in install doc"; exit 1; }
rg -n -F "./update.sh" "${UPDATE_DOC}" >/dev/null 2>&1 || { echo "missing update entry in update doc"; exit 1; }
rg -n -F "./scripts/appctl prompt-db" "${UPDATE_DOC}" >/dev/null 2>&1 || { echo "missing prompt-db entry in update doc"; exit 1; }
rg -n -F "./scripts/appctl install-link" "${UPDATE_DOC}" >/dev/null 2>&1 || { echo "missing install-link entry in update doc"; exit 1; }
rg -n -F "./scripts/appctl migrate-mysql" "${UPDATE_DOC}" >/dev/null 2>&1 || { echo "missing migrate-mysql entry in update doc"; exit 1; }

for file in \
  "${REPO_ROOT}/readme.md" \
  "${REPO_ROOT}/docs/baota-go-single-machine.md" \
  "${REPO_ROOT}/docs/pg-single-command.md"
do
  rg -n -F "docs/install.md" "${file}" >/dev/null 2>&1 || { echo "missing install doc link in ${file}"; exit 1; }
  rg -n -F "docs/update.md" "${file}" >/dev/null 2>&1 || { echo "missing update doc link in ${file}"; exit 1; }
done

echo "install/update docs test passed"
