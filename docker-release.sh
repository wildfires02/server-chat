#!/bin/bash

# ==============================================================================
# IM 聊天服务 Docker 镜像发布推送到 Docker Hub 脚本
# 支持发布：IM 服务器 (im/im, im/im-$dbtag)、聊天机器人 (im/chatbot) 和 监控导出器 (im/exporter)
# ==============================================================================

# 计算容器镜像名称函数
function containerName() {
  if [ "$1" == "alldbs" ]; then
    # 全适配器版本的容器名称为 im
    local name="im"
  else
    # 否则为 im-$dbtag
    local name="im-${dbtag}"
  fi
  echo $name
}

# 解析命令行参数 (例如 tag=v0.25.0 db=mysql)
for line in $@; do
  eval "$line"
done

# 提取 tag 版本号
tag=${tag#?}

if [ -z "$tag" ]; then
    echo "必须提供版本号 tag 参数，例如 'tag=v0.25.0'"
    exit 1
fi

# 将版本号按 '.' 拆分为数组
ver=( ${tag//./ } )

# 如果版本号不包含连字符 '-'，判定为正式发布版本
if [[ ${ver[2]} != *"-"* ]]; then
  FULLRELEASE=1
fi

# 如果指定了 db 命令行参数，则仅处理该数据库镜像；否则处理所有数据库适配器
if [ "$db" ]; then
  dbtags=( "$db" )
else
  dbtags=( mysql postgres mongodb rethinkdb alldbs )
fi

# 读取 Docker Hub 账号密码凭据文件 .dockerhub
if [ -f .dockerhub ]; then
  source .dockerhub
else
  echo "未找到 .dockerhub 凭据配置文件"
fi

# 登录 Docker Hub
if [ ! -z "$user" ] && [ ! -z "$pass" ]; then
  docker login -u $user -p $pass
fi

# 推送不同数据库版本的 IM 服务器镜像
for dbtag in "${dbtags[@]}"
do
  name="$(containerName $dbtag)"
  # 如果是正式发布版本，推送 latest 与次版本号 Tag
  if [ -n "$FULLRELEASE" ]; then
    docker push im/${name}:latest
    docker push im/${name}:"${ver[0]}.${ver[1]}"
  fi
  docker push im/${name}:"${ver[0]}.${ver[1]}.${ver[2]}"
done

# 如果指定了 db 选项，推送完服务器镜像后直接退出
if [ "$db" ]; then
  exit 0
fi

# 推送聊天机器人 im/chatbot 镜像
if [ -n "$FULLRELEASE" ]; then
  docker push im/chatbot:latest
  docker push im/chatbot:"${ver[0]}.${ver[1]}"
fi
docker push im/chatbot:"${ver[0]}.${ver[1]}.${ver[2]}"

# 推送监控导出器 im/exporter 镜像
if [ -n "$FULLRELEASE" ]; then
  docker push im/exporter:latest
  docker push im/exporter:"${ver[0]}.${ver[1]}"
fi
docker push im/exporter:"${ver[0]}.${ver[1]}.${ver[2]}"

# 登出 Docker Hub
docker logout
