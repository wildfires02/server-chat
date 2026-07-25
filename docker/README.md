# 使用 Docker 运行 IM 服务

本指南说明如何使用 Docker 在本地构建和运行 IM 聊天服务器及其关联组件。

> [!NOTE]
> 当前 Docker 构建采用基于本地源码的多阶段构建（Multi-stage Build），无需从网络下载预编译二进制包。

---

## 1. 准备工作

1. 安装 [Docker](https://docs.docker.com/install/)（建议版本 20.10+）。
2. 创建 Docker 桥接网络，用于连接 IM 容器与数据库容器：
   ```bash
   docker network create im-net
   ```

---

## 2. 启动数据库容器

根据项目需求选择以下一种数据库后端并运行容器（加入 `im-net` 网络）：

### 1) **MySQL**（推荐 8.0+）
```bash
docker run --name mysql --network im-net --restart always \
  --env MYSQL_ALLOW_EMPTY_PASSWORD=yes -d mysql:8.0
```

### 2) **PostgreSQL**（13+）
```bash
docker run --name postgres --network im-net --restart always \
  --env POSTGRES_PASSWORD=postgres -d postgres:15
```

### 3) **MongoDB**（4.4+，需启用副本集）
```bash
docker run --name mongodb --network im-net --restart always \
  -d mongo:latest --replSet "rs0"

# 进入 mongo shell 初始化副本集
docker exec -it mongodb mongosh --eval 'rs.initiate({"_id": "rs0", "members": [{"_id": 0, "host": "mongodb:27017"}]})'
```

### 4) **RethinkDB**
```bash
docker run --name rethinkdb --network im-net --restart always -d rethinkdb:2.4
```

---

## 3. 构建并运行 IM 服务器容器

### 从本地源码构建镜像

在项目根目录下，使用对应的数据库 Tag 编译本地源码：

- **MySQL 后端**：
  ```bash
  docker build -f docker/im/Dockerfile --build-arg TARGET_DB=mysql -t im/im-mysql:latest .
  ```
- **PostgreSQL 后端**：
  ```bash
  docker build -f docker/im/Dockerfile --build-arg TARGET_DB=postgres -t im/im-postgres:latest .
  ```
- **MongoDB 后端**：
  ```bash
  docker build -f docker/im/Dockerfile --build-arg TARGET_DB=mongodb -t im/im-mongodb:latest .
  ```
- **全数据库适配器版 (`alldbs`)**：
  ```bash
  docker build -f docker/im/Dockerfile --build-arg TARGET_DB=alldbs -t im/im:latest .
  ```

或者直接使用项目根目录下的脚本一键构建所有镜像：
```bash
./docker-build.sh tag=v0.25.0
```

### 运行 IM 容器

以 MySQL 为例：
```bash
docker run -p 6060:6060 -d --name im-srv --network im-net im/im-mysql:latest
```

如果使用 `alldbs` 镜像，需要通过环境变量 `STORE_USE_ADAPTER` 指定使用的数据库：
```bash
docker run -p 6060:6060 -d --name im-srv --network im-net \
  --env STORE_USE_ADAPTER=mysql im/im:latest
```

---

## 4. 测试与验证

用浏览器打开 [http://localhost:6060/](http://localhost:6060/) 测试服务器是否正常运行。

---

## 5. 高级配置

### 挂载外部配置文件

如果默认的配置模板不满足需求，可以挂载外部配置文件：
```bash
docker run -p 6060:6060 -d --name im-srv --network im-net \
  --volume /path/to/custom_im.conf:/etc/im/im.conf \
  --env EXT_CONFIG=/etc/im/im.conf \
  im/im-mysql:latest
```

### 重置或升级数据库

当服务器升级导致数据库 Schema 变更（例如提示 `Invalid database version...`），可以重置或升级数据库：

1. 停止并删除旧容器：
   ```bash
   docker stop im-srv && docker rm im-srv
   ```
2. 加上 `--env UPGRADE_DB=true`（升级）或 `--env RESET_DB=true`（重置）重新运行容器：
   ```bash
   docker run -p 6060:6060 -d --name im-srv --network im-net \
     --env UPGRADE_DB=true \
     im/im-mysql:latest
   ```

### 运行 Go 聊天机器人 (Chatbot)

机器人已全面升级为 Go 语言原生实现。在项目根目录下构建并运行：

1. **构建机器人镜像**：
   ```bash
   docker build -f docker/chatbot/Dockerfile -t im/chatbot:latest .
   ```
2. **运行机器人容器**：
   ```bash
   docker run -d --name im-chatbot --network im-net \
     --env IM_HOST=im-srv:16060 \
     --volume botdata:/botdata \
     im/chatbot:latest
   ```

### 运行 Prometheus / InfluxDB 指标导出器 (Exporter)

1. **构建导出器镜像**：
   ```bash
   docker build -f docker/exporter/Dockerfile -t im/exporter:latest .
   ```
2. **运行导出器容器**：
   ```bash
   docker run -p 6222:6222 -d --name im-exporter --network im-net \
     --env SERVE_FOR=prometheus \
     --env IM_ADDR=http://im-srv:6060/stats/expvar/ \
     im/exporter:latest
   ```

---

## 6. 支持的环境变量一览表

| 环境变量 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `STORE_USE_ADAPTER` | 字符串 | `$TARGET_DB` | 数据库适配器名称 (`mysql`, `postgres`, `mongodb`, `rethinkdb`) |
| `MYSQL_DSN` | 字符串 | `root@tcp(mysql)/im?...` | MySQL 数据库连接串 |
| `POSTGRES_DSN` | 字符串 | `postgresql://postgres:postgres@postgres:5432/im?...` | PostgreSQL 数据库连接串 |
| `RESET_DB` | 布尔值 | `false` | 是否重置（清空并重新初始化）数据库 |
| `UPGRADE_DB` | 布尔值 | `false` | 是否升级数据库 Schema |
| `NO_DB_INIT` | 布尔值 | `false` | 是否跳过数据库初始化 |
| `SAMPLE_DATA` | 字符串 | `data.json` | 初始化的示例数据文件名 |
| `EXT_CONFIG` | 字符串 |空 | 外部配置文件路径（若指定则忽略绝大部分特定环境变量） |
| `EXT_STATIC_DIR` | 字符串 |空 | 外部静态资源文件路径（Web 客户端文件） |
| `AUTH_TOKEN_KEY` | 字符串 | Base64字符串 | 签名身份验证 Token 的秘钥盐值 |
| `API_KEY_SALT` | 字符串 | Base64字符串 | API Key 生成盐值 |
| `UID_ENCRYPTION_KEY` | 字符串 | Base64字符串 | 用户 ID 加密 key |
| `FCM_PUSH_ENABLED` | 布尔值 | `false` | 是否启用 FCM 推送 |
| `FCM_CRED_FILE` | 字符串 |空 | FCM 服务账号 JSON 凭据文件路径 |
| `WEBRTC_ENABLED` | 布尔值 | `false` | 是否启用视频通话 WebRTC 功能 |
| `ICE_SERVERS_FILE` | 字符串 |空 | ICE / STUN / TURN 服务器配置文件路径 |

---

## 7. Docker Compose 支持

关于 Docker Compose 的单机及集群部署，请参阅 [docker-compose 说明文档](./docker-compose/README.md)。
