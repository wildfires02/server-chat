#!/bin/bash

# ==============================================================================
# IM 聊天服务跨平台多架构全量编译打包脚本。
# 支持操作系统：Mac (darwin)、Windows、Linux
# 支持架构：amd64、arm64
# 支持数据库类型：MySQL、MongoDB、RethinkDB、PostgreSQL 及 alldbs（支持全部适配器）
# ==============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 目标操作系统平台列表
goplat=( darwin darwin windows linux linux )

# 目标 CPU 架构列表（顺序与 goplat 对应）
goarc=( amd64 arm64 amd64 amd64 arm64 )

# 构建组合数量
buildCount=${#goplat[@]}

# 支持的数据库适配器列表
dbadapters=( mysql mongodb rethinkdb postgres )
dbtags=( ${dbadapters[@]} alldbs )

# 解析命令行传入的入参 (例如 tag=v0.25.0)
for line in $@; do
  eval "$line"
done

# 提取版本号（去除前缀 'v'）
version=${tag#?}

if [ -z "$version" ]; then
  # 若未指定 tag，从 git describe 自动获取最新版本号
  version=`git describe --tags 2>/dev/null || echo "0.25.0"`
  version=${version#?}
fi

echo "正在打包发布 IM 版本：$version"

pushd "$REPO_ROOT" > /dev/null

# 清理并创建发布目标输出目录
rm -fR ./releases/${version}
rm -fR ./releases/tmp
mkdir -p ./releases/${version} ./releases/tmp

# 复制服务器配置文件 im.yaml、数据库初始化脚本及示例数据
cp ./configs/im.yaml ./releases/tmp/
cp ./cmd/init-db/data.json ./releases/tmp
cp ./scripts/credentials-cookie.sh ./releases/tmp/credentials.sh

# 若存在本地前端资源目录 static，复制前端网页静态文件
if [[ -d ./web/static ]]
then
  mkdir -p ./releases/tmp/static/img
  mkdir ./releases/tmp/static/img/bkg
  mkdir ./releases/tmp/static/css
  mkdir ./releases/tmp/static/audio
  mkdir ./releases/tmp/static/src
  mkdir ./releases/tmp/static/umd

  cp ./web/static/img/*.png ./releases/tmp/static/img 2>/dev/null || true
  cp ./web/static/img/*.svg ./releases/tmp/static/img 2>/dev/null || true
  cp ./web/static/img/*.jpeg ./releases/tmp/static/img 2>/dev/null || true
  cp ./web/static/img/bkg/*.png ./releases/tmp/static/img/bkg 2>/dev/null || true
  cp ./web/static/img/bkg/*.jpg ./releases/tmp/static/img/bkg 2>/dev/null || true
  cp ./web/static/img/bkg/*.json ./releases/tmp/static/img/bkg 2>/dev/null || true
  cp ./web/static/audio/*.m4a ./releases/tmp/static/audio 2>/dev/null || true
  cp ./web/static/css/*.css ./releases/tmp/static/css 2>/dev/null || true
  cp ./web/static/index.html ./releases/tmp/static 2>/dev/null || true
  cp ./web/static/index-dev.html ./releases/tmp/static 2>/dev/null || true
  cp ./web/static/version.js ./releases/tmp/static 2>/dev/null || true
  cp ./web/static/umd/*.js ./releases/tmp/static/umd 2>/dev/null || true
  cp ./web/static/manifest.json ./releases/tmp/static 2>/dev/null || true
  cp ./web/static/service-worker.js ./releases/tmp/static 2>/dev/null || true
  # 生成默认空 Firebase 客户端配置文件
  echo 'const FIREBASE_INIT = {};' > ./releases/tmp/static/firebase-init.js
else
  echo "未检测到静态前端资源目录（web/static），跳过静态文件复制"
fi

# 循环针对每个目标 OS/架构 组合进行编译打包
for (( i=0; i<${buildCount}; i++ ));
do
  plat="${goplat[$i]}"
  arc="${goarc[$i]}"

  # Windows 平台程序使用 .exe 后缀
  ext=""
  if [ "$plat" = "windows" ]; then
    ext=".exe"
  fi

  # 清理上一次构建的 keygen 秘钥生成工具
  rm -f ./releases/tmp/keygen
  rm -f ./releases/tmp/keygen.exe

  # 编译跨平台的 keygen 二进制工具
  env GOOS="${plat}" GOARCH="${arc}" go build -ldflags "-s -w" -o ./releases/tmp/keygen${ext} ./cmd/keygen > /dev/null

  # 针对不同数据库适配器 Tag 依次构建发布包
  for dbtag in "${dbtags[@]}"
  do
    echo "正在编译 ${dbtag}-${plat}/${arc}..."

    # 清理上一次构建的二进制文件
    rm -f ./releases/tmp/im
    rm -f ./releases/tmp/im.exe
    rm -f ./releases/tmp/init-db
    rm -f ./releases/tmp/init-db.exe

    # 如果是 alldbs 标记，编译全部数据库驱动；否则编译对应的单数据库驱动
    if [ "$dbtag" = "alldbs" ]; then
      buildtag="${dbadapters[@]}"
    else
      buildtag=$dbtag
    fi

    # 编译 IM 主服务器可执行文件
    env GOOS="${plat}" GOARCH="${arc}" go build \
      -ldflags "-s -w -X chat/internal/server.buildstamp=`git describe --tags 2>/dev/null || echo 'custom'`" -tags "${buildtag}" \
      -o ./releases/tmp/im${ext} ./cmd/im-server > /dev/null
    
    # 编译数据库初始化工具 init-db
    env GOOS="${plat}" GOARCH="${arc}" go build \
      -ldflags "-s -w" -tags "${buildtag}" -o ./releases/tmp/init-db${ext} ./cmd/init-db > /dev/null

    # 归档打包：Windows 使用 zip 格式，其余平台（Mac/Linux）使用 tar.gz 格式
    if [ "$plat" = "windows" ]; then
      rm -f ./releases/${version}/im-${dbtag}."${plat}-${arc}".zip
      pushd ./releases/tmp > /dev/null
      zip -q -r ../${version}/im-${dbtag}."${plat}-${arc}".zip ./*
      popd > /dev/null
    else
      plat2=$plat
      # 将 darwin 更名为 mac 以直观区分
      if [ "$plat" = "darwin" ]; then
        plat2=mac
      fi

      rm -f ./releases/${version}/im-${dbtag}."${plat2}-${arc}".tar.gz
      tar -C ./releases/tmp -zcf ./releases/${version}/im-${dbtag}."${plat2}-${arc}".tar.gz .
    fi
  done
done

# 打包聊天机器人 chatbot 组件
echo "正在打包聊天机器人 chatbot..."
rm -fR ./releases/tmp
mkdir -p ./releases/tmp

cp "$REPO_ROOT/cmd/chatbot/quotes.txt" ./releases/tmp 2>/dev/null || true

tar -C "$REPO_ROOT/releases/tmp" -zcf ./releases/${version}/chatbot.tar.gz .
pushd ./releases/tmp > /dev/null
zip -q -r ../${version}/chatbot.zip ./*
popd > /dev/null

# 打包命令行 CLI 工具
echo "正在打包命令行 CLI 工具..."
rm -fR ./releases/tmp
mkdir -p ./releases/tmp

cp "$REPO_ROOT"/cmd/im-cli/examples/*.txt ./releases/tmp 2>/dev/null || true

tar -C "$REPO_ROOT/releases/tmp" -zcf ./releases/${version}/cli.tar.gz .
pushd ./releases/tmp > /dev/null
zip -q -r ../${version}/cli.zip ./*
popd > /dev/null

# 打包完成，清理临时文件夹
rm -fR ./releases/tmp

popd > /dev/null
echo "编译打包完成！构建产物保存在 ./releases/${version}/ 目录下。"
