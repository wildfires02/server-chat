#!/bin/bash

BINARY_PATH=$GOPATH/bin
IM_BINARY=$BINARY_PATH/server

# 终止并清理所有运行中的容器。
cleanup() {
  ./run-cluster.sh stop
  if [ -f "./server" ]; then
    rm ./server
  fi
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

  # 确保当前目录下不存在 IM 服务器二进制文件。
  if [ -f "./server" ]; then
    rm ./server
  fi
}

# 编译 IM 二进制文件。
build() {
  go install -tags mysql -ldflags "-X main.buildstamp=`date -u '+%Y%m%dT%H:%M:%SZ'`" \
    chat/init-db \
    chat/server && \
  ln -s $IM_BINARY
}

# 初始化 IM 数据库。
init-db() {
  $GOPATH/bin/init-db -config=./im.conf -data=../init-db/data.json
}

wait-for() {
  local port=$1
  while ! nc -z localhost $port; do
    sleep 1
  done
}

# 启动三节点 IM 集群。
run-server() {
  ./run-cluster.sh -s "" start && wait-for 16060
}

send-requests() {
  local expect=12
  local port=$1
  local id=$2
  local outfile=$(mktemp /tmp/im-${id}.txt)
  pushd .
  cd ../cli
  go run . --host=localhost:${port} --no-login < sample-script.txt > $outfile || fail "Test script failed (instance port ${port})"
  popd
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
