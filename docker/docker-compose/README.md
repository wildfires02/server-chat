# Docker Compose 端到端部署指南

本目录包含使用 Docker Compose 在单机或集群环境下部署 IM 服务器的参考配置文件。

---

## 1. 配置文件说明

- **[single-instance.yml](single-instance.yml)**：单机版配置（默认包含 MySQL 数据库、IM 服务和 Exporter 监控导出器）。
- **[cluster.yml](cluster.yml)**：三节点集群版配置（包含 3 个 IM 实例、MySQL 和对应的 Exporter 监控）。
- **覆盖配置文件（用于切换数据库后端）**：
  - PostgreSQL: `single-instance.postgres.yml`, `cluster.postgres.yml`
  - MongoDB: `single-instance.mongodb.yml`, `cluster.mongodb.yml`
  - RethinkDB: `single-instance.rethinkdb.yml`, `cluster.rethinkdb.yml`

---

## 2. 常用启动命令

在当前目录（`docker/docker-compose/`）下运行以下命令：

### MySQL 后端

- **单机模式**：
  ```bash
  docker-compose -f single-instance.yml up -d
  ```
- **集群模式**：
  ```bash
  docker-compose -f cluster.yml up -d
  ```

### PostgreSQL 后端

- **单机模式**：
  ```bash
  docker-compose -f single-instance.yml -f single-instance.postgres.yml up -d
  ```
- **集群模式**：
  ```bash
  docker-compose -f cluster.yml -f cluster.postgres.yml up -d
  ```

### MongoDB 后端

- **单机模式**：
  ```bash
  docker-compose -f single-instance.yml -f single-instance.mongodb.yml up -d
  ```
- **集群模式**：
  ```bash
  docker-compose -f cluster.yml -f cluster.mongodb.yml up -d
  ```

---

## 3. 数据库重置与升级

通过在命令前传入环境变量 `RESET_DB=true` 或 `UPGRADE_DB=true` 来重置或升级数据库。

例如，在 MongoDB 集群中升级数据库：
```bash
UPGRADE_DB=true docker-compose -f cluster.yml -f cluster.mongodb.yml up -d im-0
```

在 MySQL 单机模式下重置数据库：
```bash
RESET_DB=true docker-compose -f single-instance.yml up -d im-0
```

---

## 4. 故障排查与日志

1. 检查最终组合生效的配置：
   ```bash
   docker-compose -f single-instance.yml config
   ```
2. 查看容器运行日志：
   ```bash
   docker logs im-0
   ```
3. 从容器导出日志文件到本地：
   ```bash
   docker cp im-0:/var/log/im.log .
   ```
