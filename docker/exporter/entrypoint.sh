#!/bin/bash

# ==============================================================================
# IM 监控导出器 (Metrics Exporter) Docker 容器启动入口脚本
# 功能：验证环境变量，配置 InfluxDB/Prometheus 导出参数，等待服务器连通并启动 exporter
# ==============================================================================

# 检查必需的环境变量是否已设置
function check_vars() {
  local varnames=( "$@" )
  for varname in "${varnames[@]}"
  do
    eval value=\$${varname}
    if [ -z "$value" ] ; then
      echo "必须指定 $varname 环境变量。"
      exit 1
    fi
  done
}

# 获取 IM_ADDR
IM_ADDR="${IM_ADDR:-http://localhost:6060/stats/expvar}"
export IM_ADDR

# 确保系统在解析域名时使用 /etc/hosts（解决 docker-compose extra_hosts 域名解析问题）
echo "hosts: files dns" > /etc/nsswitch.conf

# 设置 exporter 监听端口
LISTEN_AT=":6222"

# 通用必需环境变量列表
common_vars=( IM_ADDR INSTANCE SERVE_FOR )

# InfluxDB 专属环境变量列表
influx_varnames=( INFLUXDB_VERSION INFLUXDB_ORGANIZATION INFLUXDB_PUSH_INTERVAL \
  INFLUXDB_PUSH_ADDRESS INFLUXDB_AUTH_TOKEN )

# Prometheus 专属环境变量列表
prometheus_varnames=( PROM_NAMESPACE PROM_METRICS_PATH )

# 检查通用环境变量
check_vars "${common_vars[@]}"

# 组装通用启动参数
args=("--im_addr=${IM_ADDR}" "--instance=${INSTANCE}" "--listen_at=${LISTEN_AT}" "--serve_for=${SERVE_FOR}")

# 根据 SERVE_FOR 决定启用 Prometheus 或 InfluxDB 模式
case "$SERVE_FOR" in
"prometheus")
  check_vars "${prometheus_varnames[@]}"
  args+=("--prom_namespace=${PROM_NAMESPACE}" "--prom_metrics_path=${PROM_METRICS_PATH}")
  if [ ! -z "$PROM_TIMEOUT" ]; then
    args+=("--prom_timeout=${PROM_TIMEOUT}")
  fi
  ;;
"influxdb")
  check_vars "${influx_varnames[@]}"
  args+=("--influx_db_version=${INFLUXDB_VERSION}" \
         "--influx_organization=${INFLUXDB_ORGANIZATION}" \
         "--influx_push_interval=${INFLUXDB_PUSH_INTERVAL}" \
         "--influx_push_addr=${INFLUXDB_PUSH_ADDRESS}" \
         "--influx_auth_token=${INFLUXDB_AUTH_TOKEN}")
  if [ ! -z "$INFLUXDB_BUCKET" ]; then
    args+=("--influx_bucket=${INFLUXDB_BUCKET}")
  fi
  ;;
*)
  echo "\$SERVE_FOR 必须设置为 'prometheus' 或 'influxdb'"
  exit 1
  ;;
esac

# 若配置了 WAIT_FOR 环境变量，等待 IM 目标服务器可达
if [ ! -z "$WAIT_FOR" ] ; then
	IFS=':' read -ra TND <<< "$WAIT_FOR"
	if [ ${#TND[@]} -ne 2 ]; then
		echo "\$WAIT_FOR (${WAIT_FOR}) 环境变量格式应为 HOST:PORT"
		exit 1
	fi
	until nc -z -v -w5 ${TND[0]} ${TND[1]}; do echo "正在等待 IM 服务器连通 ${WAIT_FOR}..."; sleep 5; done
fi

# 启动 exporter 程序
./exporter "${args[@]}"
