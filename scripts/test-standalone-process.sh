#!/usr/bin/env bash

# 在隔离 MySQL 容器中自动初始化、拉起、重启并验证真实单机服务进程。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CONFIG_FILE="${REPO_ROOT}/configs/im.yaml"
DATA_FILE="${REPO_ROOT}/cmd/init-db/data.json"
MYSQL_IMAGE="${IM_STANDALONE_E2E_MYSQL_IMAGE:-mysql:8.0}"
HTTP_PORT="${IM_STANDALONE_E2E_HTTP_PORT:-26060}"
GRPC_PORT="${IM_STANDALONE_E2E_GRPC_PORT:-26061}"
IDLE_CONNECTIONS="${IM_STANDALONE_E2E_IDLE_CONNECTIONS:-256}"
IDLE_HOLD="${IM_STANDALONE_E2E_IDLE_HOLD:-60s}"
HOT_RECEIVERS="${IM_STANDALONE_E2E_HOT_RECEIVERS:-16}"
HOT_MESSAGES="${IM_STANDALONE_E2E_HOT_MESSAGES:-100}"
SERVER_GOGC="${IM_STANDALONE_E2E_GOGC:-25}"
DB_PASSWORD="standalone-e2e-123"
DB_NAME="im_standalone_e2e"
DB_CONTAINER="im-standalone-e2e-$$"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/im-standalone-e2e.XXXXXX")"
SERVER_BIN="${WORK_DIR}/im-server"
INIT_BIN="${WORK_DIR}/init-db"
STATE_FILE="${WORK_DIR}/persistence-state.json"
SERVER_PID=""
SERVER_RUN=0
SERVER_LOG=""

# cleanup 只处理本脚本创建的 PID、唯一容器名和 mktemp 目录。
cleanup() {
  local exit_code=$?
  set +e
  if [[ -n "${SERVER_PID}" ]] && kill -0 "${SERVER_PID}" 2>/dev/null; then
    kill -TERM "${SERVER_PID}" 2>/dev/null
    wait "${SERVER_PID}" 2>/dev/null
  fi
  if ((exit_code != 0)) && [[ -n "${SERVER_LOG}" ]] && [[ -f "${SERVER_LOG}" ]]; then
    echo "单机进程测试失败，最后 120 行服务日志：" >&2
    tail -n 120 "${SERVER_LOG}" >&2
  fi
  docker rm -f "${DB_CONTAINER}" >/dev/null 2>&1
  rm -rf "${WORK_DIR}"
  exit "${exit_code}"
}
trap cleanup EXIT

# require_positive_integer 防止端口和测试规模参数被解释成 shell 片段。
require_positive_integer() {
  local name=$1
  local value=$2
  if [[ ! "${value}" =~ ^[1-9][0-9]*$ ]]; then
    echo "${name} 必须是正整数，当前值=${value}" >&2
    exit 1
  fi
}

require_positive_integer "IM_STANDALONE_E2E_HTTP_PORT" "${HTTP_PORT}"
require_positive_integer "IM_STANDALONE_E2E_GRPC_PORT" "${GRPC_PORT}"
require_positive_integer "IM_STANDALONE_E2E_IDLE_CONNECTIONS" "${IDLE_CONNECTIONS}"
require_positive_integer "IM_STANDALONE_E2E_HOT_RECEIVERS" "${HOT_RECEIVERS}"
require_positive_integer "IM_STANDALONE_E2E_HOT_MESSAGES" "${HOT_MESSAGES}"
require_positive_integer "IM_STANDALONE_E2E_GOGC" "${SERVER_GOGC}"

if ! command -v docker >/dev/null 2>&1; then
  echo "未找到 docker，无法创建隔离 MySQL 测试环境" >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "Docker daemon 不可用" >&2
  exit 1
fi
if ! command -v curl >/dev/null 2>&1; then
  echo "未找到 curl，无法验证健康检查" >&2
  exit 1
fi
if command -v lsof >/dev/null 2>&1; then
  if lsof -n -iTCP:"${HTTP_PORT}" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "HTTP 测试端口 ${HTTP_PORT} 已被占用" >&2
    exit 1
  fi
  if lsof -n -iTCP:"${GRPC_PORT}" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "gRPC 测试端口 ${GRPC_PORT} 已被占用" >&2
    exit 1
  fi
fi

cd "${REPO_ROOT}"

# 镜像不存在时显式拉取；已经缓存时不会访问网络。
if ! docker image inspect "${MYSQL_IMAGE}" >/dev/null 2>&1; then
  docker pull "${MYSQL_IMAGE}"
fi

docker run -d \
  --name "${DB_CONTAINER}" \
  --tmpfs /var/lib/mysql:rw,nosuid,size=1g \
  -p 127.0.0.1::3306 \
  -e "MYSQL_ROOT_PASSWORD=${DB_PASSWORD}" \
  "${MYSQL_IMAGE}" >/dev/null

# 等待 MySQL 引擎完成初始化，而不仅是等待 TCP 端口出现。
mysql_deadline=$((SECONDS + 90))
until docker exec "${DB_CONTAINER}" \
  mysqladmin ping -uroot "-p${DB_PASSWORD}" --silent >/dev/null 2>&1; do
  if ((SECONDS >= mysql_deadline)); then
    echo "MySQL 容器在 90 秒内没有就绪" >&2
    exit 1
  fi
  sleep 1
done

DB_PORT="$(docker port "${DB_CONTAINER}" 3306/tcp | tail -n 1)"
DB_PORT="${DB_PORT##*:}"
require_positive_integer "Docker MySQL 映射端口" "${DB_PORT}"

# 所有数据库和媒体目录覆盖只作用于本脚本启动的子进程。
export IM_STORE_CONFIG__ADAPTERS__MYSQL__ADDR="127.0.0.1:${DB_PORT}"
export IM_STORE_CONFIG__ADAPTERS__MYSQL__PASSWD="${DB_PASSWORD}"
export IM_STORE_CONFIG__ADAPTERS__MYSQL__DBNAME="${DB_NAME}"
export IM_MEDIA__HANDLERS__FS__UPLOAD_DIR="${WORK_DIR}/uploads"
export IM_TEST_STANDALONE_HTTP="http://127.0.0.1:${HTTP_PORT}"
export IM_TEST_STANDALONE_GRPC="127.0.0.1:${GRPC_PORT}"
export IM_TEST_STANDALONE_DB_CONTAINER="${DB_CONTAINER}"
export IM_TEST_STANDALONE_MYSQL_DSN="root:${DB_PASSWORD}@tcp(127.0.0.1:${DB_PORT})/${DB_NAME}?parseTime=true"
export IM_TEST_STANDALONE_REQUIRE_GC=1

go build -tags mysql -o "${SERVER_BIN}" ./cmd/im-server
go build -tags mysql -o "${INIT_BIN}" ./cmd/init-db
"${INIT_BIN}" \
  --config="${CONFIG_FILE}" \
  --data="${DATA_FILE}" \
  --reset=true

# wait_ready 等待 HTTP 监听器和数据库健康检查同时就绪。
wait_ready() {
  local deadline=$((SECONDS + 45))
  local status="000"
  while ((SECONDS < deadline)); do
    if ! kill -0 "${SERVER_PID}" 2>/dev/null; then
      echo "单机服务在就绪前退出" >&2
      return 1
    fi
    status="$(curl -sS -o /dev/null -w '%{http_code}' \
      "${IM_TEST_STANDALONE_HTTP}/readyz" || true)"
    if [[ "${status}" == "200" ]]; then
      return 0
    fi
    sleep 1
  done
  echo "单机服务 45 秒内未就绪，最后状态=${status}" >&2
  return 1
}

# start_server 使用同一数据库启动一个新的服务进程。
start_server() {
  SERVER_RUN=$((SERVER_RUN + 1))
  SERVER_LOG="${WORK_DIR}/server-${SERVER_RUN}.log"
  GOGC="${SERVER_GOGC}" "${SERVER_BIN}" \
    --config="${CONFIG_FILE}" \
    --listen="127.0.0.1:${HTTP_PORT}" \
    --grpc_listen="127.0.0.1:${GRPC_PORT}" \
    --static_data=- >"${SERVER_LOG}" 2>&1 &
  SERVER_PID=$!
  wait_ready
}

# stop_server 要求进程响应 SIGTERM、正常退出并执行数据库关闭 defer。
stop_server() {
  local deadline
  local process_status
  kill -TERM "${SERVER_PID}"
  deadline=$((SECONDS + 20))
  while kill -0 "${SERVER_PID}" 2>/dev/null && ((SECONDS < deadline)); do
    sleep 1
  done
  if kill -0 "${SERVER_PID}" 2>/dev/null; then
    echo "单机服务收到 SIGTERM 后 20 秒内没有退出" >&2
    return 1
  fi
  process_status=0
  wait "${SERVER_PID}" || process_status=$?
  SERVER_PID=""
  if [[ "${process_status}" != "0" ]]; then
    echo "单机服务退出码=${process_status}，期望 0" >&2
    return 1
  fi
  if ! grep -q "All done, good bye" "${SERVER_LOG}"; then
    echo "单机服务日志缺少完整关闭标记" >&2
    return 1
  fi
}

# crash_server 使用 SIGKILL 模拟 ACK 后进程立即崩溃，不给应用层执行排空或 defer。
crash_server() {
  local process_status=0
  kill -KILL "${SERVER_PID}"
  wait "${SERVER_PID}" || process_status=$?
  SERVER_PID=""
  if [[ "${process_status}" == "0" ]]; then
    echo "SIGKILL 后服务进程意外返回退出码 0" >&2
    return 1
  fi
}

start_server

# 第一组覆盖协议、完整消息生命周期、重连和真实热点 Topic。
IM_TEST_STANDALONE_HOT_RECEIVERS="${HOT_RECEIVERS}" \
IM_TEST_STANDALONE_HOT_MESSAGES="${HOT_MESSAGES}" \
go test ./tests/standalone -count=1 -v \
  -run '^TestStandalone(ProtocolConsistency|MessageLifecycle|WebSocketReconnectBurst|WebSocketHotTopic)$'

# 保持时间超过服务端 49.5 秒 Ping 周期，确保真实执行至少一轮 Ping/Pong。
IM_TEST_STANDALONE_IDLE_CONNECTIONS="${IDLE_CONNECTIONS}" \
IM_TEST_STANDALONE_IDLE_HOLD="${IDLE_HOLD}" \
go test ./tests/standalone -count=1 -v \
  -run '^TestStandaloneIdleWebSocketConnections$'

# 在真实 MySQL 上执行分级延迟和连接池耗尽/恢复。
go test -tags mysql ./server/db/mysql -count=1 -v \
  -run '^TestStandaloneMySQLLatencyAndPoolExhaustion$'

# 在第一次进程中发布并获得 ACK，然后立即 SIGKILL，覆盖进程崩溃窗口。
IM_TEST_STANDALONE_PERSISTENCE_PHASE=write \
IM_TEST_STANDALONE_PERSISTENCE_STATE="${STATE_FILE}" \
go test ./tests/standalone -count=1 -v \
  -run '^TestStandalonePersistenceProbe$'
crash_server

# 使用同一数据库重新拉起进程，验证历史恢复和 Client ID 幂等结果。
start_server
IM_TEST_STANDALONE_PERSISTENCE_PHASE=verify \
IM_TEST_STANDALONE_PERSISTENCE_STATE="${STATE_FILE}" \
go test ./tests/standalone -count=1 -v \
  -run '^TestStandalonePersistenceProbe$'

# 最后验证数据库完全失联时关闭 Readiness、恢复后自动重新接流。
"${REPO_ROOT}/scripts/test-standalone-db-failure.sh"
stop_server

echo "真实单机进程完成：启动、消息、热点、心跳、GC、数据库韧性、重启恢复、优雅关闭均通过"
