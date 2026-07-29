#!/usr/bin/env bash

# 快速冒烟入口：默认只执行无外部副作用的交付校验和核心测试。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
run_process_tests=0
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/im-sanity.XXXXXX")"

# 使用本次任务独立的构建缓存，避免 CI 用户、Docker 或提权命令留下不同属主的缓存。
cleanup() {
  rm -rf "${TEMP_DIR}"
}
trap cleanup EXIT
export GOCACHE="${IM_TEST_GOCACHE:-${TEMP_DIR}/go-cache}"

while (($# > 0)); do
  case "$1" in
    --process)
      run_process_tests=1
      shift
      ;;
    -h|--help)
      echo "用法：$0 [--process]"
      exit 0
      ;;
    *)
      echo "未知参数：$1" >&2
      exit 1
      ;;
  esac
done

cd "${REPO_ROOT}"
"${SCRIPT_DIR}/validate-delivery.sh"
go test ./internal/configutil ./internal/server ./tests/standalone ./tests/cluster

if ((run_process_tests == 1)); then
  "${SCRIPT_DIR}/test-standalone-process.sh"
  "${SCRIPT_DIR}/test-cluster-process.sh"
fi

echo "冒烟测试通过"
