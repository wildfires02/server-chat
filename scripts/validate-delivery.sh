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

# 一次编译全部适配器，既复用真实配置加载器，也提前发现任一 Docker 构建目标
# 与依赖版本不兼容；这里只执行配置门禁，不连接外部数据库。
GOCACHE="${GOCACHE:-${TEMP_DIR}/go-cache}" \
  go build -tags "mysql postgres mongodb rethinkdb" \
  -o "${TEMP_DIR}/im-server" ./cmd/im-server

"${TEMP_DIR}/im-server" --config=configs/im.yaml --validate_config

IM_CLUSTER_CONFIG__SELF=one \
IM_CLUSTER_CONFIG__ADVERTISE_ADDR=127.0.0.1:12000 \
IM_CLUSTER_CONFIG__TRANSPORT__LISTEN=127.0.0.1:12000 \
  "${TEMP_DIR}/im-server" \
  --config=configs/im.cluster-dev.yaml \
  --validate_config

IM_CLUSTER_CONFIG__SELF=im-0 \
IM_CLUSTER_CONFIG__ADVERTISE_ADDR=im-0.im.internal:12000 \
IM_CLUSTER_CONFIG__TLS__CERT_FILE=/validation/cluster-cert.pem \
IM_CLUSTER_CONFIG__TLS__KEY_FILE=/validation/cluster-key.pem \
IM_API_KEY_SALT=T713/rYYgW7g4m3vG6zGRh7+FM1t0T8j13koXScOAj4= \
IM_AUTH_CONFIG__TOKEN__KEY=wfaY2RgF2S1OQI/ZlK+LSrp1KB2jwAdGAIHQ7JZn+Kc= \
IM_STORE_CONFIG__UID_KEY=la6YsO+bNX/+XIkOqc5Svw== \
IM_STORE_CONFIG__ADAPTERS__POSTGRES__DSN=postgresql://validation.invalid/im \
IM_MEDIA__HANDLERS__S3__ACCESS_KEY_ID=validation-only \
IM_MEDIA__HANDLERS__S3__SECRET_ACCESS_KEY=validation-only \
IM_MEDIA__HANDLERS__S3__REGION=validation-only \
IM_MEDIA__HANDLERS__S3__BUCKET=validation-only \
  "${TEMP_DIR}/im-server" \
  --config=configs/im.cluster.yaml \
  --validate_config

IM_CLUSTER_CONFIG__SELF=im-0 \
IM_CLUSTER_CONFIG__ADVERTISE_ADDR=im-0.im-headless:12000 \
IM_CLUSTER_CONFIG__TLS__CERT_FILE=/validation/cluster-cert.pem \
IM_CLUSTER_CONFIG__TLS__KEY_FILE=/validation/cluster-key.pem \
IM_API_KEY_SALT=T713/rYYgW7g4m3vG6zGRh7+FM1t0T8j13koXScOAj4= \
IM_AUTH_CONFIG__TOKEN__KEY=wfaY2RgF2S1OQI/ZlK+LSrp1KB2jwAdGAIHQ7JZn+Kc= \
IM_STORE_CONFIG__UID_KEY=la6YsO+bNX/+XIkOqc5Svw== \
IM_STORE_CONFIG__ADAPTERS__POSTGRES__DSN=postgresql://validation.invalid/im \
IM_MEDIA__HANDLERS__S3__ACCESS_KEY_ID=validation-only \
IM_MEDIA__HANDLERS__S3__SECRET_ACCESS_KEY=validation-only \
IM_MEDIA__HANDLERS__S3__REGION=validation-only \
IM_MEDIA__HANDLERS__S3__BUCKET=validation-only \
  "${TEMP_DIR}/im-server" \
  --config=deployments/kubernetes/base/im.cluster.yaml \
  --validate_config

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
