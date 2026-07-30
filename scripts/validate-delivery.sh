#!/usr/bin/env bash

# 统一校验 YAML 配置、Docker Compose、Kubernetes 和 Shell 交付物。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"
REPO_ROOT="$(im_repo_root "${SCRIPT_DIR}")"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/im-delivery.XXXXXX")"

cleanup() {
  rm -rf "${TEMP_DIR}"
}
trap cleanup EXIT

cd "${REPO_ROOT}"
im_require_command go

# 一次编译全部适配器，提前发现任一 Docker 构建目标与依赖版本不兼容。
GOCACHE="${GOCACHE:-${TEMP_DIR}/go-cache}" \
  go build -tags "mysql postgres mongodb rethinkdb" \
  -o "${TEMP_DIR}/im-server" ./cmd/im-server

# 配置门禁由 Viper 仅 YAML 单元测试执行，不启动数据库或监听器。
go test ./internal/configutil ./internal/server -run \
  'Test(ExampleYAMLConfig|AdminYAMLConfig|ProductionClusterYAMLConfig)$'

# 实际部署文件禁止浮动 latest、空密码数据库和旧 envsubst 模板。
if rg -n \
  'FROM [^ ]+:latest|image:[[:space:]]+[^#[:space:]]+:latest|MYSQL_ALLOW_EMPTY_PASSWORD|config\\.template\\.yaml' \
  deployments \
  --glob 'Dockerfile' \
  --glob '*.yaml' \
  --glob '*.yml'; then
  im_die "部署物包含浮动镜像、空密码或旧配置模板"
fi

"${REPO_ROOT}/scripts/validate-cluster-manifests.sh"

if docker compose version >/dev/null 2>&1; then
  compose_dir="${REPO_ROOT}/deployments/docker/compose"
  docker compose --project-directory "${compose_dir}" \
    -f "${compose_dir}/single-instance.yml" config --quiet
  docker compose --project-directory "${compose_dir}" \
    -f "${compose_dir}/cluster.yml" config --quiet
  for database_name in postgres mongodb rethinkdb; do
    docker compose --project-directory "${compose_dir}" \
      -f "${compose_dir}/single-instance.yml" \
      -f "${compose_dir}/single-instance.${database_name}.yml" \
      config --quiet
    docker compose --project-directory "${compose_dir}" \
      -f "${compose_dir}/cluster.yml" \
      -f "${compose_dir}/cluster.${database_name}.yml" \
      config --quiet
  done
else
  echo "跳过 Compose 渲染：docker compose 不可用"
fi

if command -v shellcheck >/dev/null 2>&1; then
  shellcheck scripts/*.sh scripts/lib/*.sh deployments/docker/*/*.sh
else
  echo "跳过 ShellCheck：shellcheck 不可用"
fi

echo "配置与部署交付物校验通过"
