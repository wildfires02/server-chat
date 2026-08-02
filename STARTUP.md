# 本地开发启动指南

本文提供从空环境到服务可访问的最短路径，只适用于本地开发、自动化测试和
问题复现。生产环境必须使用集群配置和正式部署模板。

## 1. 准备环境

需要：

- Go 1.26 或与 `go.mod` 一致的更新版本。
- Docker，用于启动本地 MySQL；也可以使用已有 MySQL 8.0。
- 未被占用的 `6060`、`6061` 和 `16060` 端口。

启动开发数据库：

```bash
docker run -d --name im-mysql-dev \
  -p 3306:3306 \
  -e MYSQL_ROOT_PASSWORD=123456 \
  mysql:8.0
```

`configs/im.yaml` 使用相同的本地示例密码。该密码只允许用于隔离的
开发环境。

## 2. 构建

在仓库根目录执行：

```bash
mkdir -p bin
go build -tags mysql -o bin/init-db ./cmd/init-db
go build -tags mysql -o bin/im-server ./cmd/im-server
go build -tags mysql -o bin/im-admin ./cmd/im-admin
```

其他数据库的构建标签见[安装与构建](INSTALL.md)。

开发时也可以跳过生成二进制，显式带上与默认配置一致的 MySQL 标签：

```bash
go run -tags mysql cmd/im-admin/main.go
go run -tags mysql cmd/im-server/main.go
```

未提供数据库标签时源码入口同样默认编入 MySQL；显式写出标签更便于核对启动配置。

## 3. 初始化数据库

```bash
./bin/init-db \
  --config=./configs/im.yaml \
  --data=./cmd/init-db/data.json \
  --reset=true
```

此命令会重建 `im` 数据库，只能用于本地或隔离测试库。

## 4. 启动

分别启动管理服务和聊天服务（使用两个终端或进程管理器）：

```bash
./bin/im-admin
```

```bash
./bin/im-server
```

`im-admin` 和 `im-server` 都使用 Viper 只读取 YAML，不接受命令行或环境变量配置
覆盖。前者搜索 `configs/admin.yaml`、`admin.yaml`、`/etc/im/admin.yaml`，后者搜索
`configs/im.yaml`、`im.yaml`、`/etc/im/im.yaml`。

`im-admin` 只提供管理 API，`im-server` 不暴露 `/v0/`。二者连接同一个
数据库，后台保存的翻译策略由聊天服务自动读取。`static_data: "-"` 表示不挂载 Web
静态资源；需要调试独立 Web 客户端时，在 `configs/im.yaml` 中改为实际目录。

## 5. 验证

```bash
curl --fail http://127.0.0.1:6060/livez
curl --fail http://127.0.0.1:6060/readyz
curl --fail http://127.0.0.1:6060/debug/vars
curl --fail \
  -H 'Authorization: Bearer dev-only-change-this-admin-token' \
  http://127.0.0.1:6061/v0/health
```

使用示例账号通过 gRPC 登录：

```bash
go run ./cmd/im-cli \
  -host=127.0.0.1:16060 \
  -login-basic=alice:alice123
```

默认端点：

| 端点 | 地址 |
| --- | --- |
| HTTP、WebSocket、长轮询 | `127.0.0.1:6060` |
| 独立管理 API | `127.0.0.1:6061` |
| gRPC | `127.0.0.1:16060` |
| 存活检查 | `http://127.0.0.1:6060/livez` |
| 就绪检查 | `http://127.0.0.1:6060/readyz` |
| 运行指标 | `http://127.0.0.1:6060/debug/vars` |

## 6. 常见问题

| 现象 | 处理 |
| --- | --- |
| MySQL 连接失败 | 等待容器完成初始化，并核对 `localhost:3306` 和密码 |
| 数据库版本不匹配 | 按[数据库迁移 SOP](docs/database-migrations.md)备份并运行 `./bin/init-db --config=./configs/im.yaml --upgrade=true` |
| `address already in use` | 停止占用进程，或修改 YAML 中的 `listen`、`grpc_listen` |
| 静态目录不存在 | 将 YAML 中的 `static_data` 设为 `"-"`，或提供实际构建目录 |
| `/readyz` 返回失败 | 查看启动日志、数据库连接和运行模式校验结果 |

更多问题见[常见问题](docs/faq.md)。

## 7. 下一步

- 单机模式的测试、基准和安全边界：[docs/standalone.md](docs/standalone.md)
- 服务端配置与环境变量：[configs/README.md](configs/README.md)
- 协议与报文：[docs/API.md](docs/API.md)
- 数据库版本历史与每次升级步骤：[docs/database-migrations.md](docs/database-migrations.md)
- Docker 开发环境：[deployments/docker/compose/README.md](deployments/docker/compose/README.md)
