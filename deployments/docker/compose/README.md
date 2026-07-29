# Docker Compose 开发环境

这里提供两个入口：

- `single-instance.yml`：单机开发服务。
- `cluster.yml`：三 IM 节点 + etcd 的开发集群，使用正式的 Lease、fencing、
  gRPC Lane 和 Readiness 代码路径，但不启用生产 mTLS。

Compose 不是生产交付物。生产要求见
[`../../../docs/planning/cluster.md`](../../../docs/planning/cluster.md)。

## 启动

```bash
cp .env.example .env

# MySQL 单机。
docker compose -f single-instance.yml up -d

# MySQL 三节点开发集群。
docker compose -f cluster.yml up -d

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
