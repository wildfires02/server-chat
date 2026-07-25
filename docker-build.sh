#!/bin/bash

# ==============================================================================
# IM 聊天服务 Docker 镜像批量构建脚本 (linux/amd64)
# 支持构建 MySQL、PostgreSQL、MongoDB、RethinkDB 以及 alldbs 版本的 IM 镜像，
# 以及机器人 (im/chatbot) 和 监控导出器 (im/exporter) 镜像。
# ==============================================================================

# 解析命令行传入的参数，例如：tag=v0.25.0 db=mysql
for line in $@; do
  eval "$line"
done

# 去除 tag 前缀的 'v'（若存在）
tag=${tag#?}

if [ -z "$tag" ]; then
    echo "必须传入版本号 tag 参数，格式如：'tag=v0.25.0'"
    exit 1
fi

# 将版本号拆分为数组
ver=( ${tag//./ } )

# 如果版本号不包含连字符 '-'，则判定为正式发布版本
if [[ ${ver[2]} != *"-"* ]]; then
  FULLRELEASE=1
fi

# 在非 x86 架构主机上使用 docker buildx 跨平台构建 linux/amd64
buildcmd='build'
if [ `uname -m` != 'x86_64' ]; then
  buildcmd='buildx build --platform=linux/amd64'
fi

# 如果通过命令行参数指定了 db=xxx，则仅构建指定的数据库版本；否则默认构建所有数据库适配器版本
if [ "$db" ]; then
  dbtags=( "$db" )
else
  dbtags=( mysql postgres mongodb rethinkdb alldbs )
fi

# 循环构建各个数据库适配器对应的 IM 镜像
for dbtag in "${dbtags[@]}"
do
  if [ "$dbtag" == "alldbs" ]; then
    # 全适配器版本的镜像名称为 im/im
    name="im/im"
  else
    # 单适配器版本的镜像名称为 im/im-$dbtag
    name="im/im-${dbtag}"
  fi
  separator=
  rmitags="${name}:${ver[0]}.${ver[1]}.${ver[2]}"
  buildtags="--tag ${name}:${ver[0]}.${ver[1]}.${ver[2]}"
  if [ -n "$FULLRELEASE" ]; then
    rmitags="${rmitags} ${name}:latest ${name}:${ver[0]}.${ver[1]}"
    buildtags="${buildtags} --tag ${name}:latest --tag ${name}:${ver[0]}.${ver[1]}"
  fi
  # 清理旧的本地镜像标签
  docker rmi ${rmitags} 2>/dev/null || true
  # 基于本地源码构建镜像
  docker ${buildcmd} -f docker/im/Dockerfile --build-arg VERSION=$tag --build-arg TARGET_DB=${dbtag} ${buildtags} .
done

# 如果指定了具体 db 选项，构建完服务器镜像后退出
if [ "$db" ]; then
  exit 0
fi

# 构建聊天机器人 (im/chatbot) 镜像
buildtags="--tag im/chatbot:${ver[0]}.${ver[1]}.${ver[2]}"
rmitags="im/chatbot:${ver[0]}.${ver[1]}.${ver[2]}"
if [ -n "$FULLRELEASE" ]; then
  rmitags="${rmitags} im/chatbot:latest im/chatbot:${ver[0]}.${ver[1]}"
  buildtags="${buildtags}  --tag im/chatbot:latest --tag im/chatbot:${ver[0]}.${ver[1]}"
fi
docker rmi ${rmitags} 2>/dev/null || true
docker ${buildcmd} -f docker/chatbot/Dockerfile --build-arg VERSION=$tag ${buildtags} .

# 构建监控导出器 (im/exporter) 镜像
buildtags="--tag im/exporter:${ver[0]}.${ver[1]}.${ver[2]}"
rmitags="im/exporter:${ver[0]}.${ver[1]}.${ver[2]}"
if [ -n "$FULLRELEASE" ]; then
  rmitags="${rmitags} im/exporter:latest im/exporter:${ver[0]}.${ver[1]}"
  buildtags="${buildtags}  --tag im/exporter:latest --tag im/exporter:${ver[0]}.${ver[1]}"
fi
docker rmi ${rmitags} 2>/dev/null || true
docker ${buildcmd} -f docker/exporter/Dockerfile --build-arg VERSION=$tag ${buildtags} .
