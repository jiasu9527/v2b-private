#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APPCTL_BIN="${APPCTL_BIN:-${ROOT_DIR}/scripts/appctl}"
LOG_PATH="${LOG_PATH:-${ROOT_DIR}/go-api/run/forest-go-api.log}"
PID_PATH="${PID_PATH:-${ROOT_DIR}/go-api/run/forest-go-api.pid}"
TAIL_BIN="${TAIL_BIN:-tail}"
PS_BIN="${PS_BIN:-ps}"
SS_BIN="${SS_BIN:-ss}"
LSOF_BIN="${LSOF_BIN:-lsof}"

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

print_header() {
  cat <<'TEXT'
==============================
 Forest 管理菜单
 Go + PostgreSQL 单机版
==============================
TEXT
}

show_main_menu() {
  print_header
  cat <<'TEXT'
1. 安装与更新
2. 数据库与迁移
3. 服务管理
4. 检查与日志
5. 数据与账号
6. 系统与环境
0. 退出
TEXT
}

show_install_menu() {
  cat <<'TEXT'

[安装与更新]
1. 一键安装
2. 一键更新
3. 仅构建二进制
4. 初始化环境文件
5. 配置 PostgreSQL
6. 迁移旧 PHP 配置
0. 返回上级
TEXT
}

show_database_menu() {
  cat <<'TEXT'

[数据库与迁移]
1. 迁移旧 PHP 配置
2. 迁移旧版 MySQL -> PostgreSQL
3. 强制全量覆盖 PostgreSQL
4. 数据库健康检查
5. 查看数据库配置摘要
6. 查看当前环境文件路径
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
5. 前台运行
6. 重新构建并重启
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

show_data_menu() {
  cat <<'TEXT'

[数据与账号]
1. 创建/重置管理员
2. 导入演示数据
3. 运行 API 验证脚本
4. 运行支付 API 验证脚本
5. 运行支付回调验证脚本
6. 清理旧版运行残留
0. 返回上级
TEXT
}

show_system_menu() {
  cat <<'TEXT'

[系统与环境]
1. 查看当前环境文件
2. 打印环境变量摘要
3. 生成 systemd 服务模板
4. 运行 Go 单元测试
5. 查看命令帮助
6. 打印运行路径摘要
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

show_env_summary() {
  local env_file=""
  env_file="$("${APPCTL_BIN}" env-file 2>/dev/null | tail -n 1)"
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
  while true; do
    show_install_menu
    case "$(prompt_choice)" in
      __EOF__|0) return 0 ;;
      1) run_appctl install; pause_screen ;;
      2) run_appctl update; pause_screen ;;
      3) run_appctl build; pause_screen ;;
      4) run_appctl init-env; pause_screen ;;
      5) run_appctl prompt-db; pause_screen ;;
      6) run_appctl migrate-config; pause_screen ;;
      *) echo "无效编号"; pause_screen ;;
    esac
  done
}

database_menu() {
  while true; do
    show_database_menu
    case "$(prompt_choice)" in
      __EOF__|0) return 0 ;;
      1) run_appctl migrate-config; pause_screen ;;
      2) run_appctl migrate-mysql; pause_screen ;;
      3) FORCE_MYSQL_OVERWRITE=1 run_appctl migrate-mysql; pause_screen ;;
      4) run_appctl doctor; pause_screen ;;
      5) show_env_summary; pause_screen ;;
      6) run_appctl env-file; pause_screen ;;
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
      5) run_appctl run; pause_screen ;;
      6) rebuild_and_restart; pause_screen ;;
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

data_menu() {
  while true; do
    show_data_menu
    case "$(prompt_choice)" in
      __EOF__|0) return 0 ;;
      1) run_appctl create-admin; pause_screen ;;
      2) run_appctl seed-demo; pause_screen ;;
      3) run_verify_script "${ROOT_DIR}/scripts/verify-demo-api.sh"; pause_screen ;;
      4) run_verify_script "${ROOT_DIR}/scripts/verify-demo-payment-api.sh"; pause_screen ;;
      5) run_verify_script "${ROOT_DIR}/scripts/verify-demo-payment-notify.sh"; pause_screen ;;
      6) run_appctl cleanup; pause_screen ;;
      *) echo "无效编号"; pause_screen ;;
    esac
  done
}

system_menu() {
  while true; do
    show_system_menu
    case "$(prompt_choice)" in
      __EOF__|0) return 0 ;;
      1) run_appctl env-file; pause_screen ;;
      2) show_env_summary; pause_screen ;;
      3) run_appctl service-template; pause_screen ;;
      4) run_appctl test; pause_screen ;;
      5) run_command "${APPCTL_BIN}"; pause_screen ;;
      6) show_runtime_paths; pause_screen ;;
      *) echo "无效编号"; pause_screen ;;
    esac
  done
}

main() {
  if [[ ! -x "${APPCTL_BIN}" ]]; then
    echo "未找到管理脚本: ${APPCTL_BIN}"
    exit 1
  fi

  while true; do
    show_main_menu
    case "$(prompt_choice)" in
      __EOF__|0) exit 0 ;;
      1) install_menu ;;
      2) database_menu ;;
      3) service_menu ;;
      4) observe_menu ;;
      5) data_menu ;;
      6) system_menu ;;
      *) echo "无效编号"; pause_screen ;;
    esac
  done
}

main "$@"
