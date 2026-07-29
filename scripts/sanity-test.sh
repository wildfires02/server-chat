#!/bin/bash

# 对本地服务执行基础连接与协议健全性检查。

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BIN_DIR="$REPO_ROOT/bin"
IM_BINARY="$BIN_DIR/im-server"
cd "$REPO_ROOT"

# 终止并清理所有运行中的容器。
cleanup() {
  "$SCRIPT_DIR/run-cluster.sh" stop 2>/dev/null || true
  docker stop mysql && docker rm mysql
}

# 报告测试失败。
fail() {
  cleanup
  echo "**************************************************"
  printf "Tests Failed: ${@}\n"
  echo "**************************************************"
  exit 1
}

# 报告测试成功。
pass() {
  cleanup
  echo "**************************************************"
  echo "*                       OK                       *"
  echo "**************************************************"
  exit 0
}

# 启动 MySQL Docker 容器并等待数据库服务就绪。
setup() {
  docker info 1>/dev/null 2>&1 || (echo "docker not running" && return 1)
  docker run -p 3306:3306 --name mysql --env MYSQL_ALLOW_EMPTY_PASSWORD=yes -d mysql:5.7 || return 1

  echo -n "Waiting for mysql to come up..."
  # L7 应用层健康检查：使用 mysqladmin ping 检查 MySQL 引擎是否完全就绪（加 60s 超时保护）
  local count=0
  local max_attempts=60
  while ! mysqladmin ping -u root -h 127.0.0.1 --silent; do
    echo -n "."
    sleep 1
    count=$((count + 1))
    if [ $count -ge $max_attempts ]; then
      echo -e "\nTimed out waiting for MySQL after ${max_attempts} seconds."
      return 1
    fi
  done
  echo " MySQL is ready."

  mkdir -p "$BIN_DIR"
}

# 编译 IM 二进制文件。
build() {
  go build -tags mysql \
    -ldflags "-X chat/internal/server.buildstamp=`date -u '+%Y%m%dT%H:%M:%SZ'`" \
    -o "$IM_BINARY" ./cmd/im-server &&
  go build -tags mysql -o "$BIN_DIR/init-db" ./cmd/init-db &&
  go build -o "$BIN_DIR/im-cli" ./cmd/im-cli
}

# 初始化 IM 数据库。
init-db() {
  "$BIN_DIR/init-db" \
    -config="$REPO_ROOT/configs/im.yaml" \
    -data="$REPO_ROOT/cmd/init-db/data.json"
}

# 等待指定的本地 TCP 端口开始接受连接。
wait-for() {
  local port=$1
  while ! nc -z localhost $port; do
    sleep 1
  done
}

# 启动三节点 IM 集群。
run-server() {
  "$SCRIPT_DIR/run-cluster.sh" -s "" start && wait-for 16060
}

# 通过 CLI 向指定集群节点发送请求并校验成功响应数量。
send-requests() {
  local expect=12
  local port=$1
  local id=$2
  local outfile=$(mktemp /tmp/im-${id}.txt)
  "$BIN_DIR/im-cli" --host=localhost:${port} --no-login \
    < "$REPO_ROOT/cmd/im-cli/examples/sample-script.txt" \
    > "$outfile" || fail "Test script failed (instance port ${port})"
  num_positive_responses=`grep -c '<= 20[0-9]' $outfile`
  if [ $num_positive_responses -ne expect ]
  then
    fail "Instance ${port}: unexpected number of 20X responses: ${num_positive_responses} (expected ${expected}). Log file ${outfile}"
  fi
  rm $outfile
}

# 捕获意外失败，执行清理并输出错误信息
trap 'cleanup ; fail "For Unexpected Reasons"'\
  HUP INT QUIT PIPE TERM

# 脚本正常终止。
#trap 'cleanup'\
#  EXIT

run_id=`date +%s`
echo "+----------------------------------------------------+"
echo "|                 IM sanity test.                    |"
echo "+----------------------------------------------------+"
echo "Timestamp = ${run_id}"

setup || fail "Test setup failed."
build || fail "Could not build IM binaries"
init-db || fail "Could not initialize IM database"
run-server || fail "Could not start IM server"

# 发送测试请求。
send-requests 16060 $run_id
send-requests 16061 $run_id
send-requests 16062 $run_id

pass
