#!/bin/bash

# 任何命令失败时立即终止，避免留下只生成一半的协议代码。
set -e

# 将 Go 工具安装目录加入 PATH，便于查找 protoc-gen-go 等生成插件。
GOPATH_BIN="$(go env GOPATH)/bin"
export PATH="$PATH:$GOPATH_BIN:$GOPATH/bin"

# 固定切换到协议目录，使脚本可从仓库内任意工作目录调用。
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# 按稳定顺序重新生成拆分后的普通消息类型和 gRPC 服务代码。
PROTO_FILES=(
  chat.proto
  cluster.proto
  file.proto
  node.proto
  plugin.proto
  model.proto
)

protoc --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative "${PROTO_FILES[@]}"
