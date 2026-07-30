# Docker 交付说明

Docker 目录只提供开发、CI 和镜像构建能力。生产集群使用
[`../kubernetes/`](../kubernetes/)，不得把 Compose 当作生产高可用方案。

## 镜像特性

- 构建阶段固定为 Go 1.26.5 + Alpine 3.23，运行阶段固定为 Alpine 3.23.5。
- 服务以 UID/GID `10001` 非 root 运行，日志输出到 stdout/stderr。
- `im-server` 容器直接读取 `/etc/im/im.yaml`，运行时不接受参数或环境变量覆盖。
- 业务容器默认 `IM_DB_INIT_MODE=skip`，不会在启动时自动建库或迁移。
- 数据库初始化、升级和重置由显式的一次性容器执行。

## 构建

```bash
# 精确版本，不会自动创建 latest。
./scripts/docker-build.sh --tag v0.29.0 --db mysql

# 构建全部数据库镜像、Chatbot 和 Exporter。
./scripts/docker-build.sh --tag v0.29.0
```

支持 `mysql`、`postgres`、`mongodb`、`rethinkdb` 和 `alldbs`。默认目标平台为
`linux/amd64`，可用 `--platform linux/arm64` 覆盖。

## 运行时配置

主要容器控制变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `IM_CONFIG_FILE` | `/etc/im/im.yaml` | 规范 YAML 路径 |
| `IM_STATIC_DIR` | `/opt/im/static` | 静态资源目录 |
| `IM_DB_INIT_MODE` | `skip` | `skip/check/init/upgrade/reset` |
| `IM_DB_WAIT_FOR` | 空 | 初始化任务等待的 `host:port` |
| `IM_DB_WAIT_TIMEOUT` | `120` | 依赖等待超时秒数 |
| `IM_DB_SAMPLE_DATA` | 空 | 仅显式初始化时加载的数据 |
| `IM_RUN_SERVER` | `true` | 一次性数据库任务设为 `false` |
| `IM_VALIDATE_CONFIG` | `true` | 启动业务进程前执行离线门禁 |

配置字段继续使用 Viper 规则，例如：

```bash
IM_STORE_CONFIG__USE_ADAPTER=postgres
IM_STORE_CONFIG__ADAPTERS__POSTGRES__DSN='postgresql://...'
IM_AUTH_CONFIG__TOKEN__KEY='base64-secret'
```

执行 `reset` 必须额外设置 `IM_ALLOW_DESTRUCTIVE_DB_RESET=true`，防止拼错变量
意外清空数据库。

## Compose

复制环境示例并启动：

```bash
cd deployments/docker/compose
cp .env.example .env
docker compose -f single-instance.yml up -d

# 可选 Prometheus Exporter。
docker compose -f single-instance.yml --profile observability up -d
```

详细数据库覆盖和集群开发模式见 [Compose 文档](compose/README.md)。
