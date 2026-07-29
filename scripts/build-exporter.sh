#!/bin/bash

# 编译并归档 exporter 二进制文件及附属资源。

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 支持的操作系统：Mac (darwin)、Windows、Linux。
goplat=( darwin darwin windows linux )

# CPU 架构：amd64 和 arm64，顺序与操作系统列表对应。
goarc=( amd64 arm64 amd64 amd64 )

# 平台与架构组合数量。
buildCount=${#goplat[@]}

for line in $@; do
  eval "$line"
done

# 去除版本号前缀 'v'，例如 v0.16.4 -> 0.16.4。
version=${tag#?}

if [ -z "$version" ]; then
  # 从 git 标签获取发布版本号，标签格式类似 'v.1.2.3'，去除前缀 'v'。
  version=`git describe --tags`
  version=${version#?}
fi

echo "Releasing exporter $version"

pushd "$REPO_ROOT" > /dev/null

# 确保删除之前的构建产物。
mkdir -p ./releases/${version} ./releases/tmp
rm -f ./releases/${version}/exporter*

for (( i=0; i<${buildCount}; i++ ));
do
  plat="${goplat[$i]}"
  arc="${goarc[$i]}"

  echo "Building ${plat}/${arc}..."

  # 删除可能存在的旧版构建二进制文件。
  rm -f ./releases/tmp/exporter*

  # 设置交叉编译环境变量。
  env GOOS="${plat}" GOARCH="${arc}" go build \
    -ldflags "-s -w -X main.buildstamp=`git describe --tags`" \
    -o ./releases/tmp/exporter ./cmd/exporter > /dev/null

  # 归档打包：Windows 使用 zip 格式，其余平台使用 tar 格式。
  if [ "$plat" = "windows" ]; then
    # 仅复制二进制文件并添加 .exe 后缀。
    cp ./releases/tmp/exporter ./releases/${version}/exporter."${plat}-${arc}".exe
  else
    plat2=$plat
    # 将 'darwin' 重命名为 'mac'
    if [ "$plat" = "darwin" ]; then
      plat2=mac
    fi

    # 仅复制二进制文件。
    cp ./releases/tmp/exporter ./releases/${version}/exporter."${plat2}-${arc}"
  fi

done

popd > /dev/null
