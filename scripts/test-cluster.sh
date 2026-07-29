#!/usr/bin/env bash

# 执行集群核心回归、数据竞争、可选真实组件和可选容量微基准。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

cd "${REPO_ROOT}"

# 核心状态机和 Ring 测试不依赖外部基础设施。
go test ./internal/server ./server/ringhash ./tests/cluster
go test -race ./internal/server -run 'TestCluster|TestServiceHealth' -count=3

# 部署物必须先通过离线 Kustomize 结构检查。
"${REPO_ROOT}/scripts/validate-cluster-manifests.sh"

# 配置真实 etcd 后执行租约、Watch、Drain、任务领取和多数派测试。
if [[ -n "${IM_TEST_ETCD_ENDPOINTS:-}" ]]; then
  go test ./internal/server -run TestEtcdControlPlaneIntegration -count=1
else
  echo "跳过真实 etcd：未设置 IM_TEST_ETCD_ENDPOINTS"
fi

# 数据库集成测试会重建指定测试库，只能传入隔离的测试 DSN。
if [[ -n "${IM_TEST_POSTGRES_FENCE_DSN:-}" ]]; then
  go test -tags postgres ./server/db/postgres \
    -run TestClusterFencingIntegration -count=1
else
  echo "跳过真实 PostgreSQL fencing：未设置 IM_TEST_POSTGRES_FENCE_DSN"
fi

if [[ -n "${IM_TEST_MYSQL_FENCE_DSN:-}" ]]; then
  go test -tags mysql ./server/db/mysql \
    -run TestClusterFencingIntegration -count=1
else
  echo "跳过真实 MySQL fencing：未设置 IM_TEST_MYSQL_FENCE_DSN"
fi

# 微基准默认关闭，避免普通回归受共享 CI 主机噪声影响。
if [[ "${IM_CLUSTER_BENCHMARK:-0}" == "1" ]]; then
  go test ./internal/server -run '^$' \
    -bench 'BenchmarkClusterTopicOwner(3|5)Nodes' \
    -benchtime=2s -count=3
fi

# 真实进程矩阵会创建隔离 Docker 依赖并运行约数分钟，普通开发回归默认关闭；
# 发布流水线必须设置为 1，不能只运行内存状态机测试。
if [[ "${IM_CLUSTER_PROCESS_E2E:-0}" == "1" ]]; then
  "${REPO_ROOT}/scripts/test-cluster-process.sh"
else
  echo "跳过真实五节点进程认证：未设置 IM_CLUSTER_PROCESS_E2E=1"
fi

echo "集群测试执行完成"
