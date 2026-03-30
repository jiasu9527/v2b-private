#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

FILES=(
  "${REPO_ROOT}/readme.md"
  "${REPO_ROOT}/docs/go-api.md"
  "${REPO_ROOT}/docs/pg-single-command.md"
)

FORBIDDEN=(
  "php artisan"
  "pm2 + php"
  "pm2.yaml"
  "webman.php"
  "server.php"
  "start.php"
  "supervisor"
)

for file in "${FILES[@]}"; do
  if [[ ! -f "${file}" ]]; then
    echo "missing ${file}"
    exit 1
  fi

  for pattern in "${FORBIDDEN[@]}"; do
    if rg -n -F "${pattern}" "${file}" >/dev/null 2>&1; then
      echo "found legacy runtime hint '${pattern}' in ${file}"
      exit 1
    fi
  done
done

if ! rg -n -F "./init.sh" "${REPO_ROOT}/readme.md" >/dev/null 2>&1; then
  echo "missing one-line install entrypoint in readme.md"
  exit 1
fi

if ! rg -n -F "./update.sh" "${REPO_ROOT}/readme.md" >/dev/null 2>&1; then
  echo "missing one-line update entrypoint in readme.md"
  exit 1
fi

if ! rg -n -F "./init.sh" "${REPO_ROOT}/docs/pg-single-command.md" >/dev/null 2>&1; then
  echo "missing one-line install entrypoint in docs/pg-single-command.md"
  exit 1
fi

if ! rg -n -F "./update.sh" "${REPO_ROOT}/docs/pg-single-command.md" >/dev/null 2>&1; then
  echo "missing one-line update entrypoint in docs/pg-single-command.md"
  exit 1
fi

echo "legacy runtime hints test passed"
