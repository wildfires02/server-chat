#!/bin/bash

# 在本地主机上启动/停止测试集群。本脚本不是生产环境脚本，仅供参考参考。

# 集群节点名称列表
ALL_NODE_NAMES=( one two three )
# 第一个节点监听客户端 HTTP 连接的端口
HTTP_BASE_PORT=6060
# 第一个节点监听集群内部 gRPC 通信的端口。
GRPC_BASE_PORT=16060

USAGE="Usage: $0 [ --config <path_to_im.conf> ] {start|stop}"

# 服务器二进制文件可能具有不同的名称和路径。
SERVER='./server'

if [ "$#" -lt "1" ]; then
  echo $USAGE
  exit 1
fi

while [[ $# -gt 0 ]]; do
  key="$1"
  shift
  echo "$key"
  case "$key" in
    -c|--config)
      config=$1
      shift # 参数值
      ;;
    -s|--static_data)
      static_data=$1
      shift # 参数值
      ;;
    start)
      if [ ! -z "$config" ] ; then
        IM_CONF=$config
      else
        IM_CONF="im.conf"
      fi
      if [ ! -z "${static_data+x}" ] ; then
        STATIC_DATA_DIR=$static_data
      else
        STATIC_DATA_DIR="static"
      fi

      echo "HTTP ports 6060-6062, gRPC ports 16060-16062, config ${config}"

      HTTP_PORT=$HTTP_BASE_PORT
      GRPC_PORT=$GRPC_BASE_PORT
      for NODE_NAME in "${ALL_NODE_NAMES[@]}"
      do
        # 启动节点
        $SERVER -config=${IM_CONF} -cluster_self=${NODE_NAME} -listen=:${HTTP_PORT} -grpc_listen=:${GRPC_PORT} -static_data=${STATIC_DATA_DIR} -log_flags=stdFlags,shortfile &
        # 将节点 PID 保存到临时文件。
        echo $!> "/var/tmp/im-${NODE_NAME}.pid"
        # 为下一个节点递增端口号。
        HTTP_PORT=$((HTTP_PORT+1))
        GRPC_PORT=$((GRPC_PORT+1))
      done
      exit 0
      ;;
    stop)
      echo 'Stopping cluster'

      for NODE_NAME in "${ALL_NODE_NAMES[@]}"
      do
        # 从临时文件读取运行中节点的 PID 并终止进程。
        kill `cat /var/tmp/im-${NODE_NAME}.pid`
        # 清理：删除临时文件。
        rm "/var/tmp/im-${NODE_NAME}.pid"
      done
      exit 0
      ;;
    *)
      echo $USAGE
      exit 1
  esac
done
