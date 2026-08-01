# IM 即时通信服务端

本仓库提供即时通信服务端、数据库适配器、命令行工具、部署模板和测试工具。
服务端支持 WebSocket、HTTP 长轮询和 gRPC 接入，核心业务采用 Topic 顺序处理
模型，并提供单聊、群聊、频道、文件、搜索、推送和音视频信令能力。

> 当前交付状态
>
> - `standalone` 仅用于开发、测试和演示，不得承载生产流量。
> - `cluster` 已具备控制面、可靠传输、健康检查和部署模板，但目标基础设施上的
>   生产认证仍需按操作手册完成。
> - Web、Android 和 iOS 客户端不在本仓库内；`web/static` 只用于放置可选的
>   Web 构建产物。

## 快速开始

本地开发默认使用 MySQL：

```bash
docker run -d --name im-mysql-dev \
  -p 3306:3306 \
  -e MYSQL_ROOT_PASSWORD=123456 \
  mysql:8.0

mkdir -p bin
go build -tags mysql -o bin/init-db ./cmd/init-db
go build -tags mysql -o bin/im-server ./cmd/im-server
go build -tags mysql -o bin/im-admin ./cmd/im-admin

./bin/init-db \
  --config=./configs/im.yaml \
  --data=./cmd/init-db/data.json \
  --reset=true

./bin/im-admin
```

另一个终端启动聊天服务：

```bash
./bin/im-server
```

启动后检查：

```bash
curl --fail http://127.0.0.1:6060/livez
curl --fail http://127.0.0.1:6060/readyz
```

完整步骤和故障排查见[本地启动指南](STARTUP.md)。

## 核心能力

- WebSocket（JSON 或协议 0.33 Protobuf 二进制帧）、HTTP 长轮询和 gRPC 双向流。
- Token + Topic `seq/del` 游标快速恢复，支持固定快照无缺口续页。
- 单聊、群组、广播频道、多设备同步和细粒度访问控制。
- 消息编辑、撤回、回复、转发、反应、置顶和定时消息。
- 100 人以内普通群的逐消息 Seen by 成员与阅读时间查询。
- 本地文件系统与 S3 兼容对象存储。
- WebRTC 点对点信令和 Agora 群组通话服务端令牌。
- 独立 `im-admin` 进程：Casbin 角色权限、Domain 绑定、翻译策略、基础产品策略和
  操作审计。
- MySQL、PostgreSQL、MongoDB 和 RethinkDB 存储适配器。
- 开发单机模式，以及基于 etcd、数据库隔离栅栏和 gRPC 有序通道的集群模式。

## 部署入口

| 场景 | 入口 | 说明 |
| --- | --- | --- |
| 本地开发 | [STARTUP.md](STARTUP.md) | 最短可运行路径 |
| 源码或二进制安装 | [INSTALL.md](INSTALL.md) | 环境、构建标签和初始化 |
| Docker | [deployments/docker/README.md](deployments/docker/README.md) | 本地镜像和容器运行 |
| Docker Compose | [deployments/docker/compose/README.md](deployments/docker/compose/README.md) | 开发单机和开发集群 |
| Kubernetes | [deployments/kubernetes/README.md](deployments/kubernetes/README.md) | 三至五节点生产模板 |

生产集群的发布、滚动升级、证书轮换、扩缩容和回滚以
[生产集群操作手册](docs/cluster-operations.md)为准。

## 文档

统一文档入口见 [docs/README.md](docs/README.md)。常用文档：

- [配置说明](configs/README.md)
- [代码架构](docs/code-architecture.md)
- [服务端协议参考](docs/API.md)
- [接口调用示例](docs/api-examples.md)
- [监控与健康检查](docs/monitoring.md)
- [数据库版本、迁移记录与固定操作流程](docs/database-migrations.md)
- [群消息 Seen by 协议](docs/message-seen-by.md)
- [常见问题](docs/faq.md)
- [统一产品需求与管理后台接口](docs/im-product-requirements.md)

规划、差距分析和历史验收记录位于 `docs/planning/`，不应代替当前操作文档。

## 开发验证

```bash
go test ./...
go test -race ./internal/server ./server/agora ./internal/configutil
go vet ./...
```

修改 Protobuf 定义后运行：

```bash
./api/pbx/go-generate.sh
```

禁止直接编辑 `api/pbx/*.pb.go` 和自动生成的 Mock 文件。
