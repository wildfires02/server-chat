# 安装与构建

本文只说明运行环境、源码构建、数据库初始化和发布包安装。启动参数见
[本地启动指南](STARTUP.md)，容器和生产部署见[文档导航](docs/README.md)。

## 1. 环境要求

| 组件 | 要求 |
| --- | --- |
| Go | 1.26 或与 `go.mod` 一致的更新版本 |
| MySQL | 5.7 及以上，推荐 8.0；使用 InnoDB 和 `utf8mb4` |
| PostgreSQL | 12 及以上，推荐 13 以上 |
| MongoDB | 4.4 及以上；集群或生产场景必须启用副本集 |
| RethinkDB | 兼容保留，不作为首选生产数据库 |

只需安装并选择一种数据库。生产集群优先使用 PostgreSQL 或 MySQL；具体约束见
[生产集群计划](docs/planning/cluster.md)。

## 2. 从源码构建

在仓库根目录创建输出目录：

```bash
mkdir -p bin
```

根据数据库选择构建标签：

| 数据库 | 构建命令 |
| --- | --- |
| MySQL | `go build -tags mysql -o bin/im-server ./cmd/im-server` |
| PostgreSQL | `go build -tags postgres -o bin/im-server ./cmd/im-server` |
| MongoDB | `go build -tags mongodb -o bin/im-server ./cmd/im-server` |
| RethinkDB | `go build -tags rethinkdb -o bin/im-server ./cmd/im-server` |

数据库初始化工具必须使用相同标签。例如 MySQL：

```bash
go build -tags mysql -o bin/init-db ./cmd/init-db
```

需要在同一发布包中包含全部适配器时：

```bash
go build \
  -tags "mysql postgres mongodb rethinkdb" \
  -o bin/im-server \
  ./cmd/im-server

go build \
  -tags "mysql postgres mongodb rethinkdb" \
  -o bin/init-db \
  ./cmd/init-db
```

## 3. 配置数据库

开发单机配置位于 [`configs/im.yaml`](configs/im.yaml)。
生产模板位于 [`configs/im.cluster.yaml`](configs/im.cluster.yaml)。

确认以下字段与实际数据库一致：

```yaml
store_config:
  use_adapter: mysql
  adapters:
    mysql:
      user: root
      passwd: "你的密码"
      addr: localhost:3306
      dbname: im
```

密码、密钥和地址可通过 `IM_` 环境变量覆盖。规则见
[配置说明](configs/README.md)。

## 4. 初始化数据库

首次安装时运行：

```bash
./bin/init-db \
  --config=./configs/im.yaml \
  --data=./cmd/init-db/data.json
```

开发环境需要重建数据库时：

```bash
./bin/init-db \
  --config=./configs/im.yaml \
  --data=./cmd/init-db/data.json \
  --reset=true
```

`--reset=true` 会删除并重建目标数据库，禁止对生产库使用。升级已有数据库应先
完成备份，再使用 `--upgrade=true`。完整参数见
[数据库初始化工具](cmd/init-db/README.md)。

## 5. 校验安装

启动服务前先执行配置门禁：

```bash
./bin/im-server \
  --config=./configs/im.yaml \
  --validate_config
```

校验成功后按照以下文档继续：

- 本地开发：[STARTUP.md](STARTUP.md)
- Docker：[deployments/docker/README.md](deployments/docker/README.md)
- Kubernetes：[deployments/kubernetes/README.md](deployments/kubernetes/README.md)
- systemd：[deployments/systemd/README.md](deployments/systemd/README.md)

## 6. 使用发布包

如果发布渠道提供预编译压缩包，应同时取得与目标操作系统和数据库匹配的
`im-server`、`init-db`、配置模板及校验文件。

安装前必须：

1. 校验发布包摘要或签名。
2. 确认二进制包含目标数据库适配器。
3. 将示例密码、令牌密钥和接口密钥替换为部署环境专用值。
4. 先独立执行数据库迁移，再启动业务节点。

生产环境不得直接使用开发单机配置或示例密钥。

## 7. 协议代码生成

只有修改 `api/pbx/*.proto` 时才需要安装 Protobuf 和 Go 代码生成器。生成命令：

```bash
./api/pbx/go-generate.sh
```

生成后必须重新运行 `go test ./api/pbx ./internal/server`。不要手工修改生成文件。
