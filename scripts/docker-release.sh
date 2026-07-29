#!/usr/bin/env bash

# 推送已经在可信 CI 中构建完成的镜像；登录必须由 CI 的凭据助手预先完成。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

version_input=""
database=""
namespace="${IM_IMAGE_NAMESPACE:-im}"
include_aliases=0

usage() {
  echo "用法：$0 --tag v0.29.0 [--db mysql] [--include-aliases]"
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
    --include-aliases)
      include_aliases=1
      shift
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
minor_version="${version%.*}"

# push_image 先确认本地精确版本存在，再按需推送稳定别名。
push_image() {
  local image_name=$1
  docker image inspect "${image_name}:${version}" >/dev/null
  docker push "${image_name}:${version}"
  if ((include_aliases == 1)) && im_is_stable_version "${version}"; then
    docker push "${image_name}:${minor_version}"
    docker push "${image_name}:latest"
  fi
}

for database_name in "${databases[@]}"; do
  if [[ "${database_name}" = "alldbs" ]]; then
    push_image "${namespace}/im"
  else
    push_image "${namespace}/im-${database_name}"
  fi
done

if [[ -z "${database}" ]]; then
  push_image "${namespace}/chatbot"
  push_image "${namespace}/exporter"
fi

echo "Docker 镜像推送完成：版本 ${version}"
