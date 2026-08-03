# Docker Compose 开发环境

这里提供两个入口：

- `single-instance.yml`：单机开发服务。
- `cluster.yml`：三 IM 节点 + 三成员 etcd 的开发集群，使用正式的 Lease、fencing、
  gRPC Lane 和 Readiness 代码路径，但不启用生产 mTLS。

Compose 不是生产交付物。生产要求见
[`../../../docs/planning/cluster.md`](../../../docs/planning/cluster.md)。

需要 Docker Compose `2.24.4` 或更高版本。数据库覆盖文件使用官方
`!override` 合并标签，确保切换 PostgreSQL、MongoDB 或 RethinkDB 后不会残留
MySQL 的环境变量和数据卷。

## 启动

```bash
cp .env.example .env

# MySQL 单机。
docker compose -f single-instance.yml up -d

# MySQL 三节点开发集群。
docker compose -f cluster.yml up -d

# 只启动三成员 etcd，供仓库根目录 scripts/run-cluster.sh 使用。
docker compose -f cluster.yml up -d etcd-0 etcd-1 etcd-2

# PostgreSQL。
docker compose \
  -f single-instance.yml \
  -f single-instance.postgres.yml \
  up -d

# MongoDB Replica Set。
docker compose \
  -f cluster.yml \
  -f cluster.mongodb.yml \
  up -d
```

三个 etcd 成员的客户端端口分别映射为 `127.0.0.1:2379`、
`127.0.0.1:22379` 和 `127.0.0.1:32379`。`configs/im.cluster-dev.yaml`
会同时配置这三个端点；Docker 内的 IM 节点则使用 `etcd-0:2379`、
`etcd-1:2379` 和 `etcd-2:2379`。`2380` 是 etcd 成员间 Peer 端口，不能写入
server-chat 的 `control_plane.endpoints`。

RethinkDB 使用同名 `.rethinkdb.yml` 覆盖。Exporter 位于 `observability` profile：

```bash
docker compose -f cluster.yml --profile observability up -d
```

## 数据库变更

数据库工作由 `db-init` 一次性服务完成，三个业务节点不会并发迁移。

```bash
# 升级 Schema。
IM_DB_INIT_MODE=upgrade docker compose -f single-instance.yml run --rm db-init

# 开发环境重置；两个开关必须同时明确。
IM_DB_INIT_MODE=reset \
IM_ALLOW_DESTRUCTIVE_DB_RESET=true \
docker compose -f single-instance.yml run --rm db-init
```

## 校验和日志

```bash
./scripts/validate-delivery.sh
docker compose -f cluster.yml config --quiet
docker compose -f cluster.yml ps
docker compose -f cluster.yml logs -f im-0
curl --fail http://127.0.0.1:6060/readyz
```

容器日志直接进入 Docker 日志驱动，不再写 `/var/log/im.log`。
