#!/bin/sh

# 验证 Exporter 环境并以前台进程启动，日志由容器运行时收集。
set -eu

require_value() {
  name=$1
  value=$2
  if [ -z "${value}" ]; then
    echo "必须设置 ${name}" >&2
    exit 1
  fi
}

wait_for_endpoint() {
  target=$1
  timeout=${WAIT_TIMEOUT:-120}
  host=${target%:*}
  port=${target##*:}
  case "${timeout}" in
    ''|*[!0-9]*) echo "WAIT_TIMEOUT 必须是非负整数秒" >&2; exit 1 ;;
  esac
  if [ -z "${host}" ] || [ -z "${port}" ] || [ "${host}" = "${target}" ]; then
    echo "WAIT_FOR 必须使用 host:port 格式，当前值=${target}" >&2
    exit 1
  fi
  started_at=$(date +%s)
  while ! nc -z -w 2 "${host}" "${port}" >/dev/null 2>&1; do
    now=$(date +%s)
    if [ $((now - started_at)) -ge "${timeout}" ]; then
      echo "等待 ${target} 超过 ${timeout}s" >&2
      exit 1
    fi
    sleep 2
  done
}

require_value IM_ADDR "${IM_ADDR:-}"
require_value INSTANCE "${INSTANCE:-}"
require_value SERVE_FOR "${SERVE_FOR:-}"

if [ -n "${WAIT_FOR:-}" ]; then
  wait_for_endpoint "${WAIT_FOR}"
fi

set -- \
  "--im_addr=${IM_ADDR}" \
  "--instance=${INSTANCE}" \
  "--listen_at=:6222" \
  "--serve_for=${SERVE_FOR}"

case "${SERVE_FOR}" in
  prometheus)
    require_value PROM_NAMESPACE "${PROM_NAMESPACE:-}"
    require_value PROM_METRICS_PATH "${PROM_METRICS_PATH:-}"
    set -- "$@" \
      "--prom_namespace=${PROM_NAMESPACE}" \
      "--prom_metrics_path=${PROM_METRICS_PATH}"
    if [ -n "${PROM_TIMEOUT:-}" ]; then
      set -- "$@" "--prom_timeout=${PROM_TIMEOUT}"
    fi
    ;;
  influxdb)
    require_value INFLUXDB_VERSION "${INFLUXDB_VERSION:-}"
    require_value INFLUXDB_ORGANIZATION "${INFLUXDB_ORGANIZATION:-}"
    require_value INFLUXDB_PUSH_INTERVAL "${INFLUXDB_PUSH_INTERVAL:-}"
    require_value INFLUXDB_PUSH_ADDRESS "${INFLUXDB_PUSH_ADDRESS:-}"
    require_value INFLUXDB_AUTH_TOKEN "${INFLUXDB_AUTH_TOKEN:-}"
    set -- "$@" \
      "--influx_db_version=${INFLUXDB_VERSION}" \
      "--influx_organization=${INFLUXDB_ORGANIZATION}" \
      "--influx_push_interval=${INFLUXDB_PUSH_INTERVAL}" \
      "--influx_push_addr=${INFLUXDB_PUSH_ADDRESS}" \
      "--influx_auth_token=${INFLUXDB_AUTH_TOKEN}"
    if [ -n "${INFLUXDB_BUCKET:-}" ]; then
      set -- "$@" "--influx_bucket=${INFLUXDB_BUCKET}"
    fi
    ;;
  *)
    echo "SERVE_FOR 必须是 prometheus 或 influxdb" >&2
    exit 1
    ;;
esac

exec /usr/local/bin/im-exporter "$@"
