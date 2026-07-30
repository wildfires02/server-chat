#!/bin/sh

# im-server 只读取 /etc/im/im.yaml；入口脚本只负责可选数据库任务。
# 数据库变更必须通过 IM_DB_INIT_MODE 显式选择，默认 skip。
set -eu

config_file="${IM_CONFIG_FILE:-/etc/im/im.yaml}"
init_mode="${IM_DB_INIT_MODE:-skip}"
wait_target="${IM_DB_WAIT_FOR:-}"
wait_timeout="${IM_DB_WAIT_TIMEOUT:-120}"
run_server="${IM_RUN_SERVER:-true}"

if [ ! -r "${config_file}" ]; then
  echo "配置文件不可读：${config_file}" >&2
  exit 1
fi

case "${wait_timeout}" in
  ''|*[!0-9]*)
    echo "IM_DB_WAIT_TIMEOUT 必须是非负整数秒" >&2
    exit 1
    ;;
esac

# wait_for_endpoint 在明确超时内等待依赖端口，避免容器永久卡在启动阶段。
wait_for_endpoint() {
  target=$1
  host=${target%:*}
  port=${target##*:}
  if [ -z "${host}" ] || [ -z "${port}" ] || [ "${host}" = "${target}" ]; then
    echo "IM_DB_WAIT_FOR 必须使用 host:port 格式，当前值=${target}" >&2
    exit 1
  fi

  started_at=$(date +%s)
  while ! nc -z -w 2 "${host}" "${port}" >/dev/null 2>&1; do
    now=$(date +%s)
    if [ $((now - started_at)) -ge "${wait_timeout}" ]; then
      echo "等待 ${target} 超过 ${wait_timeout}s" >&2
      exit 1
    fi
    echo "等待依赖 ${target}..."
    sleep 2
  done
}

if [ -n "${wait_target}" ]; then
  wait_for_endpoint "${wait_target}"
fi

config_arg="--config=${config_file}"
case "${init_mode}" in
  skip)
    ;;
  check)
    /usr/local/bin/init-db "${config_arg}" --no_init=true
    ;;
  init)
    if [ -n "${IM_DB_SAMPLE_DATA:-}" ]; then
      /usr/local/bin/init-db "${config_arg}" --data="${IM_DB_SAMPLE_DATA}"
    else
      /usr/local/bin/init-db "${config_arg}"
    fi
    ;;
  reset)
    if [ "${IM_ALLOW_DESTRUCTIVE_DB_RESET:-false}" != "true" ]; then
      echo "reset 需要同时设置 IM_ALLOW_DESTRUCTIVE_DB_RESET=true" >&2
      exit 1
    fi
    /usr/local/bin/init-db "${config_arg}" --reset=true --data="${IM_DB_SAMPLE_DATA:-}"
    ;;
  upgrade)
    /usr/local/bin/init-db "${config_arg}" --upgrade=true
    ;;
  *)
    echo "IM_DB_INIT_MODE 必须是 skip、check、init、reset 或 upgrade" >&2
    exit 1
    ;;
esac

if [ "${run_server}" != "true" ]; then
  exit 0
fi

# 使用 exec 让服务端直接接收 SIGTERM，并把日志交给容器运行时收集。
if [ "${config_file}" != "/etc/im/im.yaml" ]; then
  cp "${config_file}" /etc/im/im.yaml
fi
exec /usr/local/bin/im-server
