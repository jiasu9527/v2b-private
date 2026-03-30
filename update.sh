#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LEGACY_BACKUP_DIR=""

backup_legacy_config() {
  local source_config="${ROOT_DIR}/config/v2board.php"
  local source_theme_dir="${ROOT_DIR}/config/theme"
  local copied=false

  LEGACY_BACKUP_DIR="$(mktemp -d "${ROOT_DIR}/.legacy-config-backup.XXXXXX")"
  mkdir -p "${LEGACY_BACKUP_DIR}/config/theme"

  if [[ -f "${source_config}" ]]; then
    cp "${source_config}" "${LEGACY_BACKUP_DIR}/config/v2board.php"
    copied=true
  fi

  if [[ -d "${source_theme_dir}" ]]; then
    while IFS= read -r file; do
      cp "${file}" "${LEGACY_BACKUP_DIR}/config/theme/"
      copied=true
    done < <(find "${source_theme_dir}" -maxdepth 1 -type f -name '*.php' | sort)
  fi

  if [[ "${copied}" != "true" ]]; then
    rm -rf "${LEGACY_BACKUP_DIR}"
    LEGACY_BACKUP_DIR=""
  fi
}

cleanup_legacy_backup() {
  if [[ -n "${LEGACY_BACKUP_DIR}" && -d "${LEGACY_BACKUP_DIR}" ]]; then
    rm -rf "${LEGACY_BACKUP_DIR}"
  fi
}

should_prompt_db_config() {
  if [[ "${FORCE_INTERACTIVE_DB_CONFIG:-0}" == "1" ]]; then
    return 0
  fi
  [[ -t 0 && -t 1 ]]
}

trap cleanup_legacy_backup EXIT

if [[ ! -d "${ROOT_DIR}/.git" ]]; then
  echo "请使用 Git 方式部署项目。"
  exit 1
fi

if ! command -v git >/dev/null 2>&1; then
  echo "未检测到 Git，请先安装 Git。"
  exit 1
fi

cd "${ROOT_DIR}"
backup_legacy_config

before_rev="$(git rev-parse HEAD 2>/dev/null || true)"
echo "正在同步代码..."
git config --global --add safe.directory "${ROOT_DIR}"
git fetch --all --prune --quiet
git pull --ff-only --quiet
after_rev="$(git rev-parse HEAD 2>/dev/null || true)"

if [[ -n "${before_rev}" && "${before_rev}" == "${after_rev}" ]]; then
  echo "代码已经是最新版本"
else
  echo "代码已同步到最新版本 ${after_rev:0:8}"
fi

if should_prompt_db_config; then
  "${ROOT_DIR}/scripts/appctl" prompt-db --optional
fi

if [[ -n "${LEGACY_BACKUP_DIR}" ]]; then
  export LEGACY_CONFIG_ROOT="${LEGACY_BACKUP_DIR}"
fi
"${ROOT_DIR}/scripts/appctl" update
"${ROOT_DIR}/scripts/appctl" restart
