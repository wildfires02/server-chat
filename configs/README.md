# YAML 配置说明

项目自有配置统一使用 YAML：

- `im.yaml`：唯一的开发单机配置，包含完整功能示例且不包含集群参数。
- `admin.yaml`：独立 `im-admin` 管理控制面开发配置。
- `im.cluster-dev.yaml`：本机三节点开发集群配置，依赖本机 etcd 与 MySQL。
- `im.cluster.yaml`：三节点生产集群模板，只保存非敏感拓扑和安全默认值。
- `init-db.yaml`：数据库初始化工具的最小配置。
- `ice-servers.example.yaml`：使用 `ice_servers` 对象根节点的独立 WebRTC ICE 服务器示例。
- `server/db/*/tests/test.yaml`：各数据库集成测试配置。
- `deployments/docker/compose/im.cluster.yaml`：Docker Compose 开发集群配置。

## 配置加载

`im-server` 和 `im-admin` 使用 Viper 只读取 YAML，不解析启动参数，也不应用环境变量
覆盖。地址、端口、数据库、密钥、TLS、日志、静态目录和诊断开关都必须出现在对应
配置文件中。

- `im-server` 依次搜索 `configs/im.yaml`、当前目录的 `im.yaml`、
  `/etc/im/im.yaml`。
- `im-admin` 依次搜索 `configs/admin.yaml`、当前目录的 `admin.yaml`、
  `/etc/im/admin.yaml`。

生产环境应由 Secret 管理系统在进程启动前生成权限受限的完整 YAML 文件，再挂载到
`/etc/im/`；服务进程本身不再拼接或覆盖配置。

## 独立管理后台

管理 API 不再挂载到聊天进程。两个程序使用不同入口和配置：

- `im-server`：读取 `im.yaml`，负责聊天连接、消息投递和翻译执行，默认端口
  `6060`。
- `im-admin`：通过 Viper 固定读取 `configs/admin.yaml`，负责管理配置、供应商
  连通性测试和审计，默认端口 `6061`。

`configs/admin.yaml` 中的 `admin` 节点控制独立管理服务：

```yaml
admin:
  enabled: true
  worker_id: 1023
  bootstrap_token: dev-only-change-this-admin-token
  allowed_origins:
    - http://localhost:4173
```

- `/v0/admin/` 和 `/v0/internal/` 只由 `im-admin` 暴露；`im-server` 不注册这些路由。
- 两个进程必须连接同一个 `store_config`，并使用相同的 `uid_key` 和
  `api_key_salt`。`admin.worker_id` 必须与所有聊天节点的 Snowflake Worker ID
  不同。
- `im-server` 通过 `translation.refresh_interval` 周期读取 `im-admin` 写入共享数据库
  的翻译策略，更新管理配置不需要重启聊天节点。
- 官方频道通过 `/v0/admin/official-topics` 创建和认证，角色分配必须使用该管理接口；
  普通聊天协议不能任命官方管理员或发布者。
- 开发令牌只能用于本地；预发布和生产令牌至少 32 字符，并应由 Secret 管理系统
  写入权限受限的最终 `admin.yaml`。
- `allowed_origins` 必须逐项声明，禁止 `*`；非开发环境只接受 HTTPS 来源。
- 一个部署环境通常只运行一个受控的 `im-admin` 写实例。
- Groupbuying 身份与权限同步在最终联调阶段替换 Bootstrap 令牌和本地策略 Repository。

两个二进制独立构建、独立启动：

```bash
go build -tags mysql -o bin/im-server ./cmd/im-server
go build -tags mysql -o bin/im-admin ./cmd/im-admin

./bin/im-admin
./bin/im-server
```

`im-admin` 不接受 `--config`、`--listen` 等运行参数，也不读取环境变量覆盖。监听
地址、日志格式、令牌、数据库和 TLS 都必须直接写入 `configs/admin.yaml`。

容器中的服务进程同样只读取最终 YAML。数据库初始化或升级由显式的一次性任务
执行，业务容器启动时不会自动修改 Schema。

## 文件断点续传与可靠处理

`/v0/file/resumable/` 的会话、偏移、分块清单和写租约保存在数据库持久缓存中。分块
本体写入 `media.use_handler` 指定的媒体存储，因此生产和预发布集群必须使用 `s3`
等共享处理器，配置为节点本地 `fs` 会在启动校验阶段被拒绝。

`media.processing` 的任务同样持久化。`poll_interval` 是任务扫描秒数，
`max_attempts` 是最大执行次数，`retry_base` 是指数退避基数秒数，
`lease_seconds` 必须覆盖单次处理超时；未显式配置时服务端会使用安全默认值。

## 部署模式

服务端配置必须显式提供：

```yaml
runtime:
  environment: development
  deployment_mode: standalone
```

- `environment` 支持 `development`、`test`、`staging` 和 `production`。
- `deployment_mode` 支持 `standalone` 和 `cluster`。
- `staging`、`production` 只能使用集群模式。
- 生产集群必须配置 Cluster ID、至少 3 个奇数节点、广播地址和 etcd 控制面。
- etcd 控制面使用 Lease 注册节点，并以 members 键的修改 Revision 和持久化成员变更 marker 形成专用 Cluster View epoch；无关 etcd 写入不会推动 IM 任期。
- 启用控制面时，PostgreSQL/MySQL 可直接使用数据库 fencing；MongoDB 必须配置 Replica Set；RethinkDB 会被启动门禁拒绝。
- 配置门禁由服务启动和 `internal/server` 配置测试共同执行。

## 格式约束

配置加载器只接受 `.yaml` 和 `.yml` 文件，且所有文件的根节点必须是对象，以便统一由 Viper 解析。`.conf`、`.json` 和 `.jsonc` 不再作为配置格式支持。
