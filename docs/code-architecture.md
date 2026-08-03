# 代码架构与维护边界

> 文档信息
>
> - 类型：开发参考
> - 文档入口：[文档导航](README.md)

本文说明当前仓库的代码职责、扩展入口和重构约束，避免新功能继续堆叠到少数超大文件。

## 1. 顶层目录

| 目录 | 职责 | 维护要求 |
| --- | --- | --- |
| `cmd/` | 七个可执行程序的入口和专属资源 | `main` 只组装依赖，不承载共享业务 |
| `internal/server/` | 连接、Topic、消息、集群和通话核心 | 对外协议与数据库兼容优先 |
| `internal/configutil/` | 所有命令共享的配置解析 | 不放置具体 Topic 业务 |
| `server/` | 认证、存储、媒体、推送等服务端基础包 | 通过接口或注册器供核心服务使用 |
| `api/pbx/` | 按领域拆分的 Protobuf 协议及生成代码 | 修改任一 `.proto` 后运行 `go-generate.sh`，禁止手改生成文件 |
| `configs/` | 本地开发和初始化配置 | 不保存生产环境密钥 |
| `deployments/` | Docker 镜像和 Compose 部署文件 | 构建上下文统一使用仓库根目录 |
| `scripts/` | 构建、发布、集群和健全性检查 | 从任意工作目录调用时都应定位仓库根目录 |
| `tests/` | 跨包集成与压力测试资源 | 单元测试仍与对应 Go 包同目录 |
| `tools/` | 数据生成等仅供开发使用的辅助程序 | 不参与生产进程启动 |
| `docs/` | API、运维、架构和功能差距文档 | 路径调整时同步校验本地链接 |
| `web/` | 可选的 Web 静态资源 | 生产环境可以由 CDN 独立托管 |

### 1.1 可执行入口

生产和运维命令统一从仓库根目录按 Go 包构建：

| 命令包 | 用途 |
| --- | --- |
| `./cmd/im-server` | IM 主服务 |
| `./cmd/init-db` | 数据库建表、升级和示例数据导入 |
| `./cmd/im-cli` | gRPC 命令行客户端 |
| `./cmd/chatbot` | 自动回复与插件示例 |
| `./cmd/exporter` | Prometheus / InfluxDB 指标导出 |
| `./cmd/keygen` | API Key 生成与校验 |
| `./cmd/rest-auth` | 外部 REST 认证服务示例 |

开发辅助命令放在 `tools/`，例如 `go run ./tools/generate-dataset`；它们不进入正式发布包。

## 2. 服务端核心边界

### 2.1 连接层

- `hdl_websock.go`：WebSocket 连接。
- `hdl_longpoll.go`：HTTP 长轮询。
- `hdl_grpc.go`：gRPC 双向流。
- `session.go`、`session_auth.go`、`session_dispatch.go`：连接身份、认证和请求分发。
- `session_queue.go`、`session_serialize.go`：出站队列和协议序列化。
- `sessionstore.go`：会话索引与过期清理。

连接层只负责协议收发、认证上下文和路由，不应直接实现消息、搜索或通话业务。

### 2.2 Topic 层

- `hub.go`：Topic 的创建、查找和跨节点路由。
- `topic.go`：Topic 状态与主事件循环。
- `topic_sub.go`、`topic_sub_*.go`、`topic_roles.go`：订阅、群成员和访问控制。
- `topic_msg.go`：普通消息保存与广播。
- `topic_meta.go`、`topic_meta_*.go`：描述、订阅、历史和删除等元数据请求。
- `search.go`：用户、群组发现和当前 Topic 全文搜索。

Topic 内存状态只能由 Topic 主循环修改。后台任务和连接处理器应通过现有通道
投递请求，避免跨协程直接写入。

### 2.3 消息能力

- `message_features.go`：消息头、正文分析、回复、转发、相册和发布前校验。
- `message_schedule.go`：定时投递与 `client_id` 幂等。
- `message_edit.go`：消息编辑和附件关联更新。
- `message_interactions.go`：反应、置顶及在线通知。
- `scheduled_messages.go`：持久化队列扫描和到期投递。

客户端提交的扩展字段必须先转换成服务端管理的消息头；服务端私有字段不得原样信任或向无权限用户泄露。

### 2.4 音视频通话

- `calls.go`：公共状态、生命周期和提供方分发。
- `calls_config.go`：统一的 Agora 配置。
- `calls_agora.go`：Agora 一对一/群组通话、ACL 角色和 Token。
- `server/agora/`：独立的 AccessToken2 编码与签名。

新增媒体提供方时，应增加提供方专属状态和处理文件，不把 SDK 字段继续加入公共参与者结构。

### 2.5 集群层

- `cluster_control_plane*.go`：etcd 租约、成员拓扑、监听和运维操作。
- `cluster_transport_*.go`：节点间协议、有序通道、去重和双向流。
- `cluster_membership.go`、`cluster_routing.go`：成员视图与 Topic 路由。
- `cluster_tls.go`：节点双向 TLS 和证书身份校验。
- `topic_proxy.go`：远端 Topic 的本地代理。

控制面负责成员身份和拓扑，数据面负责业务请求传输。任何新写路径都必须先通过
当前成员视图和数据库隔离栅栏，不能在失去多数派时退化为无保护写入。

### 2.6 存储层

- `server/store/`：业务使用的统一存储接口。
- `server/db/common/`：适配器共享逻辑和测试数据。
- `server/db/mysql/`、`server/db/postgres/`、`server/db/mongodb/`、`server/db/rethinkdb/`：数据库实现。

业务层只依赖 `store` 接口。数据库专属类型、查询语法和迁移逻辑不得进入 Topic 或 Session 层。

## 3. 公共基础能力

`internal/configutil` 是 YAML 配置解析入口。`im-server` 和 `im-admin` 使用
`DecodeFileConfigOnly`，只接受完整 YAML，不应用环境变量覆盖；数据库工具仍可使用
兼容加载入口。配置加载器只接受 `.yaml` 和 `.yml`，解析行为需要新增或修复时只修改
这一处并补充测试。

## 4. 不纳入人工拆分的文件

以下文件属于机械生成产物，不按普通业务文件的行数标准处理：

- `api/pbx/*.pb.go`
- `server/store/mock_store/mock_store.go`

生成文件应通过源协议或生成器更新。数据库适配器按缓存、凭据、设备、文件、
消息、订阅、主题和用户等领域拆分；新增查询应放入对应领域文件，不能重新堆回
入口文件。

## 5. 提交前验证

```bash
go test ./...
go test -race ./internal/server ./server/agora ./internal/configutil
go vet ./...
```

带构建标签的数据库集成测试至少应完成编译检查；连接真实数据库的测试在对应 CI 环境执行。

文档入口、运行参数或文件路径变化时，还应同步更新
[`docs/README.md`](README.md)、根目录启动文档和对应组件说明。
