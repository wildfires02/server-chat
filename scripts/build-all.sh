#!/usr/bin/env bash

# 为支持的平台和数据库适配器生成隔离、可复现的发布归档。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"
REPO_ROOT="$(im_repo_root "${SCRIPT_DIR}")"

version_input=""
database=""
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
    -h|--help)
      echo "用法：$0 [--tag v0.29.0] [--db mysql]"
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

if [[ -n "${database}" ]]; then
  im_validate_database "${database}"
  databases=("${database}")
else
  databases=(mysql postgres mongodb rethinkdb alldbs)
fi

platforms=(
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "linux/amd64"
  "linux/arm64"
)
release_dir="${REPO_ROOT}/releases/${version}"
temporary_root="$(mktemp -d "${TMPDIR:-/tmp}/im-release.XXXXXX")"

cleanup() {
  rm -rf "${temporary_root}"
}
trap cleanup EXIT

mkdir -p "${release_dir}"
revision="$(git -C "${REPO_ROOT}" rev-parse --short=12 HEAD)"

# database_tags 把 alldbs 归档名映射为实际 Go 构建标签。
database_tags() {
  if [[ "$1" = "alldbs" ]]; then
    echo "mysql postgres mongodb rethinkdb"
  else
    echo "$1"
  fi
}

# archive_directory 根据目标平台生成 zip 或 tar.gz。
archive_directory() {
  local source_dir=$1
  local archive_base=$2
  local os_name=$3
  if [[ "${os_name}" = "windows" ]]; then
    (
      cd "${source_dir}"
      zip -q -r "${archive_base}.zip" .
    )
  else
    tar -C "${source_dir}" -zcf "${archive_base}.tar.gz" .
  fi
}

for target in "${platforms[@]}"; do
  os_name=${target%/*}
  architecture=${target#*/}
  extension=""
  [[ "${os_name}" = "windows" ]] && extension=".exe"

  for database_name in "${databases[@]}"; do
    package_dir="${temporary_root}/server-${database_name}-${os_name}-${architecture}"
    mkdir -p "${package_dir}/configs"
    build_tags="$(database_tags "${database_name}")"
    echo "构建服务端 ${database_name} ${os_name}/${architecture}"

    CGO_ENABLED=0 GOOS="${os_name}" GOARCH="${architecture}" \
      go build -trimpath \
        -ldflags="-s -w -X chat/internal/server.buildstamp=${version}+${revision}" \
        -tags "${build_tags}" \
        -o "${package_dir}/im-server${extension}" \
        "${REPO_ROOT}/cmd/im-server"
    CGO_ENABLED=0 GOOS="${os_name}" GOARCH="${architecture}" \
      go build -trimpath -ldflags="-s -w" -tags "${build_tags}" \
        -o "${package_dir}/init-db${extension}" \
        "${REPO_ROOT}/cmd/init-db"
    CGO_ENABLED=0 GOOS="${os_name}" GOARCH="${architecture}" \
      go build -trimpath -ldflags="-s -w" \
        -o "${package_dir}/keygen${extension}" \
        "${REPO_ROOT}/cmd/keygen"

    cp "${REPO_ROOT}/configs/im.yaml" "${package_dir}/configs/"
    cp "${REPO_ROOT}/configs/im.cluster.yaml" "${package_dir}/configs/"
    cp "${REPO_ROOT}/configs/init-db.yaml" "${package_dir}/configs/"
    cp "${REPO_ROOT}/cmd/init-db/data.json" "${package_dir}/"
    cp "${REPO_ROOT}/scripts/credentials-cookie.sh" "${package_dir}/"

    archive_directory \
      "${package_dir}" \
      "${release_dir}/im-${database_name}-${os_name}-${architecture}" \
      "${os_name}"
  done

  # CLI 与 Chatbot 与数据库无关，每个平台只构建一次。
  tools_dir="${temporary_root}/tools-${os_name}-${architecture}"
  mkdir -p "${tools_dir}"
  CGO_ENABLED=0 GOOS="${os_name}" GOARCH="${architecture}" \
    go build -trimpath -ldflags="-s -w" \
      -o "${tools_dir}/im-cli${extension}" "${REPO_ROOT}/cmd/im-cli"
  CGO_ENABLED=0 GOOS="${os_name}" GOARCH="${architecture}" \
    go build -trimpath -ldflags="-s -w" \
      -o "${tools_dir}/chatbot${extension}" "${REPO_ROOT}/cmd/chatbot"
  cp "${REPO_ROOT}/cmd/chatbot/quotes.txt" "${tools_dir}/"
  if compgen -G "${REPO_ROOT}/cmd/im-cli/examples/*" >/dev/null; then
    mkdir -p "${tools_dir}/examples"
    cp "${REPO_ROOT}"/cmd/im-cli/examples/* "${tools_dir}/examples/"
  fi
  archive_directory \
    "${tools_dir}" \
    "${release_dir}/im-tools-${os_name}-${architecture}" \
    "${os_name}"
done

echo "全量构建完成：${release_dir}"
