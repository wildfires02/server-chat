# 本地开发启动指南

本文提供从空环境到服务可访问的最短路径，只适用于本地开发、自动化测试和
问题复现。生产环境必须使用集群配置和正式部署模板。

## 1. 准备环境

需要：

- Go 1.26 或与 `go.mod` 一致的更新版本。
- Docker，用于启动本地 MySQL；也可以使用已有 MySQL 8.0。
- 未被占用的 `6060` 和 `16060` 端口。

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
```

其他数据库的构建标签见[安装与构建](INSTALL.md)。

## 3. 初始化数据库

```bash
./bin/init-db \
  --config=./configs/im.yaml \
  --data=./cmd/init-db/data.json \
  --reset=true
```

此命令会重建 `im` 数据库，只能用于本地或隔离测试库。

## 4. 校验并启动

先校验配置：

```bash
./bin/im-server \
  --config=./configs/im.yaml \
  --validate_config
```

再启动服务：

```bash
./bin/im-server \
  --config=./configs/im.yaml \
  --static_data=-
```

`--static_data=-` 表示不挂载 Web 静态资源。需要调试独立 Web 客户端时，将其
构建产物放入 `web/static`，并改为 `--static_data=./web/static`。

## 5. 验证

```bash
curl --fail http://127.0.0.1:6060/livez
curl --fail http://127.0.0.1:6060/readyz
curl --fail http://127.0.0.1:6060/debug/vars
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
| gRPC | `127.0.0.1:16060` |
| 存活检查 | `http://127.0.0.1:6060/livez` |
| 就绪检查 | `http://127.0.0.1:6060/readyz` |
| 运行指标 | `http://127.0.0.1:6060/debug/vars` |

## 6. 常见问题

| 现象 | 处理 |
| --- | --- |
| MySQL 连接失败 | 等待容器完成初始化，并核对 `localhost:3306` 和密码 |
| 数据库版本不匹配 | 备份后运行 `init-db --upgrade=true`；开发库可重置 |
| `address already in use` | 停止占用进程，或使用 `--listen`、`--grpc_listen` 更换端口 |
| 静态目录不存在 | 使用 `--static_data=-`，或提供实际构建目录 |
| `/readyz` 返回失败 | 查看启动日志、数据库连接和运行模式校验结果 |

更多问题见[常见问题](docs/faq.md)。

## 7. 下一步

- 单机模式的测试、基准和安全边界：[docs/standalone.md](docs/standalone.md)
- 服务端配置与环境变量：[configs/README.md](configs/README.md)
- 协议与报文：[docs/API.md](docs/API.md)
- Docker 开发环境：[deployments/docker/compose/README.md](deployments/docker/compose/README.md)
