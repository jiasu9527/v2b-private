#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

REQUIRED_FILES=(
  "${REPO_ROOT}/scripts/smoke-node-api.sh"
  "${REPO_ROOT}/docs/node-smoke.md"
  "${REPO_ROOT}/docs/go-live-checklist.md"
)

for file in "${REQUIRED_FILES[@]}"; do
  if [[ ! -f "${file}" ]]; then
    echo "missing ${file}"
    exit 1
  fi
done

bash -n "${REPO_ROOT}/scripts/smoke-node-api.sh"

if ! grep -Fq "./scripts/smoke-node-api.sh" "${REPO_ROOT}/readme.md"; then
  echo "missing smoke script entry in readme.md"
  exit 1
fi

if ! grep -Fq "./scripts/smoke-node-api.sh" "${REPO_ROOT}/docs/go-api.md"; then
  echo "missing smoke script entry in docs/go-api.md"
  exit 1
fi

for token in "BASE_URL" "SERVER_TOKEN" "NODE_ID" "NODE_TYPE" "LOCAL_PORT"; do
  if ! grep -Fq "${token}" "${REPO_ROOT}/docs/node-smoke.md"; then
    echo "missing ${token} in docs/node-smoke.md"
    exit 1
  fi
done

for command in "./scripts/appctl doctor" "./scripts/appctl service-template" "./scripts/smoke-node-api.sh"; do
  if ! grep -Fq "${command}" "${REPO_ROOT}/docs/go-live-checklist.md"; then
    echo "missing ${command} in docs/go-live-checklist.md"
    exit 1
  fi
done

echo "node ops assets test passed"
