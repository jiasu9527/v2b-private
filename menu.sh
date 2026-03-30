#!/usr/bin/env bash
set -euo pipefail

resolve_script_path() {
  local source="${BASH_SOURCE[0]}"
  while [[ -L "${source}" ]]; do
    local dir
    dir="$(cd -P "$(dirname "${source}")" && pwd)"
    source="$(readlink "${source}")"
    [[ "${source}" != /* ]] && source="${dir}/${source}"
  done
  cd -P "$(dirname "${source}")" && pwd
}

ROOT_DIR="$(resolve_script_path)"
APPCTL_BIN="${APPCTL_BIN:-${ROOT_DIR}/scripts/appctl}"
LOG_PATH="${LOG_PATH:-${ROOT_DIR}/go-api/run/forest-go-api.log}"
PID_PATH="${PID_PATH:-${ROOT_DIR}/go-api/run/forest-go-api.pid}"
TAIL_BIN="${TAIL_BIN:-tail}"
PS_BIN="${PS_BIN:-ps}"
SS_BIN="${SS_BIN:-ss}"
LSOF_BIN="${LSOF_BIN:-lsof}"
GIT_BIN="${GIT_BIN:-git}"
COLOR_RESET=""
COLOR_BOLD=""
COLOR_DIM=""
COLOR_GREEN=""
COLOR_RED=""
COLOR_CYAN=""
RUNTIME_STATUS_LABEL="已停止"
RUNTIME_STATUS_COLOR=""
RUNTIME_PID="-"

normalize_choice() {
  printf '%s' "${1:-}" | tr -d '[:space:]'
}

pause_screen() {
  printf '\n按回车继续...'
  local _discard=""
  IFS= read -r _discard || true
}

run_command() {
  set +e
  "$@"
  local status=$?
  set -e
  if [[ ${status} -eq 130 ]]; then
    echo "已结束当前操作"
    return 0
  fi
  if [[ ${status} -ne 0 ]]; then
    echo "命令执行失败，退出码 ${status}"
  fi
  return 0
}

run_appctl() {
  run_command "${APPCTL_BIN}" "$@"
}

tool_available() {
  command -v "$1" >/dev/null 2>&1
}

supports_color() {
  [[ -t 1 || "${FORCE_COLOR:-0}" == "1" ]]
}

init_colors() {
  if ! supports_color; then
    return 0
  fi

  COLOR_RESET=$'\033[0m'
  COLOR_BOLD=$'\033[1m'
  COLOR_DIM=$'\033[2m'
  COLOR_GREEN=$'\033[32m'
  COLOR_RED=$'\033[31m'
  COLOR_CYAN=$'\033[36m'
}

detect_runtime_status() {
  RUNTIME_STATUS_LABEL="已停止"
  RUNTIME_STATUS_COLOR="${COLOR_RED}"
  RUNTIME_PID="-"

  local pid=""
  if [[ -f "${PID_PATH}" ]]; then
    pid="$(cat "${PID_PATH}" 2>/dev/null || true)"
    if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
      RUNTIME_STATUS_LABEL="运行中"
      RUNTIME_STATUS_COLOR="${COLOR_GREEN}"
      RUNTIME_PID="${pid}"
    fi
  fi
}

resolve_git_version() {
  if ! tool_available "${GIT_BIN}"; then
    printf '%s' "-"
    return 0
  fi

  local version=""
  version="$("${GIT_BIN}" -C "${ROOT_DIR}" rev-parse --short=7 HEAD 2>/dev/null | tail -n 1)"
  printf '%s' "${version:--}"
}

resolve_env_file_path() {
  local env_file=""
  env_file="$("${APPCTL_BIN}" env-file 2>/dev/null | tail -n 1)"
  printf '%s' "${env_file}"
}

resolve_env_file_name() {
  local env_file=""
  env_file="$(resolve_env_file_path)"
  if [[ -z "${env_file}" ]]; then
    printf '%s' "-"
    return 0
  fi
  printf '%s' "${env_file##*/}"
}

print_header() {
  printf '%s--------------------------------%s\n' "${COLOR_CYAN}" "${COLOR_RESET}"
  printf '%sForest 管理菜单%s\n' "${COLOR_BOLD}" "${COLOR_RESET}"
  printf '%sGo API + PostgreSQL 单机版%s\n' "${COLOR_DIM}" "${COLOR_RESET}"
  printf '%s--------------------------------%s\n' "${COLOR_CYAN}" "${COLOR_RESET}"
}

show_runtime_overview() {
  detect_runtime_status

  printf '服务: %s%s%s\n' "${RUNTIME_STATUS_COLOR}" "${RUNTIME_STATUS_LABEL}" "${COLOR_RESET}"
  printf 'PID: %s\n' "${RUNTIME_PID}"
  printf '版本: %s\n' "$(resolve_git_version)"
  printf '数据库: PostgreSQL\n'
  printf '环境文件: %s\n' "$(resolve_env_file_name)"
  printf '\n'
}

show_main_menu() {
  print_header
  show_runtime_overview
  cat <<'TEXT'
1. 服务管理
2. 安装更新
3. 检查日志
4. 高级操作
0. 退出
TEXT
}

show_install_menu() {
  cat <<'TEXT'

[安装更新]
1. 一键安装
2. 一键更新
3. 从旧项目一键迁移安装
4. 初始化环境文件
5. 配置 PostgreSQL
0. 返回上级
TEXT
}

show_service_menu() {
  cat <<'TEXT'

[服务管理]
1. 启动服务
2. 停止服务
3. 重启服务
4. 查看状态
5. 重新构建并重启
0. 返回上级
TEXT
}

show_observe_menu() {
  cat <<'TEXT'

[检查与日志]
1. 健康检查
2. 查看最新 200 行日志
3. 实时跟踪日志
4. 查看最近错误关键字
5. 查看进程详情
6. 查看监听端口
0. 返回上级
TEXT
}

show_advanced_menu() {
  cat <<'TEXT'

[高级操作]
1. 迁移旧 PHP 配置
2. 迁移旧版 MySQL -> PostgreSQL
3. 强制全量覆盖 PostgreSQL
4. 创建/重置管理员
5. 查看数据库配置摘要
6. 查看当前环境文件路径
7. 生成 systemd 服务模板
8. 安装全局 forest 命令
9. 安全卸载当前部署
0. 返回上级
TEXT
}

prompt_choice() {
  local choice=""
  printf '请输入编号: ' >&2
  if ! IFS= read -r choice; then
    printf '__EOF__'
    return 0
  fi
  normalize_choice "${choice}"
}

prompt_text() {
  local label="$1"
  local value=""
  printf '%s: ' "${label}" >&2
  if ! IFS= read -r value; then
    printf '__EOF__'
    return 0
  fi
  printf '%s' "${value}"
}

show_env_summary() {
  local env_file=""
  env_file="$(resolve_env_file_path)"
  if [[ -z "${env_file}" ]]; then
    echo "无法获取当前环境文件路径"
    return 0
  fi

  echo "当前环境文件: ${env_file}"
  if [[ ! -f "${env_file}" ]]; then
    echo "环境文件不存在"
    return 0
  fi

  awk -F= '
    /^(POSTGRES_DSN|DB_HOST|DB_PORT|DB_DATABASE|DB_USERNAME|DB_SSLMODE|ADMIN_EMAIL|APP_URL)=/ {
      print $0
    }
  ' "${env_file}"
}

show_runtime_paths() {
  echo "项目目录: ${ROOT_DIR}"
  echo "管理脚本: ${APPCTL_BIN}"
  echo "日志文件: ${LOG_PATH}"
  echo "PID 文件: ${PID_PATH}"
}

show_recent_log() {
  if [[ ! -f "${LOG_PATH}" ]]; then
    echo "日志文件不存在: ${LOG_PATH}"
    return 0
  fi
  run_command "${TAIL_BIN}" -n 200 "${LOG_PATH}"
}

follow_log() {
  if [[ ! -f "${LOG_PATH}" ]]; then
    echo "日志文件不存在: ${LOG_PATH}"
    return 0
  fi
  echo "按 Ctrl+C 结束实时跟踪"
  run_command "${TAIL_BIN}" -f "${LOG_PATH}"
}

show_recent_errors() {
  if [[ ! -f "${LOG_PATH}" ]]; then
    echo "日志文件不存在: ${LOG_PATH}"
    return 0
  fi
  if ! rg -n -i 'error|panic|fatal|fail' "${LOG_PATH}" | tail -n 50; then
    echo "最近日志里未匹配到 error/panic/fatal/fail"
  fi
}

show_process_details() {
  local status_output=""
  status_output="$("${APPCTL_BIN}" status 2>&1)"
  printf '%s\n' "${status_output}"
  local pid=""
  pid="$(printf '%s' "${status_output}" | sed -nE 's/.*PID[[:space:]]+([0-9]+).*/\1/p' | tail -n 1)"
  if [[ -z "${pid}" ]]; then
    echo "当前没有可用的 PID"
    return 0
  fi
  if tool_available "${PS_BIN}"; then
    run_command "${PS_BIN}" -fp "${pid}"
  else
    echo "未找到 ps 命令"
  fi
}

show_listen_ports() {
  if tool_available "${SS_BIN}"; then
    run_command "${SS_BIN}" -lntp
    return 0
  fi
  if tool_available "${LSOF_BIN}"; then
    run_command "${LSOF_BIN}" -iTCP -sTCP:LISTEN -n -P
    return 0
  fi
  echo "未找到 ss 或 lsof，无法查看监听端口"
}

run_verify_script() {
  local script_path="$1"
  if [[ ! -x "${script_path}" ]]; then
    echo "脚本不存在或不可执行: ${script_path}"
    return 0
  fi
  run_command "${script_path}"
}

restart_service() {
  run_appctl stop
  run_appctl start
}

rebuild_and_restart() {
  run_appctl build
  restart_service
}

install_menu() {
  local legacy_path=""
  while true; do
    show_install_menu
    case "$(prompt_choice)" in
      __EOF__|0) return 0 ;;
      1) run_appctl install; pause_screen ;;
      2) run_appctl update; pause_screen ;;
      3)
        legacy_path="$(prompt_text "请输入旧项目目录或旧版 .env 路径")"
        if [[ "${legacy_path}" == "__EOF__" ]]; then
          return 0
        fi
        if [[ -z "${legacy_path}" ]]; then
          echo "旧项目路径不能为空"
        else
          run_appctl install-legacy "${legacy_path}"
        fi
        pause_screen
        ;;
      4) run_appctl init-env; pause_screen ;;
      5) run_appctl prompt-db; pause_screen ;;
      *) echo "无效编号"; pause_screen ;;
    esac
  done
}

service_menu() {
  while true; do
    show_service_menu
    case "$(prompt_choice)" in
      __EOF__|0) return 0 ;;
      1) run_appctl start; pause_screen ;;
      2) run_appctl stop; pause_screen ;;
      3) restart_service; pause_screen ;;
      4) run_appctl status; pause_screen ;;
      5) rebuild_and_restart; pause_screen ;;
      *) echo "无效编号"; pause_screen ;;
    esac
  done
}

observe_menu() {
  while true; do
    show_observe_menu
    case "$(prompt_choice)" in
      __EOF__|0) return 0 ;;
      1) run_appctl doctor; pause_screen ;;
      2) show_recent_log; pause_screen ;;
      3) follow_log; pause_screen ;;
      4) show_recent_errors; pause_screen ;;
      5) show_process_details; pause_screen ;;
      6) show_listen_ports; pause_screen ;;
      *) echo "无效编号"; pause_screen ;;
    esac
  done
}

advanced_menu() {
  while true; do
    show_advanced_menu
    case "$(prompt_choice)" in
      __EOF__|0) return 0 ;;
      1) run_appctl migrate-config; pause_screen ;;
      2) run_appctl migrate-mysql; pause_screen ;;
      3) FORCE_MYSQL_OVERWRITE=1 run_appctl migrate-mysql; pause_screen ;;
      4) run_appctl create-admin; pause_screen ;;
      5) show_env_summary; pause_screen ;;
      6) run_appctl env-file; pause_screen ;;
      7) run_appctl service-template; pause_screen ;;
      8) run_appctl install-link; pause_screen ;;
      9) run_appctl uninstall; pause_screen ;;
      *) echo "无效编号"; pause_screen ;;
    esac
  done
}

main() {
  if [[ ! -x "${APPCTL_BIN}" ]]; then
    echo "未找到管理脚本: ${APPCTL_BIN}"
    exit 1
  fi

  if [[ $# -gt 0 ]]; then
    run_appctl "$@"
    exit 0
  fi

  init_colors

  while true; do
    show_main_menu
    case "$(prompt_choice)" in
      __EOF__|0) exit 0 ;;
      1) service_menu ;;
      2) install_menu ;;
      3) observe_menu ;;
      4) advanced_menu ;;
      *) echo "无效编号"; pause_screen ;;
    esac
  done
}

main "$@"
