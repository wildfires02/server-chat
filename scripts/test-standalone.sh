#!/usr/bin/env bash

# 执行开发单机版配置、功能、数据竞争和可选微基准回归。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

# 配置门禁和共享业务测试使用同一套 internal/server 包。
go test ./internal/configutil ./internal/server ./tests/standalone
go test -race ./internal/server -count=1

# 设置真实进程地址后，执行三协议一致性、空闲连接和重连突发回归。
if [[ -n "${IM_TEST_STANDALONE_HTTP:-}" ]]; then
  if [[ -z "${IM_TEST_STANDALONE_GRPC:-}" ]]; then
    echo "设置 IM_TEST_STANDALONE_HTTP 时必须同时设置 IM_TEST_STANDALONE_GRPC" >&2
    exit 1
  fi
  go test ./tests/standalone -count=1 -v
  # 指定隔离数据库容器时，追加 Readiness 的失联与自动恢复验证。
  if [[ -n "${IM_TEST_STANDALONE_DB_CONTAINER:-}" ]]; then
    "${REPO_ROOT}/scripts/test-standalone-db-failure.sh"
  else
    echo "跳过数据库失联测试：未设置 IM_TEST_STANDALONE_DB_CONTAINER"
  fi
else
  echo "跳过真实单机进程测试：未设置 IM_TEST_STANDALONE_HTTP"
fi

# 微基准默认关闭，避免普通 CI 把共享主机波动误判为性能回退。
if [[ "${IM_STANDALONE_BENCHMARK:-0}" == "1" ]]; then
  go test ./internal/server -run '^$' \
    -bench 'BenchmarkStandalone(JSONSerialize|GRPCSerialize|SessionQueue|TopicFanout)' \
    -benchmem -benchtime=2s -count=3
fi

echo "开发单机版回归执行完成"
