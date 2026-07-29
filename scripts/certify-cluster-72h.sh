#!/usr/bin/env bash

# 在真实 staging 三个入口上连续执行 72 小时跨节点消息稳定性认证。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
SOAK_DURATION="${IM_CLUSTER_SOAK_DURATION:-72h}"
SOAK_QPS="${IM_CLUSTER_SOAK_QPS:-10}"
CHECKPOINT_INTERVAL="${IM_CLUSTER_SOAK_CHECKPOINT:-1m}"
ACK_P99_MAX="${IM_CLUSTER_ACK_P99_MAX:-300ms}"
DELIVERY_P99_MAX="${IM_CLUSTER_DELIVERY_P99_MAX:-500ms}"
REPORT_PATH="${IM_CLUSTER_SOAK_REPORT:-${REPO_ROOT}/test-results/cluster-soak-latest.json}"

# require_value 阻止发布流水线把缺失的 staging 地址误当成跳过成功。
require_value() {
  local name=$1
  local value=$2
  if [[ -z "${value}" ]]; then
    echo "${name} 不能为空，必须指向一个真实 staging 节点" >&2
    exit 1
  fi
}

require_value IM_TEST_CLUSTER_NODE0_HTTP "${IM_TEST_CLUSTER_NODE0_HTTP:-}"
require_value IM_TEST_CLUSTER_NODE1_HTTP "${IM_TEST_CLUSTER_NODE1_HTTP:-}"
require_value IM_TEST_CLUSTER_NODE2_HTTP "${IM_TEST_CLUSTER_NODE2_HTTP:-}"
require_value IM_TEST_CLUSTER_API_KEY "${IM_TEST_CLUSTER_API_KEY:-}"
require_value IM_TEST_CLUSTER_USERNAME "${IM_TEST_CLUSTER_USERNAME:-}"
require_value IM_TEST_CLUSTER_PASSWORD "${IM_TEST_CLUSTER_PASSWORD:-}"
require_value IM_CLUSTER_RELEASE_ID "${IM_CLUSTER_RELEASE_ID:-}"
if [[ ! "${IM_CLUSTER_RELEASE_ID}" =~ (^|@)sha256:[[:xdigit:]]{64}$ ]]; then
  echo "IM_CLUSTER_RELEASE_ID 必须是不可变 SHA-256（sha256:<64位> 或 image@sha256:<64位>）" >&2
  exit 1
fi

mkdir -p "$(dirname "${REPORT_PATH}")"
cd "${REPO_ROOT}"

# Go 测试超时比默认计划时长多留一小时，用于最后检查点和连接关闭；
# 自定义超过 72h 的任务必须同步覆盖这个值。
GO_TEST_TIMEOUT="${IM_CLUSTER_SOAK_GO_TEST_TIMEOUT:-73h}"

IM_TEST_CLUSTER_SOAK_DURATION="${SOAK_DURATION}" \
IM_TEST_CLUSTER_SOAK_QPS="${SOAK_QPS}" \
IM_TEST_CLUSTER_SOAK_CHECKPOINT="${CHECKPOINT_INTERVAL}" \
IM_TEST_CLUSTER_SOAK_REPORT="${REPORT_PATH}" \
IM_TEST_CLUSTER_RELEASE_ID="${IM_CLUSTER_RELEASE_ID}" \
IM_TEST_CLUSTER_REQUIRE_PRODUCTION_DURATION=1 \
IM_TEST_ACK_P99_MAX="${ACK_P99_MAX}" \
IM_TEST_DELIVERY_P99_MAX="${DELIVERY_P99_MAX}" \
go test ./tests/cluster \
  -run '^TestClusterSoak$' \
  -count=1 \
  -timeout="${GO_TEST_TIMEOUT}" \
  -v

# 只有测试自然跑满并写出 passed 检查点才算认证成功。
if ! grep -q '"status": "passed"' "${REPORT_PATH}"; then
  echo "稳定性报告没有 passed 状态：${REPORT_PATH}" >&2
  exit 1
fi

echo "集群 ${SOAK_DURATION} 稳定性认证通过，报告：${REPORT_PATH}"
