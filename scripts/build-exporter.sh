#!/usr/bin/env bash

# 为受支持平台构建独立 Exporter 二进制，不修改既有发布目录中的其他文件。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"
REPO_ROOT="$(im_repo_root "${SCRIPT_DIR}")"

version_input=""
while (($# > 0)); do
  case "$1" in
    --tag)
      (($# >= 2)) || im_die "--tag 缺少值"
      version_input=$2
      shift 2
      ;;
    -h|--help)
      echo "用法：$0 [--tag v0.29.0]"
      exit 0
      ;;
    *)
      im_die "未知参数：$1"
      ;;
  esac
done

if [[ -z "${version_input}" ]]; then
  version_input="$(git -C "${REPO_ROOT}" describe --tags --always)"
  if [[ ! "${version_input}" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+ ]]; then
    version_input="v0.0.0+${version_input//[^0-9A-Za-z.-]/-}"
  fi
fi
version="$(im_normalize_version "${version_input}")"
output_dir="${REPO_ROOT}/releases/${version}"
mkdir -p "${output_dir}"

platforms=(
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "linux/amd64"
  "linux/arm64"
)

for target in "${platforms[@]}"; do
  os_name=${target%/*}
  architecture=${target#*/}
  extension=""
  [[ "${os_name}" = "windows" ]] && extension=".exe"
  output_file="${output_dir}/im-exporter-${os_name}-${architecture}${extension}"
  echo "构建 Exporter ${os_name}/${architecture}"
  CGO_ENABLED=0 GOOS="${os_name}" GOARCH="${architecture}" \
    go build \
      -trimpath \
      -ldflags="-s -w -X main.buildstamp=${version}" \
      -o "${output_file}" \
      "${REPO_ROOT}/cmd/exporter"
done

echo "Exporter 构建完成：${output_dir}"
