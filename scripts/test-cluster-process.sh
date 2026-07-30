#!/usr/bin/env bash

# 在隔离 MySQL、三节点 etcd 和五个真实 IM 进程上执行生产集群故障与容量认证。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
TEMPLATE_FILE="${REPO_ROOT}/tests/cluster/config.template.yaml"
DATA_FILE="${REPO_ROOT}/tests/cluster/data.json"
ETCD_IMAGE="${IM_CLUSTER_E2E_ETCD_IMAGE:-gcr.io/etcd-development/etcd:v3.7.1}"
MYSQL_IMAGE="${IM_CLUSTER_E2E_MYSQL_IMAGE:-mysql:8.0}"
HTTP_BASE_PORT="${IM_CLUSTER_E2E_HTTP_BASE_PORT:-27060}"
CLIENT_GRPC_BASE_PORT="${IM_CLUSTER_E2E_CLIENT_GRPC_BASE_PORT:-27160}"
CLUSTER_BASE_PORT="${IM_CLUSTER_E2E_CLUSTER_BASE_PORT:-27260}"
HOT_RECEIVERS="${IM_CLUSTER_E2E_HOT_RECEIVERS:-32}"
HOT_MESSAGES="${IM_CLUSTER_E2E_HOT_MESSAGES:-300}"
RECONNECTS="${IM_CLUSTER_E2E_RECONNECTS:-256}"
MYSQL_PASSWORD="cluster-e2e-123"
RUN_ID="$$"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/im-cluster-e2e.XXXXXX")"
CONFIG_FILE="${WORK_DIR}/im.cluster.yaml"
SERVER_BIN="${WORK_DIR}/im-server"
INIT_BIN="${WORK_DIR}/init-db"
STATE_FILE="${WORK_DIR}/process-state.json"
DOCKER_NETWORK="im-cluster-e2e-${RUN_ID}"
MYSQL_CONTAINER="im-cluster-e2e-mysql-${RUN_ID}"
ETCD_CONTAINERS=(
  "im-cluster-e2e-etcd-0-${RUN_ID}"
  "im-cluster-e2e-etcd-1-${RUN_ID}"
  "im-cluster-e2e-etcd-2-${RUN_ID}"
)
SERVER_PIDS=("" "" "" "" "")
SERVER_LOGS=("" "" "" "" "")
ETCD_PORTS=("" "" "")
HTTP_PORTS=("" "" "" "" "")
CLIENT_GRPC_PORTS=("" "" "" "" "")
CLUSTER_PORTS=("" "" "" "" "")
PAUSED_ETCD=0
STOPPED_NODE=-1
REPORT_DIR="${REPO_ROOT}/test-results"
REPORT_FILE="${IM_CLUSTER_CERTIFICATION_REPORT:-${REPORT_DIR}/cluster-certification-latest.md}"
STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# cleanup 只处理本脚本创建的显式 PID、容器、网络和 mktemp 目录。
cleanup() {
  local exit_code=$?
  local index
  set +e
  if ((PAUSED_ETCD == 1)); then
    docker unpause "${ETCD_CONTAINERS[1]}" "${ETCD_CONTAINERS[2]}" >/dev/null 2>&1
  fi
  if ((STOPPED_NODE >= 0)) && [[ -n "${SERVER_PIDS[STOPPED_NODE]}" ]]; then
    kill -CONT "${SERVER_PIDS[STOPPED_NODE]}" >/dev/null 2>&1
  fi
  for index in 0 1 2 3 4; do
    if [[ -n "${SERVER_PIDS[index]}" ]] &&
      kill -0 "${SERVER_PIDS[index]}" >/dev/null 2>&1; then
      kill -TERM "${SERVER_PIDS[index]}" >/dev/null 2>&1
      wait "${SERVER_PIDS[index]}" >/dev/null 2>&1
    fi
  done
  docker rm -f "${MYSQL_CONTAINER}" "${ETCD_CONTAINERS[@]}" >/dev/null 2>&1
  docker network rm "${DOCKER_NETWORK}" >/dev/null 2>&1
  if ((exit_code != 0)); then
    echo "集群进程认证失败，服务日志保存在 ${WORK_DIR}" >&2
    for index in 0 1 2 3 4; do
      if [[ -f "${SERVER_LOGS[index]}" ]]; then
        echo "im-${index} 最后 80 行日志：" >&2
        tail -n 80 "${SERVER_LOGS[index]}" >&2
      fi
    done
  else
    rm -rf "${WORK_DIR}"
  fi
  exit "${exit_code}"
}
trap cleanup EXIT

# require_positive_integer 防止端口和负载参数被解释成 shell 片段。
require_positive_integer() {
  local name=$1
  local value=$2
  if [[ ! "${value}" =~ ^[1-9][0-9]*$ ]]; then
    echo "${name} 必须是正整数，当前值=${value}" >&2
    exit 1
  fi
}

# wait_http_status 等待指定节点健康接口达到期望状态码。
wait_http_status() {
  local node_index=$1
  local path=$2
  local expected=$3
  local timeout_seconds=$4
  local deadline=$((SECONDS + timeout_seconds))
  local actual="000"
  while ((SECONDS < deadline)); do
    if [[ -n "${SERVER_PIDS[node_index]}" ]] &&
      ! kill -0 "${SERVER_PIDS[node_index]}" >/dev/null 2>&1; then
      echo "im-${node_index} 在等待 ${path} 时提前退出" >&2
      return 1
    fi
    actual="$(curl -sS -o /dev/null -w '%{http_code}' \
      "http://127.0.0.1:${HTTP_PORTS[node_index]}${path}" || true)"
    if [[ "${actual}" == "${expected}" ]]; then
      return 0
    fi
    sleep 1
  done
  echo "im-${node_index}${path} 在 ${timeout_seconds}s 内未达到 ${expected}，最后=${actual}" >&2
  return 1
}

# read_cluster_epoch 从 Readiness JSON 中提取已经提交的数据库 fence epoch。
read_cluster_epoch() {
  local node_index=$1
  curl -fsS "http://127.0.0.1:${HTTP_PORTS[node_index]}/readyz" |
    sed -E 's/.*"cluster_epoch":([0-9]+).*/\1/'
}

# wait_new_epoch 要求存活节点在 RTO 门限内应用比故障前更大的视图。
wait_new_epoch() {
  local node_index=$1
  local previous_epoch=$2
  local deadline=$((SECONDS + 15))
  local current_epoch=0
  while ((SECONDS < deadline)); do
    current_epoch="$(read_cluster_epoch "${node_index}" 2>/dev/null || echo 0)"
    if [[ "${current_epoch}" =~ ^[0-9]+$ ]] &&
      ((current_epoch > previous_epoch)); then
      return 0
    fi
    sleep 1
  done
  echo "im-${node_index} 未在 15s RTO 内推进 Cluster View" >&2
  return 1
}

# start_node 使用同一不可变候选拓扑和节点专属 mTLS 身份启动一个进程。
start_node() {
  local node_index=$1
  local node_name="im-${node_index}"
  local node_dir="${WORK_DIR}/${node_name}"
  local node_config="${node_dir}/configs/im.yaml"
  local log_file="${WORK_DIR}/${node_name}.log"
  SERVER_LOGS[node_index]="${log_file}"
  mkdir -p "${node_dir}/configs"
  sed \
    -e "s|^listen:.*|listen: \"127.0.0.1:${HTTP_PORTS[node_index]}\"|" \
    -e "s|^grpc_listen:.*|grpc_listen: \"127.0.0.1:${CLIENT_GRPC_PORTS[node_index]}\"|" \
    -e "s|^  self:.*|  self: ${node_name}|" \
    -e "s|^  advertise_addr:.*|  advertise_addr: \"127.0.0.1:${CLUSTER_PORTS[node_index]}\"|" \
    -e "s|^    listen:.*|    listen: \"127.0.0.1:${CLUSTER_PORTS[node_index]}\"|" \
    -e "s|${WORK_DIR}/pki/im-0.pem|${WORK_DIR}/pki/${node_name}.pem|" \
    -e "s|${WORK_DIR}/pki/im-0-key.pem|${WORK_DIR}/pki/${node_name}-key.pem|" \
    "${CONFIG_FILE}" >"${node_config}"
  (
    cd "${node_dir}"
    exec "${SERVER_BIN}"
  ) >"${log_file}" 2>&1 &
  SERVER_PIDS[node_index]=$!
  wait_http_status "${node_index}" /livez 200 45
}

# stop_node 要求真实进程在 SIGTERM 后完成 Drain 并正常退出。
stop_node() {
  local node_index=$1
  local process_status=0
  local deadline
  if [[ -z "${SERVER_PIDS[node_index]}" ]]; then
    return 0
  fi
  kill -TERM "${SERVER_PIDS[node_index]}"
  deadline=$((SECONDS + 30))
  while kill -0 "${SERVER_PIDS[node_index]}" >/dev/null 2>&1 &&
    ((SECONDS < deadline)); do
    sleep 1
  done
  if kill -0 "${SERVER_PIDS[node_index]}" >/dev/null 2>&1; then
    echo "im-${node_index} 收到 SIGTERM 后 30s 内没有退出" >&2
    return 1
  fi
  wait "${SERVER_PIDS[node_index]}" || process_status=$?
  SERVER_PIDS[node_index]=""
  if ((process_status != 0)); then
    echo "im-${node_index} 优雅退出码=${process_status}" >&2
    return 1
  fi
}

# issue_certificate 使用测试 CA 签发带精确 DNS SAN 的双用途节点证书。
issue_certificate() {
  local common_name=$1
  local ca_prefix=$2
  local output_prefix=$3
  local san=$4
  local extension_file="${WORK_DIR}/pki/${output_prefix}.ext"
  openssl req -newkey rsa:2048 -nodes \
    -keyout "${WORK_DIR}/pki/${output_prefix}-key.pem" \
    -out "${WORK_DIR}/pki/${output_prefix}.csr" \
    -subj "/CN=${common_name}" >/dev/null 2>&1
  printf '%s\n' \
    "subjectAltName=${san}" \
    "extendedKeyUsage=serverAuth,clientAuth" >"${extension_file}"
  openssl x509 -req \
    -in "${WORK_DIR}/pki/${output_prefix}.csr" \
    -CA "${WORK_DIR}/pki/${ca_prefix}-ca.pem" \
    -CAkey "${WORK_DIR}/pki/${ca_prefix}-ca-key.pem" \
    -CAserial "${WORK_DIR}/pki/${ca_prefix}-ca.srl" \
    -out "${WORK_DIR}/pki/${output_prefix}.pem" \
    -days 2 -sha256 -extfile "${extension_file}" >/dev/null 2>&1
}

for value_name in \
  HTTP_BASE_PORT CLIENT_GRPC_BASE_PORT CLUSTER_BASE_PORT \
  HOT_RECEIVERS HOT_MESSAGES RECONNECTS; do
  require_positive_integer "${value_name}" "${!value_name}"
done
for index in 0 1 2 3 4; do
  HTTP_PORTS[index]=$((HTTP_BASE_PORT + index))
  CLIENT_GRPC_PORTS[index]=$((CLIENT_GRPC_BASE_PORT + index))
  CLUSTER_PORTS[index]=$((CLUSTER_BASE_PORT + index))
done

for command_name in docker curl openssl envsubst go; do
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "缺少集群进程认证依赖：${command_name}" >&2
    exit 1
  fi
done
if ! docker info >/dev/null 2>&1; then
  echo "Docker daemon 不可用" >&2
  exit 1
fi
if command -v lsof >/dev/null 2>&1; then
  for port in "${HTTP_PORTS[@]}" "${CLIENT_GRPC_PORTS[@]}" "${CLUSTER_PORTS[@]}"; do
    if lsof -n -iTCP:"${port}" -sTCP:LISTEN >/dev/null 2>&1; then
      echo "集群测试端口 ${port} 已被占用" >&2
      exit 1
    fi
  done
fi

mkdir -p "${WORK_DIR}/pki" "${REPORT_DIR}"
cd "${REPO_ROOT}"

# 生成彼此独立的 etcd CA 和 IM 数据面 CA，避免测试掩盖信任域配置错误。
for ca_prefix in etcd cluster; do
  openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout "${WORK_DIR}/pki/${ca_prefix}-ca-key.pem" \
    -out "${WORK_DIR}/pki/${ca_prefix}-ca.pem" \
    -days 2 -sha256 -subj "/CN=im-${ca_prefix}-test-ca" >/dev/null 2>&1
  # 后续签发统一使用显式 serial 文件，避免并发证书签发竞争。
  printf '%s\n' 01 >"${WORK_DIR}/pki/${ca_prefix}-ca.srl"
done
issue_certificate \
  etcd etcd etcd-server \
  "DNS:etcd,DNS:etcd-0,DNS:etcd-1,DNS:etcd-2,IP:127.0.0.1"
issue_certificate etcd-client etcd etcd-client "DNS:etcd-client"
for index in 0 1 2 3 4; do
  issue_certificate "im-${index}" cluster "im-${index}" "DNS:im-${index}"
done

docker network create "${DOCKER_NETWORK}" >/dev/null
if ! docker image inspect "${ETCD_IMAGE}" >/dev/null 2>&1; then
  docker pull "${ETCD_IMAGE}"
fi
if ! docker image inspect "${MYSQL_IMAGE}" >/dev/null 2>&1; then
  docker pull "${MYSQL_IMAGE}"
fi

# 三个 etcd 成员使用独立数据目录和 Peer，客户端侧强制双向 TLS。
for index in 0 1 2; do
  docker run -d \
    --name "${ETCD_CONTAINERS[index]}" \
    --network "${DOCKER_NETWORK}" \
    --network-alias "etcd-${index}" \
    --tmpfs /etcd-data:rw,nosuid,size=256m \
    -v "${WORK_DIR}/pki:/certs:ro" \
    -p 127.0.0.1::2379 \
    "${ETCD_IMAGE}" \
    /usr/local/bin/etcd \
    --name "etcd-${index}" \
    --data-dir /etcd-data \
    --listen-client-urls https://0.0.0.0:2379 \
    --advertise-client-urls "https://etcd-${index}:2379" \
    --client-cert-auth=true \
    --trusted-ca-file=/certs/etcd-ca.pem \
    --cert-file=/certs/etcd-server.pem \
    --key-file=/certs/etcd-server-key.pem \
    --listen-peer-urls http://0.0.0.0:2380 \
    --initial-advertise-peer-urls "http://etcd-${index}:2380" \
    --initial-cluster \
      "etcd-0=http://etcd-0:2380,etcd-1=http://etcd-1:2380,etcd-2=http://etcd-2:2380" \
    --initial-cluster-state new \
    --initial-cluster-token "im-cluster-e2e-${RUN_ID}" >/dev/null
  mapped_port="$(docker port "${ETCD_CONTAINERS[index]}" 2379/tcp | tail -n 1)"
  ETCD_PORTS[index]="${mapped_port##*:}"
  require_positive_integer "etcd-${index} 映射端口" "${ETCD_PORTS[index]}"
done

for index in 0 1 2; do
  deadline=$((SECONDS + 60))
  until curl -fsS \
    --cacert "${WORK_DIR}/pki/etcd-ca.pem" \
    --cert "${WORK_DIR}/pki/etcd-client.pem" \
    --key "${WORK_DIR}/pki/etcd-client-key.pem" \
    "https://127.0.0.1:${ETCD_PORTS[index]}/health" >/dev/null 2>&1; do
    if ((SECONDS >= deadline)); then
      echo "etcd-${index} 在 60s 内没有形成健康多数派" >&2
      exit 1
    fi
    sleep 1
  done
done

docker run -d \
  --name "${MYSQL_CONTAINER}" \
  --tmpfs /var/lib/mysql:rw,nosuid,size=1g \
  -p 127.0.0.1::3306 \
  -e "MYSQL_ROOT_PASSWORD=${MYSQL_PASSWORD}" \
  "${MYSQL_IMAGE}" >/dev/null
mysql_deadline=$((SECONDS + 90))
until docker exec "${MYSQL_CONTAINER}" \
  mysqladmin ping -uroot "-p${MYSQL_PASSWORD}" --silent >/dev/null 2>&1; do
  if ((SECONDS >= mysql_deadline)); then
    echo "MySQL 在 90s 内没有就绪" >&2
    exit 1
  fi
  sleep 1
done
mapped_mysql_port="$(docker port "${MYSQL_CONTAINER}" 3306/tcp | tail -n 1)"
MYSQL_PORT="${mapped_mysql_port##*:}"
require_positive_integer "MySQL 映射端口" "${MYSQL_PORT}"

# envsubst 只接收脚本生成的端口和 mktemp 路径，不读取任意用户模板内容。
export TEST_WORK_DIR="${WORK_DIR}" MYSQL_PASSWORD MYSQL_PORT
export IM0_HTTP_PORT="${HTTP_PORTS[0]}"
export IM0_CLIENT_GRPC_PORT="${CLIENT_GRPC_PORTS[0]}"
export IM0_CLUSTER_PORT="${CLUSTER_PORTS[0]}"
export IM1_CLUSTER_PORT="${CLUSTER_PORTS[1]}"
export IM2_CLUSTER_PORT="${CLUSTER_PORTS[2]}"
export IM3_CLUSTER_PORT="${CLUSTER_PORTS[3]}"
export IM4_CLUSTER_PORT="${CLUSTER_PORTS[4]}"
export ETCD0_PORT="${ETCD_PORTS[0]}"
export ETCD1_PORT="${ETCD_PORTS[1]}"
export ETCD2_PORT="${ETCD_PORTS[2]}"
envsubst \
  '${TEST_WORK_DIR} ${MYSQL_PASSWORD} ${MYSQL_PORT} ${IM0_HTTP_PORT} '\
'${IM0_CLIENT_GRPC_PORT} ${IM0_CLUSTER_PORT} ${IM1_CLUSTER_PORT} '\
'${IM2_CLUSTER_PORT} ${IM3_CLUSTER_PORT} ${IM4_CLUSTER_PORT} '\
'${ETCD0_PORT} ${ETCD1_PORT} ${ETCD2_PORT}' \
  <"${TEMPLATE_FILE}" >"${CONFIG_FILE}"

go build -tags mysql -o "${SERVER_BIN}" ./cmd/im-server
go build -tags mysql -o "${INIT_BIN}" ./cmd/init-db
SERVER_SHA256="$(openssl dgst -sha256 "${SERVER_BIN}" | sed -E 's/^.*= //')"
SOURCE_COMMIT="$(git rev-parse --verify HEAD 2>/dev/null || echo unknown)"
SOURCE_DIRTY="no"
if [[ -n "$(git status --porcelain --untracked-files=normal 2>/dev/null)" ]]; then
  SOURCE_DIRTY="yes"
fi
"${INIT_BIN}" --config="${CONFIG_FILE}" --data="${DATA_FILE}" --reset=true

# 三节点冷启动必须在达到多数派前 Not Ready，达到多数派后全部接流。
start_node 0
start_node 1
start_node 2
for index in 0 1 2; do
  wait_http_status "${index}" /readyz 200 45
done

export IM_TEST_CLUSTER_NODE0_HTTP="http://127.0.0.1:${HTTP_PORTS[0]}"
export IM_TEST_CLUSTER_NODE1_HTTP="http://127.0.0.1:${HTTP_PORTS[1]}"
export IM_TEST_CLUSTER_NODE2_HTTP="http://127.0.0.1:${HTTP_PORTS[2]}"
export IM_TEST_CLUSTER_STATE="${STATE_FILE}"
go test ./tests/cluster -run '^TestClusterCrossNodeRouting$' -count=1 -v

# 终止真实远端 Owner，要求剩余多数派在 15s 内推进 fence 和 Ring，随后验证 RPO=0。
OWNER="$(sed -E 's/.*"owner":"([^"]+)".*/\1/' "${STATE_FILE}")"
OWNER_INDEX="${OWNER#im-}"
require_positive_integer "故障 Owner 下标" "${OWNER_INDEX}"
if ((OWNER_INDEX < 1 || OWNER_INDEX > 2)); then
  echo "黑盒测试返回的 Owner=${OWNER}，期望 im-1 或 im-2" >&2
  exit 1
fi
SURVIVOR_INDEX=1
if ((OWNER_INDEX == 1)); then
  SURVIVOR_INDEX=2
fi
PREVIOUS_EPOCH="$(read_cluster_epoch 0)"
FAILOVER_STARTED="$(date +%s)"
kill -KILL "${SERVER_PIDS[OWNER_INDEX]}"
wait "${SERVER_PIDS[OWNER_INDEX]}" >/dev/null 2>&1 || true
SERVER_PIDS[OWNER_INDEX]=""
wait_new_epoch 0 "${PREVIOUS_EPOCH}"
wait_new_epoch "${SURVIVOR_INDEX}" "${PREVIOUS_EPOCH}"
FAILOVER_RTO=$(( $(date +%s) - FAILOVER_STARTED ))
go test ./tests/cluster -run '^TestClusterFailoverRecovery$' -count=1 -v
start_node "${OWNER_INDEX}"
for index in 0 1 2; do
  wait_http_status "${index}" /readyz 200 45
done

# 候选节点先 Joining/Not Ready，只有 etcd CAS 提交 3→5 后才进入 Ring。
start_node 3
start_node 4
wait_http_status 3 /readyz 503 15
wait_http_status 4 /readyz 503 15
curl -fsS -X POST \
  -H 'Content-Type: application/json' \
  --data '{"members":["im-0","im-1","im-2","im-3","im-4"]}' \
  "http://127.0.0.1:${HTTP_PORTS[0]}/clusterz" >/dev/null
for index in 0 1 2 3 4; do
  wait_http_status "${index}" /readyz 200 45
done

# 五节点下执行真实热点、重连风暴和 p99 发布门禁。
IM_TEST_STANDALONE_HTTP="http://127.0.0.1:${HTTP_PORTS[0]}" \
IM_TEST_STANDALONE_GRPC="127.0.0.1:${CLIENT_GRPC_PORTS[0]}" \
IM_TEST_STANDALONE_HOT_RECEIVERS="${HOT_RECEIVERS}" \
IM_TEST_STANDALONE_HOT_MESSAGES="${HOT_MESSAGES}" \
IM_TEST_STANDALONE_RECONNECTS="${RECONNECTS}" \
IM_TEST_ACK_P99_MAX=300ms \
IM_TEST_DELIVERY_P99_MAX=500ms \
go test ./tests/standalone -count=1 -v \
  -run '^TestStandalone(WebSocketHotTopic|WebSocketReconnectBurst)$' |
  tee "${WORK_DIR}/capacity.log"

# 在线缩容强制先 Drain 两个节点，再由活动节点提交 5→3。
for index in 3 4; do
  curl -fsS -X POST "http://127.0.0.1:${HTTP_PORTS[index]}/drainz" >/dev/null
  wait_http_status "${index}" /readyz 503 15
done
curl -fsS -X POST \
  -H 'Content-Type: application/json' \
  --data '{"members":["im-0","im-1","im-2"]}' \
  "http://127.0.0.1:${HTTP_PORTS[0]}/clusterz" >/dev/null
for index in 0 1 2; do
  wait_http_status "${index}" /readyz 200 45
done
stop_node 3
stop_node 4

# 丢失 etcd 多数派时，所有节点必须在 lease_ttl+2s 附近退出 Readiness；
# 多数派恢复后验证租约自动重新注册，不依赖人工重启。
docker pause "${ETCD_CONTAINERS[1]}" "${ETCD_CONTAINERS[2]}" >/dev/null
PAUSED_ETCD=1
for index in 0 1 2; do
  wait_http_status "${index}" /readyz 503 12
done
write_status="$(curl -sS -o /dev/null -w '%{http_code}' \
  "http://127.0.0.1:${HTTP_PORTS[0]}/v0/channels/lp?apikey=${clusterTestAPIKey:-AQEAAAABAAD_rAp4DJh05a1HAwFT3A6K}" ||
  true)"
if [[ "${write_status}" != "503" ]]; then
  echo "控制面失去多数派后新 Session 状态=${write_status}，期望 503" >&2
  exit 1
fi
docker unpause "${ETCD_CONTAINERS[1]}" "${ETCD_CONTAINERS[2]}" >/dev/null
PAUSED_ETCD=0
for index in 0 1 2; do
  wait_http_status "${index}" /readyz 200 45
done

# 冻结一个活动节点超过 lease_ttl，模拟该节点与控制面和数据面同时隔离。
# 多数派推进新 View 后恢复该进程，它必须先拒绝接流，再自动重新注册。
ISOLATION_EPOCH="$(read_cluster_epoch 0)"
kill -STOP "${SERVER_PIDS[2]}"
STOPPED_NODE=2
sleep 7
wait_new_epoch 0 "${ISOLATION_EPOCH}"
wait_new_epoch 1 "${ISOLATION_EPOCH}"
kill -CONT "${SERVER_PIDS[2]}"
STOPPED_NODE=-1
# 恢复后的调度顺序可能先返回一次 503，也可能先完成重新注册；若直接
# Ready，则必须已经应用大于隔离前的 fence，不能以旧 epoch 接流。
rejoin_deadline=$((SECONDS + 10))
safe_rejoin=0
while ((SECONDS < rejoin_deadline)); do
  isolation_response="$(curl -sS -w $'\\n%{http_code}' \
    "http://127.0.0.1:${HTTP_PORTS[2]}/readyz" || true)"
  isolation_status="${isolation_response##*$'\n'}"
  isolation_body="${isolation_response%$'\n'*}"
  if [[ "${isolation_status}" == "503" ]]; then
    safe_rejoin=1
    break
  fi
  isolation_epoch="$(printf '%s' "${isolation_body}" |
    sed -E 's/.*"cluster_epoch":([0-9]+).*/\1/')"
  if [[ "${isolation_status}" == "200" ]] &&
    [[ "${isolation_epoch}" =~ ^[0-9]+$ ]] &&
    ((isolation_epoch > ISOLATION_EPOCH)); then
    safe_rejoin=1
    break
  fi
  sleep 1
done
if ((safe_rejoin != 1)); then
  echo "隔离节点恢复后既未拒绝接流，也未推进到新 fence" >&2
  exit 1
fi
for index in 0 1 2; do
  wait_http_status "${index}" /readyz 200 45
done

# 数据库完全失联时全部节点摘流，恢复后自动重新接流。
docker pause "${MYSQL_CONTAINER}" >/dev/null
for index in 0 1 2; do
  wait_http_status "${index}" /readyz 503 15
done
docker unpause "${MYSQL_CONTAINER}" >/dev/null
for index in 0 1 2; do
  wait_http_status "${index}" /readyz 200 45
done

# 逐节点 Drain、停止、重启，确保任一时刻保留活动多数派。
for rolling_index in 0 1 2; do
  curl -fsS -X POST \
    "http://127.0.0.1:${HTTP_PORTS[rolling_index]}/drainz" >/dev/null
  wait_http_status "${rolling_index}" /readyz 503 15
  stop_node "${rolling_index}"
  start_node "${rolling_index}"
  for index in 0 1 2; do
    wait_http_status "${index}" /readyz 200 45
  done
done

FINISHED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
{
  printf '# 集群进程认证报告\n\n'
  printf -- '- 开始时间：%s\n' "${STARTED_AT}"
  printf -- '- 完成时间：%s\n' "${FINISHED_AT}"
  printf -- '- Git commit：%s\n' "${SOURCE_COMMIT}"
  printf -- '- 工作区存在未提交变更：%s\n' "${SOURCE_DIRTY}"
  printf -- '- 被测 im-server SHA-256：%s\n' "${SERVER_SHA256}"
  printf -- '- Owner SIGKILL RTO：%ss（门限 15s）\n' "${FAILOVER_RTO}"
  printf -- '- 在线拓扑：3→5→3 通过\n'
  printf -- '- etcd 多数派丢失：fail-closed 与自动租约恢复通过\n'
  printf -- '- 单节点隔离超过 lease TTL：旧视图拒写与自动重新注册通过\n'
  printf -- '- 数据库失联：全节点摘流与恢复通过\n'
  printf -- '- 滚动重启：3/3 节点通过\n'
  printf -- '- 容量门禁：%s 接收者 × %s 消息，ACK p99≤300ms，投递 p99≤500ms\n' \
    "${HOT_RECEIVERS}" "${HOT_MESSAGES}"
  printf '\n## 容量测试原始输出\n\n```text\n'
  sed -n '1,240p' "${WORK_DIR}/capacity.log"
  printf '```\n'
} >"${REPORT_FILE}"

for index in 0 1 2; do
  stop_node "${index}"
done

echo "真实集群进程认证通过，报告：${REPORT_FILE}"
