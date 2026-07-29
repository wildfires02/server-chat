#!/usr/bin/env bash

# 在本机启动三节点开发集群；生产故障认证使用 test-cluster-process.sh。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"
REPO_ROOT="$(im_repo_root "${SCRIPT_DIR}")"

SERVER="${IM_SERVER_BIN:-${REPO_ROOT}/bin/im-server}"
CONFIG_FILE="${IM_CLUSTER_DEV_CONFIG:-${REPO_ROOT}/configs/im.cluster-dev.yaml}"
STATIC_DIR="${IM_STATIC_DIR:-${REPO_ROOT}/web/static}"
RUN_DIR="${REPO_ROOT}/.local/run/cluster"
LOG_DIR="${REPO_ROOT}/.local/logs/cluster"
NODE_NAMES=(one two three)
HTTP_BASE_PORT=6060
CLIENT_GRPC_BASE_PORT=16060
CLUSTER_BASE_PORT=12000

usage() {
  echo "用法：$0 {start|stop|status}"
}

node_pid_file() {
  printf '%s/%s.pid\n' "${RUN_DIR}" "$1"
}

node_is_running() {
  local pid_file
  pid_file="$(node_pid_file "$1")"
  [[ -r "${pid_file}" ]] || return 1
  local process_id
  process_id="$(<"${pid_file}")"
  [[ "${process_id}" =~ ^[1-9][0-9]*$ ]] || return 1
  kill -0 "${process_id}" >/dev/null 2>&1
}

wait_livez() {
  local port=$1
  local deadline=$((SECONDS + 30))
  while ((SECONDS < deadline)); do
    if curl -fsS "http://127.0.0.1:${port}/livez" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

start_cluster() {
  im_require_command curl
  [[ -x "${SERVER}" ]] || im_die "服务端二进制不存在或不可执行：${SERVER}"
  [[ -r "${CONFIG_FILE}" ]] || im_die "配置文件不可读：${CONFIG_FILE}"
  curl -fsS http://127.0.0.1:2379/health >/dev/null ||
    im_die "本机 etcd 未就绪：http://127.0.0.1:2379"

  mkdir -p "${RUN_DIR}" "${LOG_DIR}"
  local index node_name process_id
  for index in "${!NODE_NAMES[@]}"; do
    node_name="${NODE_NAMES[index]}"
    if node_is_running "${node_name}"; then
      im_die "${node_name} 已经运行"
    fi
    IM_CLUSTER_CONFIG__SELF="${node_name}" \
    IM_CLUSTER_CONFIG__ADVERTISE_ADDR="127.0.0.1:$((CLUSTER_BASE_PORT + index))" \
    IM_CLUSTER_CONFIG__TRANSPORT__LISTEN="127.0.0.1:$((CLUSTER_BASE_PORT + index))" \
      "${SERVER}" \
        "--config=${CONFIG_FILE}" \
        "--listen=:$((HTTP_BASE_PORT + index))" \
        "--grpc_listen=:$((CLIENT_GRPC_BASE_PORT + index))" \
        "--static_data=${STATIC_DIR}" \
        >"${LOG_DIR}/${node_name}.log" 2>&1 &
    process_id=$!
    printf '%s\n' "${process_id}" >"$(node_pid_file "${node_name}")"
  done

  for index in "${!NODE_NAMES[@]}"; do
    if ! wait_livez "$((HTTP_BASE_PORT + index))"; then
      stop_cluster
      im_die "${NODE_NAMES[index]} 未在 30s 内通过 Liveness"
    fi
  done
  echo "开发集群已启动：HTTP 6060-6062，客户端 gRPC 16060-16062"
}

stop_cluster() {
  local index node_name pid_file process_id deadline
  mkdir -p "${RUN_DIR}"
  for index in "${!NODE_NAMES[@]}"; do
    node_name="${NODE_NAMES[index]}"
    pid_file="$(node_pid_file "${node_name}")"
    if ! node_is_running "${node_name}"; then
      rm -f "${pid_file}"
      continue
    fi
    process_id="$(<"${pid_file}")"
    curl -fsS -X POST "http://127.0.0.1:$((HTTP_BASE_PORT + index))/drainz" \
      >/dev/null 2>&1 || true
    kill -TERM "${process_id}"
    deadline=$((SECONDS + 30))
    while kill -0 "${process_id}" >/dev/null 2>&1 && ((SECONDS < deadline)); do
      sleep 1
    done
    if kill -0 "${process_id}" >/dev/null 2>&1; then
      kill -KILL "${process_id}"
    fi
    rm -f "${pid_file}"
  done
  echo "开发集群已停止"
}

show_status() {
  local node_name
  for node_name in "${NODE_NAMES[@]}"; do
    if node_is_running "${node_name}"; then
      echo "${node_name}: running"
    else
      echo "${node_name}: stopped"
    fi
  done
}

case "${1:-}" in
  start) start_cluster ;;
  stop) stop_cluster ;;
  status) show_status ;;
  *) usage; exit 1 ;;
esac
