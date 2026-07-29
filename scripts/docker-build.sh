#!/usr/bin/env bash

# 使用 BuildKit 构建带不可变版本标签的服务端、Chatbot 和 Exporter 镜像。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"
REPO_ROOT="$(im_repo_root "${SCRIPT_DIR}")"

version_input=""
database=""
platform="${IM_DOCKER_PLATFORM:-linux/amd64}"
namespace="${IM_IMAGE_NAMESPACE:-im}"
tag_latest="${IM_TAG_LATEST:-0}"

usage() {
  echo "用法：$0 --tag v0.29.0 [--db mysql] [--platform linux/amd64]"
}

while (($# > 0)); do
  case "$1" in
    --tag)
      (($# >= 2)) || im_die "--tag 缺少值"
      version_input=$2
      shift 2
      ;;
    --db)
      (($# >= 2)) || im_die "--db 缺少值"
      database=$2
      shift 2
      ;;
    --platform)
      (($# >= 2)) || im_die "--platform 缺少值"
      platform=$2
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      im_die "未知参数：$1"
      ;;
  esac
done

[[ -n "${version_input}" ]] || im_die "必须提供 --tag"
version="$(im_normalize_version "${version_input}")"
if [[ -n "${database}" ]]; then
  im_validate_database "${database}"
  databases=("${database}")
else
  databases=(mysql postgres mongodb rethinkdb alldbs)
fi

im_require_command docker
docker buildx version >/dev/null

revision="$(git -C "${REPO_ROOT}" rev-parse --verify HEAD)"
created="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
minor_version="${version%.*}"
source_url="${IM_SOURCE_URL:-unknown}"

# build_image 为单个 Dockerfile 组装精确标签；latest 必须显式开启。
build_image() {
  local dockerfile=$1
  local image_name=$2
  shift 2
  local tag_arguments=(--tag "${image_name}:${version}")
  if im_is_stable_version "${version}"; then
    tag_arguments+=(--tag "${image_name}:${minor_version}")
    if [[ "${tag_latest}" = "1" ]]; then
      tag_arguments+=(--tag "${image_name}:latest")
    fi
  fi
  docker buildx build \
    --load \
    --platform "${platform}" \
    --file "${dockerfile}" \
    --build-arg "VERSION=${version}" \
    --build-arg "REVISION=${revision}" \
    --build-arg "CREATED=${created}" \
    --build-arg "SOURCE=${source_url}" \
    "${tag_arguments[@]}" \
    "$@" \
    "${REPO_ROOT}"
}

for database_name in "${databases[@]}"; do
  if [[ "${database_name}" = "alldbs" ]]; then
    server_image="${namespace}/im"
  else
    server_image="${namespace}/im-${database_name}"
  fi
  echo "构建 ${server_image}:${version} (${platform})"
  build_image \
    "${REPO_ROOT}/deployments/docker/im/Dockerfile" \
    "${server_image}" \
    --build-arg "TARGET_DB=${database_name}"
done

# 只构建一个数据库时，不重复构建与数据库无关的附属镜像。
if [[ -z "${database}" ]]; then
  build_image \
    "${REPO_ROOT}/deployments/docker/chatbot/Dockerfile" \
    "${namespace}/chatbot"
  build_image \
    "${REPO_ROOT}/deployments/docker/exporter/Dockerfile" \
    "${namespace}/exporter"
fi

echo "Docker 镜像构建完成：版本 ${version}"
