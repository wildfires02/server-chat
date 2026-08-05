# YAML 配置说明

项目自有配置统一使用 YAML：

- `im.yaml`：开发单机最小配置，只保留当前启用功能和必要参数。
- `admin.yaml`：独立 `im-admin` 管理控制面开发配置。
- `im.cluster-dev.yaml`：本机三节点开发集群配置，依赖本机 etcd 与 MySQL。
- `im.cluster.yaml`：三节点生产集群模板，只保存非敏感拓扑和安全默认值。
- `init-db.yaml`：数据库初始化工具的最小配置。
- `server/db/*/tests/test.yaml`：各数据库集成测试配置。
- `deployments/docker/compose/im.cluster.yaml`：Docker Compose 开发集群配置。

## 配置加载

`im-server` 和 `im-admin` 使用 Viper 只读取 YAML，不解析启动参数，也不应用环境变量
覆盖。需要偏离代码默认值的地址、端口、数据库、密钥和功能开关必须写入对应配置
文件；未使用模块不需要写一整段 `enabled: false` 示例。

`im.yaml` 不再罗列关闭状态的完整示例。未配置项由代码默认值处理；当前删除了本地
文件存储、上传后处理、邮件/短信验证、Push 死信告警、TnPG、TLS 终止和 gRPC
插件示例。TLS 建议交给 Nginx/Caddy，确实需要某项功能时再按对应模块文档加入。
数据库部分保留 MySQL、PostgreSQL 和 MongoDB，切换时只修改
`store_config.use_adapter` 并确认对应构建标签和数据库迁移已准备好。

- `im-server` 依次搜索 `configs/im.yaml`、当前目录的 `im.yaml`、
  `/etc/im/im.yaml`。
- `im-admin` 依次搜索 `configs/admin.yaml`、当前目录的 `admin.yaml`、
  `/etc/im/admin.yaml`。

生产环境应由 Secret 管理系统在进程启动前生成权限受限的完整 YAML 文件，再挂载到
`/etc/im/`；服务进程本身不再拼接或覆盖配置。

### Cloudflare R2 配置来源

`server-chat` 运行时只从自己的 `im.yaml` 读取 Cloudflare R2 配置，不调用
Groupbuying `server`，也不会在运行期间依赖商城数据库。R2 使用 S3 兼容处理器：

```yaml
media:
  use_handler: s3
  handlers:
    s3:
      access_key_id: ""
      secret_access_key: ""
      region: auto
      bucket: ""
      endpoint: "https://<account-id>.r2.cloudflarestorage.com"
      direct_upload: true
      cdn_base_url: "https://media.example.com"
```

- `endpoint` 是包含 Cloudflare Account ID 的 S3 API 地址，不是公开访问域名。
- `cdn_base_url` 是用户访问文件的 R2 自定义域名。
- 商城后台的 Cloudflare 配置仍由商城和后台上传使用，需要单独维护相同的存储桶与域名。
- 修改 R2 密钥、存储桶或域名后必须重启 `server-chat`。

本地开发需要复用 Groupbuying `server` 数据库中的“默认 Cloudflare 配置”时，在
`server-chat` 根目录执行：

```bash
go run ./cmd/sync-r2-config \
  -server-config ../server/config.yaml \
  -im-config configs/im.yaml
```

该命令只在生成配置时执行：它从数据库读取默认 Cloudflare 配置，再使用其中的 API
Token 查询该 R2 桶已启用并完成验证的 Custom Domain，最后更新
`media.handlers.s3`，不会输出 Access Key、Secret Key 或 API Token。S3 API 地址写入
`endpoint`，查询到的自定义域名自动写入 `cdn_base_url`，不需要手工重复填写。同步后
`im-server` 仍只读取自己的 YAML，运行期间不会依赖商城数据库或 Cloudflare 管理 API。

`configs/im.yaml` 同步后含有明文 R2 密钥，只适合本机开发且不能提交。生产环境应由
Secret 管理系统生成 `/etc/im/im.yaml`，不要在服务器上直接连接商城数据库同步。

### Firebase Admin 凭据来源

Firebase 开关、凭据和离线消息时长统一放在一个节点中：

```yaml
firebase:
  enabled: false
  credential_file: "./firebase-adminsdk.json"
  time_to_live: 3600
```

`enabled: false` 时不会读取凭据文件；改为 `true` 后启用 FCM。生产环境不要把私钥
写入数据库或后台表单，应把 Secret 文件挂载为
`/etc/im/firebase-adminsdk.json`。server-chat 不再读取通用 `push` 列表。

### Agora 通话

一对一语音、一对一视频、群组语音和群组视频统一使用 Agora，不需要 STUN、TURN
或 ICE 配置：

```yaml
calls:
  enabled: true
  app_id: ""
  app_certificate: ""
```

未接听超时、Token 有效期、频道前缀和人数限制使用代码默认值；需要调整时才增加
`call_establishment_timeout`、`token_ttl`、`channel_prefix` 或
`max_participants`。

### 消息与附件保留期

聊天主库与管理审计库使用不同的保存目的：聊天主库为用户提供正常的多端历史记录，
管理审计库只保存可倒查的纯文字副本。当前配置把聊天主库也限制为最多 90 天：

```yaml
message_retention:
  enabled: true
  days: 90
  scan_period: 300
  batch_size: 1000
```

- `days`：正文、发送者、搜索文本及附件引用的最长保留天数。
- `scan_period`：后台扫描间隔，单位为秒。
- `batch_size`：单个事务最多清理的消息数；每轮最多连续执行 10 个事务，既能追赶积压，
  也避免长时间占用数据库。
- 到期消息保留不含个人内容的 SeqId 墓碑，Topic 同步游标不会回退。
- 附件引用解除后，由 `media.gc_period` 对 R2 中不再被引用的对象执行垃圾回收。
- `business_policy.audit_endpoint` 对应的管理审计仍只写入文字，图片、语音、视频和文件
  不进入审计库；商城审计接口继续按 90 天过滤并清除过期记录。

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

- `im-server` 暴露聊天前端使用的 `/v0/*`，包括连接、上传、`workspace` 和 `pins`。
- `im-admin` 只暴露后台与服务间调用使用的 `/internal/*`，不注册 `/v0/*`。
- 两个进程必须连接同一个 `store_config`，并使用相同的 `uid_key` 和
  `api_key_salt`。`admin.worker_id` 必须与所有聊天节点的 Snowflake Worker ID
  不同。
- `im-server` 通过 `translation.refresh_interval` 周期读取 `im-admin` 写入共享数据库
  的翻译策略，更新管理配置不需要重启聊天节点。
- 官方频道通过 `/internal/official-topics` 创建和认证，角色分配必须使用该管理接口；
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
地址、令牌和数据库必须直接写入 `configs/admin.yaml`；HTTPS 由统一网关终止。

容器中的服务进程同样只读取最终 YAML。数据库初始化或升级由显式的一次性任务
执行，业务容器启动时不会自动修改 Schema。

## 文件断点续传与可靠处理

`/v0/file/resumable/` 的会话、偏移、Multipart Part 清单和写租约保存在数据库持久缓存中。
使用 `s3` 处理器时，新客户端通过 `Upload-Part-Size` 协商至少 5 MiB 的块，每个 tus
PATCH 由服务端直接流入 S3 `UploadPart`，完成时调用 `CompleteMultipartUpload`；不再保存
中间对象、下载拼接临时文件或二次上传。非 Multipart 处理器仍保留旧分块兼容路径。
S3 配置 `direct_upload: true` 后，`/v0/file/direct/` 还会签发原生 Multipart Upload
分块 URL，浏览器直接上传且服务端只完成签名、合并、ACL 和处理任务登记。
可选 `cdn_base_url` 把 ACL 校验后的下载重定向到 CDN；设置 `cdn_hmac_secret` 时边缘
需校验 `path + "\n" + expires` 的 HMAC-SHA256 URL-safe Base64 签名。集群文件处理必须
使用 `s3` 等共享处理器，配置为节点本地 `fs` 会在启动校验阶段被拒绝。

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
- `control_plane.endpoints` 应列出至少三个可独立访问的 etcd 客户端端点；不要只填写一个成员，也不要填写 `2380` Peer 地址。
- etcd 控制面使用 Lease 注册节点，并以 members 键的修改 Revision 和持久化成员变更 marker 形成专用 Cluster View epoch；无关 etcd 写入不会推动 IM 任期。
- 启用控制面时，PostgreSQL/MySQL 可直接使用数据库 fencing；MongoDB 必须配置 Replica Set。
- 配置门禁由服务启动和 `internal/server` 配置测试共同执行。

## 格式约束

配置加载器只接受 `.yaml` 和 `.yml` 文件，且所有文件的根节点必须是对象，以便统一由 Viper 解析。`.conf`、`.json` 和 `.jsonc` 不再作为配置格式支持。
