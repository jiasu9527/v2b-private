#!/usr/bin/env bash
set -euo pipefail

DEFAULT_REPO_URL="${FOREST_REPO_URL:-https://github.com/jiasu9527/v2b-private.git}"
DEFAULT_BRANCH="${FOREST_BRANCH:-master}"

normalize_yes_no_answer() {
  printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]'
}

should_prompt() {
  [[ -t 0 && -t 1 ]]
}

prompt_with_default() {
  local label="$1"
  local default_value="$2"
  local value=""
  if [[ -n "${default_value}" ]]; then
    printf '%s [%s]: ' "${label}" "${default_value}" >&2
  else
    printf '%s: ' "${label}" >&2
  fi
  IFS= read -r value || true
  if [[ -z "${value}" ]]; then
    value="${default_value}"
  fi
  printf '%s\n' "${value}"
}

prompt_secret_with_default() {
  local label="$1"
  local default_value="$2"
  local value=""
  if [[ -n "${default_value}" ]]; then
    printf '%s [已保存]: ' "${label}" >&2
  else
    printf '%s: ' "${label}" >&2
  fi
  IFS= read -r -s value || true
  printf '\n' >&2
  if [[ -z "${value}" ]]; then
    value="${default_value}"
  fi
  printf '%s\n' "${value}"
}

default_install_dir() {
  if [[ -n "${FOREST_INSTALL_DIR:-}" ]]; then
    printf '%s\n' "${FOREST_INSTALL_DIR}"
    return 0
  fi
  if [[ -d "/www/wwwroot" && -w "/www/wwwroot" ]]; then
    printf '%s\n' "/www/wwwroot/forest"
    return 0
  fi
  if [[ -d "/opt" && -w "/opt" ]]; then
    printf '%s\n' "/opt/forest"
    return 0
  fi
  if [[ -n "${HOME:-}" ]]; then
    printf '%s\n' "${HOME}/forest"
    return 0
  fi
  printf '%s\n' "$(pwd)/forest"
}

set_env_var_in_path() {
  local env_path="$1"
  local key="$2"
  local value="$3"
  local tmp="${env_path}.tmp.$$"
  awk -v k="$key" -v v="$value" '
    BEGIN { done = 0 }
    $0 ~ ("^" k "=") { print k "=" v; done = 1; next }
    { print }
    END { if (!done) print k "=" v }
  ' "${env_path}" > "${tmp}"
  mv "${tmp}" "${env_path}"
}

env_value_from_path() {
  local env_path="$1"
  local target_key="$2"
  [[ -f "${env_path}" ]] || return 0
  while IFS= read -r line || [[ -n "${line}" ]]; do
    [[ -z "${line}" ]] && continue
    [[ "${line}" =~ ^[[:space:]]*# ]] && continue
    [[ "${line}" != *=* ]] && continue
    local key="${line%%=*}"
    local value="${line#*=}"
    key="$(printf '%s' "${key}" | tr -d '[:space:]')"
    value="${value%$'\r'}"
    if [[ "${key}" != "${target_key}" ]]; then
      continue
    fi
    if [[ "${value}" =~ ^\".*\"$ ]] || [[ "${value}" =~ ^\'.*\'$ ]]; then
      value="${value:1:${#value}-2}"
    fi
    printf '%s\n' "${value}"
    return 0
  done < "${env_path}"
}

ensure_git() {
  if ! command -v git >/dev/null 2>&1; then
    echo "未检测到 Git，请先安装 Git。" >&2
    exit 1
  fi
}

sync_repo() {
  local repo_url="$1"
  local install_dir="$2"
  local branch="$3"

  if [[ -d "${install_dir}/.git" ]]; then
    echo "检测到已有仓库，开始更新 ${install_dir}"
    git -C "${install_dir}" fetch --all --prune --quiet
    git -C "${install_dir}" pull --ff-only --quiet
    return 0
  fi

  if [[ -e "${install_dir}" && -n "$(find "${install_dir}" -mindepth 1 -maxdepth 1 2>/dev/null | head -n 1)" ]]; then
    echo "安装目录已存在且不是空目录：${install_dir}" >&2
    exit 1
  fi

  mkdir -p "${install_dir}"
  echo "开始拉取仓库到 ${install_dir}"
  git clone --branch "${branch}" --depth 1 "${repo_url}" "${install_dir}" >/dev/null 2>&1
}

prepare_env_file() {
  local install_dir="$1"
  local env_path=""
  local admin_email="${FOREST_ADMIN_EMAIL:-}"
  local admin_password="${FOREST_ADMIN_PASSWORD:-}"
  local current_admin=""
  local current_password=""
  local current_dsn=""

  (
    cd "${install_dir}"
    ./scripts/appctl init-env >/dev/null
    env_path="$(./scripts/appctl env-file | tail -n 1)"
    printf '%s\n' "${env_path}"
  )
}

configure_install_env() {
  local install_dir="$1"
  local env_path="$2"
  local admin_email="${FOREST_ADMIN_EMAIL:-}"
  local admin_password="${FOREST_ADMIN_PASSWORD:-}"
  local current_admin=""
  local current_password=""
  local current_dsn=""

  current_dsn="$(env_value_from_path "${env_path}" "POSTGRES_DSN")"
  if [[ -n "${FOREST_POSTGRES_DSN:-}" ]]; then
    set_env_var_in_path "${env_path}" "POSTGRES_DSN" "${FOREST_POSTGRES_DSN}"
  else
    if [[ -n "${FOREST_DB_HOST:-}" ]]; then
      set_env_var_in_path "${env_path}" "DB_HOST" "${FOREST_DB_HOST}"
    fi
    if [[ -n "${FOREST_DB_PORT:-}" ]]; then
      set_env_var_in_path "${env_path}" "DB_PORT" "${FOREST_DB_PORT}"
    fi
    if [[ -n "${FOREST_DB_DATABASE:-}" ]]; then
      set_env_var_in_path "${env_path}" "DB_DATABASE" "${FOREST_DB_DATABASE}"
    fi
    if [[ -n "${FOREST_DB_USERNAME:-}" ]]; then
      set_env_var_in_path "${env_path}" "DB_USERNAME" "${FOREST_DB_USERNAME}"
    fi
    if [[ -n "${FOREST_DB_PASSWORD:-}" ]]; then
      set_env_var_in_path "${env_path}" "DB_PASSWORD" "${FOREST_DB_PASSWORD}"
    fi
    if [[ -n "${FOREST_DB_SSLMODE:-}" ]]; then
      set_env_var_in_path "${env_path}" "DB_SSLMODE" "${FOREST_DB_SSLMODE}"
    fi
    if [[ -z "${current_dsn}" && -z "${FOREST_DB_DATABASE:-}" && should_prompt ]]; then
      (
        cd "${install_dir}"
        ./scripts/appctl prompt-db
      )
    fi
  fi

  current_admin="$(env_value_from_path "${env_path}" "ADMIN_EMAIL")"
  current_password="$(env_value_from_path "${env_path}" "ADMIN_PASSWORD")"

  if [[ -z "${admin_email}" && should_prompt ]]; then
    if [[ "${current_admin}" == "admin@example.com" ]]; then
      current_admin=""
    fi
    admin_email="$(prompt_with_default "管理员邮箱 (ADMIN_EMAIL)" "${current_admin}")"
  fi
  if [[ -n "${admin_email}" ]]; then
    set_env_var_in_path "${env_path}" "ADMIN_EMAIL" "${admin_email}"
  fi

  if [[ -z "${admin_password}" && should_prompt ]]; then
    admin_password="$(prompt_secret_with_default "管理员密码 (ADMIN_PASSWORD，可留空自动生成)" "${current_password}")"
  fi
  if [[ -n "${admin_password}" ]]; then
    set_env_var_in_path "${env_path}" "ADMIN_PASSWORD" "${admin_password}"
  fi
}

install_global_forest() {
  local install_dir="$1"
  local enabled="${FOREST_INSTALL_LINK:-1}"
  local normalized
  normalized="$(normalize_yes_no_answer "${enabled}")"
  if [[ "${normalized}" == "0" || "${normalized}" == "false" || "${normalized}" == "n" || "${normalized}" == "no" ]]; then
    return 0
  fi

  if ! (
    cd "${install_dir}"
    ./scripts/appctl install-link
  ); then
    echo "警告：全局 forest 命令安装失败，可稍后手动执行 ./scripts/appctl install-link" >&2
  fi
}

resolve_legacy_root() {
  if [[ -n "${FOREST_LEGACY_ROOT:-}" ]]; then
    printf '%s\n' "${FOREST_LEGACY_ROOT}"
    return 0
  fi
  if should_prompt; then
    prompt_with_default "旧项目目录或旧版 .env 路径（留空表示全新安装）" ""
    return 0
  fi
  printf '\n'
}

run_install() {
  local install_dir="$1"
  local legacy_root="$2"
  if [[ -n "${legacy_root}" ]]; then
    (
      cd "${install_dir}"
      ./scripts/appctl install-legacy "${legacy_root}"
    )
    return 0
  fi

  (
    cd "${install_dir}"
    ./scripts/appctl install
  )
}

main() {
  local repo_url="${DEFAULT_REPO_URL}"
  local branch="${DEFAULT_BRANCH}"
  local install_dir
  local env_path=""
  local legacy_root=""

  ensure_git

  install_dir="$(default_install_dir)"
  if [[ -z "${FOREST_INSTALL_DIR:-}" && should_prompt ]]; then
    install_dir="$(prompt_with_default "安装目录" "${install_dir}")"
  fi
  legacy_root="$(resolve_legacy_root)"

  sync_repo "${repo_url}" "${install_dir}" "${branch}"
  chmod +x "${install_dir}/scripts/appctl" "${install_dir}/menu.sh" 2>/dev/null || true

  env_path="$(prepare_env_file "${install_dir}")"
  configure_install_env "${install_dir}" "${env_path}"
  install_global_forest "${install_dir}"
  run_install "${install_dir}" "${legacy_root}"

  cat <<EOF
安装完成
项目目录: ${install_dir}
环境文件: ${env_path}
后台主配置: ${install_dir}/config/admin.json
主题配置目录: ${install_dir}/config/theme
日志文件: ${install_dir}/go-api/run/forest-go-api.log
EOF
}

main "$@"
