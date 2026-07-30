# 开发单机版使用说明

> 文档信息
>
> - 类型：开发操作手册
> - 适用环境：`development`、`test`
> - 生产使用：禁止

单机版只用于本地开发、自动化测试、演示和问题复现，禁止承载生产流量。它与集群版使用同一个 `im-server` 二进制和业务实现，但由
`runtime.deployment_mode: standalone` 显式跳过控制面、节点间监听器、gRPC Lane、Topic Proxy 和集群队列。

## 1. 准备 MySQL

下面的容器只适合本地开发：

```bash
docker run -d --name im-mysql \
  -p 3306:3306 \
  -e MYSQL_ROOT_PASSWORD=123456 \
  mysql:8.0
```

数据库地址、账号和密码直接修改 `configs/im.yaml`。

## 2. 构建和初始化

```bash
mkdir -p bin
go build -tags mysql -o bin/init-db ./cmd/init-db
go build -tags mysql -o bin/im-server ./cmd/im-server

./bin/init-db \
  --config=./configs/im.yaml \
  --data=./cmd/init-db/data.json \
  --reset=true
```

`--reset=true` 会重建目标数据库，只能用于本地或隔离测试库。

## 3. 启动

```bash
./bin/im-server
```

需要 Web 静态资源时，把 `configs/im.yaml` 中的 `static_data` 改为
`./web/static`；不需要时设为 `"-"`。

启动日志必须包含：

```text
Deployment environment 'development', mode 'standalone'
Cluster: running as a standalone server.
```

`configs/im.yaml` 不包含 `cluster_config`。服务端根据显式部署模式跳过集群初始化，不再通过 `self` 是否为空猜测单机模式。

## 4. 验证

```bash
curl --fail http://127.0.0.1:6060/livez
curl --fail http://127.0.0.1:6060/readyz
curl --fail http://127.0.0.1:6060/debug/vars

go run ./cmd/im-cli \
  -host=127.0.0.1:16060 \
  -login-basic=alice:alice123
```

`/debug/vars` 应显示：

- `RuntimeEnvironment` 为 `development`。
- `DeploymentMode` 为 `standalone`。
- `TotalClusterNodes`、`LiveClusterNodes` 和 `ClusterConnectedLanes` 为 0。

WebSocket、Long Polling 和 gRPC 都使用同一本地 Hub、Topic Sequencer、数据库和 Session 投递逻辑。

## 5. 回归和微基准

```bash
./scripts/test-standalone.sh

IM_STANDALONE_BENCHMARK=1 ./scripts/test-standalone.sh
```

回归脚本会执行单机配置门禁、本地 Resolver、慢 Session 背压、热点 Fanout、共享业务测试和数据竞争检查。微基准只用于开发对比，不代表生产容量承诺。

完整真实进程回归无需手工启动数据库或服务：

```bash
./scripts/test-standalone-process.sh
```

脚本会创建专属 MySQL 临时容器，构建并初始化数据库，自动拉起两轮单机服务，执行：

- WebSocket、Long Polling 和 gRPC 的握手、登录、建群、发布和幂等重试。
- text、drafty、image、video、voice、audio、file 的真实发布、历史和删除。
- 64 路重连、真实热点 Topic 网络投递，以及跨 Ping 周期的 256 条连接和 GC 采样。
- MySQL 分级延迟、连接池耗尽/恢复、完全失联时 `readyz=503` 和恢复后 `200`。
- ACK 后立即 SIGKILL、同库新进程历史恢复和 Client ID 原 seq 核对。
- 最终 SIGTERM 优雅关闭和完整资源清理。

默认端口、镜像和测试规模可覆盖：

```bash
IM_STANDALONE_E2E_MYSQL_IMAGE=mysql:8.0 \
IM_STANDALONE_E2E_HTTP_PORT=26060 \
IM_STANDALONE_E2E_GRPC_PORT=26061 \
IM_STANDALONE_E2E_IDLE_CONNECTIONS=256 \
IM_STANDALONE_E2E_IDLE_HOLD=60s \
IM_STANDALONE_E2E_HOT_RECEIVERS=16 \
IM_STANDALONE_E2E_HOT_MESSAGES=100 \
./scripts/test-standalone-process.sh
```

脚本只删除自己生成的唯一容器名和临时目录，不会操作已有开发数据库。首次使用的 MySQL 镜像若未缓存，Docker 会先拉取镜像。

## 6. 安全边界

- `production + standalone` 和 `staging + standalone` 会在监听端口之前失败。
- 单机模式没有高可用、在线扩容、多数派、Owner 迁移或跨节点容灾。
- 示例签名密钥、数据库密码和本地媒体目录均不能复制到生产。
- 需要正式业务流量时必须使用 `configs/im.cluster.yaml` 和生产集群部署物。
