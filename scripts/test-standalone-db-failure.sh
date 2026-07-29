#!/usr/bin/env bash

# 验证单机服务在数据库失联时关闭就绪状态，并在数据库恢复后自动恢复。
set -euo pipefail

HTTP_BASE="${IM_TEST_STANDALONE_HTTP:-}"
DB_CONTAINER="${IM_TEST_STANDALONE_DB_CONTAINER:-}"
FAILURE_TIMEOUT="${IM_TEST_STANDALONE_DB_FAILURE_TIMEOUT:-25}"
RECOVERY_TIMEOUT="${IM_TEST_STANDALONE_DB_RECOVERY_TIMEOUT:-25}"

if [[ -z "${HTTP_BASE}" ]]; then
  echo "必须设置 IM_TEST_STANDALONE_HTTP，例如 http://127.0.0.1:26060" >&2
  exit 1
fi
if [[ -z "${DB_CONTAINER}" ]]; then
  echo "必须设置 IM_TEST_STANDALONE_DB_CONTAINER，并且只能指向隔离测试容器" >&2
  exit 1
fi
# 容器名限制为 Docker 支持的安全字符，禁止把 shell 片段当作名称执行。
if [[ ! "${DB_CONTAINER}" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]]; then
  echo "IM_TEST_STANDALONE_DB_CONTAINER 包含非法字符" >&2
  exit 1
fi
if [[ ! "${FAILURE_TIMEOUT}" =~ ^[1-9][0-9]*$ ]] ||
  [[ ! "${RECOVERY_TIMEOUT}" =~ ^[1-9][0-9]*$ ]]; then
  echo "故障和恢复超时必须是正整数秒" >&2
  exit 1
fi

READY_URL="${HTTP_BASE%/}/readyz"
container_paused=0

# 无论测试从哪里退出，都恢复数据库容器，避免影响后续开发。
cleanup() {
  if [[ "${container_paused}" == "1" ]]; then
    docker unpause "${DB_CONTAINER}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# pause 前必须确认服务和数据库处于健康状态。
initial_status="$(curl -sS -o /dev/null -w '%{http_code}' "${READY_URL}")"
if [[ "${initial_status}" != "200" ]]; then
  echo "故障注入前 readyz=${initial_status}，期望 200" >&2
  exit 1
fi

docker pause "${DB_CONTAINER}" >/dev/null
container_paused=1

# 等待健康检查发现数据库不可用，入口应 fail-closed。
failure_deadline=$((SECONDS + FAILURE_TIMEOUT))
failure_status="200"
while ((SECONDS < failure_deadline)); do
  failure_status="$(curl -sS -o /dev/null -w '%{http_code}' "${READY_URL}" || true)"
  if [[ "${failure_status}" != "200" ]]; then
    break
  fi
  sleep 1
done
if [[ "${failure_status}" == "200" ]]; then
  echo "${FAILURE_TIMEOUT}s 内 readyz 未在数据库失联后关闭" >&2
  exit 1
fi

docker unpause "${DB_CONTAINER}" >/dev/null
container_paused=0

# 数据库恢复后服务应自行回到 ready，无需重启进程。
recovery_deadline=$((SECONDS + RECOVERY_TIMEOUT))
recovery_status="000"
while ((SECONDS < recovery_deadline)); do
  recovery_status="$(curl -sS -o /dev/null -w '%{http_code}' "${READY_URL}" || true)"
  if [[ "${recovery_status}" == "200" ]]; then
    break
  fi
  sleep 1
done
if [[ "${recovery_status}" != "200" ]]; then
  echo "${RECOVERY_TIMEOUT}s 内 readyz 未在数据库恢复后回到 200" >&2
  exit 1
fi

echo "数据库失联与恢复验证通过：失联状态=${failure_status}，恢复状态=${recovery_status}"
