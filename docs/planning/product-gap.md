# 产品能力差距分析

> 文档信息
>
> - 分析日期：2026-07-30
> - 类型：产品规划参考，不作为当前功能承诺
> - 对照对象：Telegram 当前公开产品能力
> - 分析范围：本仓库中的服务端、命令行工具、示例程序、部署、监控和测试代码
> - 状态标记：已实现、部分实现、未实现
> - 需求权威来源：[`docs/im-product-requirements.md`](../im-product-requirements.md)；
>   本文只做差距和优先级分析，不另行定义功能需求

## 1. 结论摘要

当前项目已经具备一个可运行、可扩展的即时通讯服务端骨架，适合继续建设私有化 IM 产品，但与 Telegram 当前完整产品仍有较大差距。

综合估算：

- 基础云聊天服务端完成度：约 **70%–80%**
- 包含客户端的端到端聊天产品完成度：约 **30%–40%**
- 对照 Telegram 全部产品能力：约 **15%–20%**
- 整体仍有约 **80% 的能力差距**

本轮提出的三项基础服务端范围均已形成闭环：

- “群组与频道 ACL、群成员管理、普通群组、只读广播频道”在本次限定范围内可按 **100%** 计。
- “用户和群组 Tag 搜索”已经扩展为公开 alias、群组/频道名称发现和当前会话消息全文搜索，在本次基础服务端范围内可按 **100%** 计。
- “群组语音/视频通话使用 Agora”已经完成服务端 AccessToken2、群 ACL 角色、加入/离开/续期和状态持久化；正式客户端 Agora SDK 接入仍待完成。

协议 `0.30` 又补充了三类基础服务端能力：

- 联系人 CRUD、联系人分组、好友申请/接受/删除、共同好友推荐和版本化多设备同步。
- 文件所有权与 Topic ACL、安全扫描状态、后台预览/转码、文件元数据查询和断点续传。
- 贴纸、动态 Emoji、GIF 素材包目录、发布控制、关键词查询和 Drafty `SK`、`AE`、`GF` 实体校验。

这些能力已经形成基础协议闭环，但联系人和素材仍使用 PCache 聚合状态，文件处理和
断点续传仍依赖节点本地运行时，不应把“基础功能已实现”等同于“生产集群闭环”。

下文给出的群组与频道 **45%–50%**、搜索与发现 **35%–40%**，是继续对照 Telegram 的超大群、全局聊天搜索、公共帖子搜索、搜索推荐、商业化和客户端体验后的完整产品评分，与本次基础服务端范围不是同一个统计口径。

这里的差距不是单纯的代码量差距，主要来自以下方面：

1. 本仓库缺少正式 Web、Android 和 iOS 客户端。
2. 基础消息交互、当前会话全文搜索和服务端素材目录已经补齐；投票、草稿、跨会话搜索、用户素材生态等高级消息能力仍未形成完整模型。
3. 群组与广播频道基础权限边界已经完成；容量、社区治理和频道运营能力仍与 Telegram 相差较大。
4. 音视频已经具备 P2P WebRTC 信令和 Agora 群组通话服务端；正式客户端、语音聊天室、直播、录制和质量运营体系仍未完成。
5. 缺少 Secret Chat、端到端加密、2FA、Passkey、自毁消息和成熟反滥用体系。
6. 缺少 Telegram Bot API、Mini Apps、支付、Stories、商业账号和内容生态。

如果目标是“可私有部署的基础 IM”，当前项目已经有不错的服务端基础；如果目标是“完整复刻 Telegram”，则属于多年、跨端、跨基础设施的大型工程。

### 1.1 状态图例

- ✅ **已实现**：仓库中已经有服务端代码和对应协议，不再作为当前待办。
- 🟡 **部分实现**：已有基础代码，但范围、规模、客户端或生产能力还没有达到 Telegram 水平。
- ❌ **未实现**：当前仓库没有形成可用功能，仍需新增设计和代码。

> 状态以当前仓库为准。标记“✅ 已实现”不代表与 Telegram 的容量、客户端体验和全球基础设施完全相同。

### 1.2 功能状态速览

#### ✅ 已实现

| 功能 | 当前已经具备的内容 |
|---|---|
| 消息连接 | WebSocket、HTTP Long Polling、gRPC 双向流 |
| 多端同步 | 离线历史、快照水位追赶、删除游标、发布幂等、多端已读/送达 |
| 基础消息 | 文本、Drafty、图片、视频、语音、音频、文件 |
| 消息操作 | 编辑、回复、转发、相册、反应、置顶、定时发送、软删除、双方删除 |
| 会话状态 | 未读数、已读、送达、输入状态、在线状态、最后在线时间 |
| 基础会话 | P2P 单聊、普通群组、广播频道 |
| 群组 ACL | owner、admin、member、readonly、banned 及底层 ACL 校验 |
| 频道 ACL | owner、admin、publisher、subscriber、banned，服务端强制读者只读 |
| 成员管理 | 邀请、查询在线/离线成员、调整角色、禁言、封禁、移除、所有权转移 |
| 基础搜索 | 用户公开 alias、群组/频道 Tag 与名称搜索、当前会话消息全文搜索 |
| 搜索过滤 | 发送者、消息类型、日期、ACL、软/硬删除过滤和不透明分页游标 |
| 群组音视频服务端 | Agora AccessToken2、群 ACL 角色、加入/离开/续期、多端 UID、断线清理和通话状态持久化 |
| 联系人基础能力 | 联系人/分组 CRUD、好友申请与接受、共同好友推荐、版本化多设备同步 |
| 文件存储 | 本地文件系统、S3、附件引用计数、文件 ACL、扫描状态、断点续传和未引用文件回收 |
| 素材消息 | 贴纸、动态 Emoji、GIF 素材目录与 Drafty 实体校验 |
| 数据库 | MySQL、PostgreSQL、MongoDB、RethinkDB 适配器 |
| 基础认证 | Basic、Token、匿名、REST、邮箱/手机号验证和密码重置 |

#### 🟡 部分实现

| 功能 | 已有部分 | 还缺什么 |
|---|---|---|
| 群组与频道 | 基础 ACL、成员管理和只读频道已完成 | Telegram 级容量、Forum、邀请链接、审批、Slow Mode、运营能力 |
| 搜索 | Peer 发现和当前会话搜索已完成 | 跨会话、公共帖子、结果上下文、分词排序、独立检索服务 |
| 音视频 | P2P WebRTC 信令、Agora 群组通话服务端 | 正式客户端、Agora 真实项目联调、TURN 生产验证、Voice Chat、直播、录制和质量运营 |
| 安全与隐私 | TLS 配置、密码/Token/验证码认证 | E2EE、Secret Chat、2FA、Passkey、自毁消息和隐私控制 |
| Bot | gRPC Plugin、FireHose、示例机器人 | Telegram 风格公共 Bot API、Webhook、Inline Bot 和键盘 |
| 集群与运维 | 分片、故障转移、监控、压测脚本、Docker | CI/CD、Kubernetes、灾备演练、可复现容量报告 |
| 社区治理 | ACL、禁言、封禁 | 限流、反垃圾、举报、申诉、审核队列和管理后台 |
| 联系人与好友 | 基础 CRUD、分组、好友关系、共同好友推荐和增量同步 | 数据库表与跨节点事务、通讯录匹配、隐私控制、反骚扰和推荐策略 |
| 文件处理 | ClamAV 协议、FFmpeg/LibreOffice 调度、预览、ACL 和节点内断点续传 | 持久任务队列、跨节点续传、分块校验、ACL 回收、物理隔离和自动重试 |
| 贴纸、动态 Emoji 与 GIF | root 管理的素材包、发布控制、本地关键词查询和消息引用校验 | 用户安装/收藏/最近使用、创作者流程、外部 GIF 搜索、热门排序、格式审核和内容治理 |
| 推送与外部服务 | FCM、TNPG、邮件、短信接口存在 | 独立通知偏好、持久队列、失败重试、死信、送达监控和运营管理 |
| 官方频道与官方大群 | 官方频道与可写大群创建、平台角色分配、冷成员 ACL、全员/单人禁言、移出、封禁、游标分页和审计 | 目标硬件容量验收、批量 Push、限流与高级运营 |
| 客户内部工作区 | 个人联系人 Alias 和 Topic 共享消息置顶可以复用一部分概念 | 机构客户备注、字段级权限、客户/会话/消息三级内部置顶和增量同步 |

#### ❌ 未实现

| 功能 | 当前状态 |
|---|---|
| 正式 Web 客户端 | 未实现 |
| 正式 Android 客户端 | 未实现 |
| 正式 iOS 客户端 | 未实现 |
| Secret Chat 与端到端加密 | 未实现 |
| 两步验证、Passkey、精细设备会话管理 | 未实现 |
| 自毁消息、阅后即焚媒体 | 未实现 |
| Voice Chat、屏幕共享、录制、频道直播 | 未实现 |
| 跨会话全局消息搜索 | 未实现 |
| 频道公共帖子搜索与搜索结果上下文跳转 | 未实现 |
| 消息草稿云同步、投票、测验 | 未实现 |
| 外部 GIF 搜索、用户素材包安装、收藏和最近使用 | 未实现 |
| Forum Topics、邀请链接、入群审批、Slow Mode | 未实现 |
| 频道浏览量、转发量、评论区、讨论组和数据分析 | 未实现 |
| 成熟反垃圾、举报、申诉、审核与管理后台 | 未实现 |
| Telegram 风格公共 Bot API | 未实现 |
| Mini Apps、支付、数字商品 | 未实现 |
| 客户归属隔离、业务员红包与转账 | 已完成服务端设计，代码、支付机构接入和资金对账尚未实现 |
| 中英双向自动翻译与内部客户资料 | 翻译服务端首期已实现；客户归属身份、机构词库/持久重试和内部工作区尚未完成 |
| Stories、商业账号、广告和收入分成 | 未实现 |
| 自动化 CI/CD、Helm/Kubernetes 和混沌测试 | 未实现 |

## 2. 能力对照评分

| 状态 | 维度 | 当前项目情况 | 对 Telegram 估算 |
|---|---|---|---:|
| ✅ | 消息连接与同步 | 三种传输共用同步语义；支持快照水位升序追赶、删除游标、持久化发布幂等、多端回执 | 100%（本仓库服务端范围） |
| ✅ | 基础消息 | 服务端校验文本/Drafty/图片/视频/语音/音频/文件；编辑、回复、转发、相册、反应、置顶、定时发送、双方删除 | 100%（本次列出的服务端范围） |
| 🟡 | 群组与频道 | 基础 ACL、成员管理、普通群双向发言和只读频道已实现；高级社区与频道运营未实现 | 100%（本次基础范围）；45%–50%（Telegram 完整范围） |
| 🟡 | 媒体与通话 | FS/S3、P2P WebRTC 和 Agora 群组通话服务端已实现；正式客户端、Agora 真实项目联调与高级实时媒体能力未完成 | 35%–40% |
| 🟡 | 安全与隐私 | 密码、Token、REST、验证码认证和 TLS 配置已实现；E2EE 与高级隐私未实现 | 10%–15% |
| 🟡 | 搜索与发现 | 基础 Peer 发现和当前会话搜索已实现；全局搜索与公共帖子搜索未实现 | 100%（本次基础范围）；35%–40%（Telegram 完整范围） |
| 🟡 | 联系人、文件与素材 | 基础协议与服务端流程已实现；分布式持久化、可靠后台任务和完整用户素材生态未完成 | 60%–70%（本次服务端范围） |
| 🟡 | Bot 与开放平台 | gRPC 插件和示例机器人已实现；公共 Bot 平台未实现 | 5% |
| ❌ | 客户端与用户体验 | 当前仓库没有正式 Web、Android、iOS 客户端 | 0% |
| 🟡 | 运维与规模化 | Docker、四种数据库、分片、故障转移、监控和压测脚本已实现；生产流水线不完整 | 30%–40% |

Telegram 官方目前支持 20 万人群组、无限订阅者频道、普通账号单文件 2 GB（Premium 为 4 GB）、最多 200 人端到端加密群组通话、Voice Chat、Secret Chat 和自毁消息等能力，参见 [Telegram FAQ](https://telegram.org/faq)。

## 3. ✅ 当前项目已经具备的能力

以下能力在代码中存在实际实现，并非仅在 README 中声明。

### 3.1 ✅ 通讯协议与消息路由

- WebSocket 双向长连接。
- HTTP Long Polling 备用通道。
- 基于 Protobuf 的 gRPC 双向流。
- 基于 Topic 的发布订阅消息模型。
- 单 Topic 内由服务端生成连续消息序号。
- 协议 0.27 支持 `forward + since` 的快照水位追赶，分页不会跳过中间消息。
- 发布消息支持持久化 `cid` 幂等键，重试不会重复落库、广播、增加未读或触发推送。
- JSON 与 gRPC 均返回 `cid`，并统一支持 data/del 离线同步游标。
- read/recv 状态可携带请求 ID，服务端在订阅状态持久化后确认；重复上报按幂等成功处理。
- MySQL、PostgreSQL 将 Topic 游标与消息写入放在同一事务；MongoDB 与 RethinkDB 启动加载时会以消息日志修复游标。
- 生产控制面已接入 etcd Lease、成员专用 Cluster View epoch 和数据库 Owner fencing；过期 Owner 的消息事务会被拒绝。
- 客户端协议版本协商。

主要代码：

- [`internal/server/hdl_websock.go`](../../internal/server/hdl_websock.go)
- [`internal/server/hdl_longpoll.go`](../../internal/server/hdl_longpoll.go)
- [`internal/server/hdl_grpc.go`](../../internal/server/hdl_grpc.go)
- [`internal/server/topic_msg.go`](../../internal/server/topic_msg.go)
- [`api/pbx/model.proto`](../../api/pbx/model.proto)

### 3.2 ✅ 基础聊天能力

- P2P 单聊。
- 普通群组。
- 广播频道。
- 多设备同时在线。
- 离线历史消息拉取。
- 未读消息计数。
- 已接收和已读回执。
- 正在输入状态。
- 在线状态和最后在线时间。
- 消息软删除和硬删除。
- 用户公开 alias、群组和频道关键词搜索。
- 当前会话消息全文搜索，支持发送者、消息类型、日期和不透明游标过滤。

### 3.3 ✅ 富文本与媒体

- Drafty 富文本消息格式。
- 加粗、斜体、删除线、代码、高亮、链接、提及和话题标签。
- 图片、视频、音频、语音和通用文件实体。
- 带外文件上传与下载。
- 本地文件系统和 S3 对象存储。
- 未引用文件垃圾回收。

主要代码：

- [`server/drafty/drafty.go`](../../server/drafty/drafty.go)
- [`internal/server/hdl_files.go`](../../internal/server/hdl_files.go)
- [`server/media/fs/filesys.go`](../../server/media/fs/filesys.go)
- [`server/media/s3/s3.go`](../../server/media/s3/s3.go)

### 3.4 ✅ 用户、认证与权限

- Basic 用户名密码认证。
- Token 认证。
- 匿名认证。
- REST 外部认证。
- 邮箱验证。
- 手机号码和 Twilio 短信验证。
- 密码重置流程。
- 用户和 Topic 标签。
- 基于位图的 ACL。
- 加入、读取、写入、Presence、审批、分享、删除和所有者权限。
- 成员邀请、移除、禁言和所有者转移。

### 3.5 ✅ 群组和广播频道基础能力

本轮已经完成基础群组与频道服务端闭环，主要实现位于：

- [`internal/server/topic_roles.go`](../../internal/server/topic_roles.go)
- [`internal/server/topic_sub.go`](../../internal/server/topic_sub.go)
- [`internal/server/topic_meta.go`](../../internal/server/topic_meta.go)
- [`internal/server/topic_msg.go`](../../internal/server/topic_msg.go)
- [`internal/server/message_features.go`](../../internal/server/message_features.go)
- [`server/store/store_subs.go`](../../server/store/store_subs.go)
- [`api/pbx/model.proto`](../../api/pbx/model.proto)

#### 角色与 ACL

高层角色是底层 ACL 的安全预设，现有 `mode` 接口仍然兼容：

| 场景 | 角色 | 服务端权限语义 |
|---|---|---|
| 普通群 | `owner` | 完整权限，只能通过所有权转移产生 |
| 普通群 | `admin` | 加入、读、写、Presence、审批、分享和删除，无所有者权限 |
| 普通群 | `member` | 加入、读、写和 Presence |
| 普通群 | `readonly` | 加入、读和 Presence |
| 普通群 | `banned` | 无有效加入权限 |
| 广播频道 | `owner` | 完整权限，只能通过所有权转移产生 |
| 广播频道 | `admin` | 管理与发布权限，无所有者权限 |
| 广播频道 | `publisher` | 加入、读、写和 Presence |
| 广播频道 | `subscriber` | 加入、读和 Presence，存放在 `chn...` 读者命名空间 |
| 广播频道 | `banned` | 无有效加入权限，并保留原成员命名空间以阻止绕过封禁 |

权限提升有额外安全约束：

- 只有所有者可以任命管理员或修改其他管理员。
- 管理员不能授予自己并不具备的权限。
- 所有者角色不能通过普通成员更新接口修改。
- 直接使用原始 ACL `mode` 时，同样禁止非所有者越权授权。
- `role` 与 `mode` 互斥，避免同一请求产生两套冲突权限。

#### 成员管理

- 管理员可以邀请成员、修改角色、禁言、封禁和移除成员。
- 普通群成员和频道发布者保存在 `grp...` 成员空间，并受群成员上限约束。
- 频道订阅读者保存在 `chn...` 空间，离线时无需常驻 Topic 内存。
- 管理员可以分页查询、封禁和移除不在线的频道订阅读者。
- 频道读者与发布者转换时会迁移订阅命名空间；迁移失败会回滚，避免同时拥有两套身份。
- 被封禁的频道读者重连时继续使用持久化 ACL，不会被默认读者权限覆盖。
- 订阅数量统一记到父 `grp...` Topic；MySQL、PostgreSQL、MongoDB 和 RethinkDB 的增减逻辑已经校准。

#### 普通群与只读广播频道

- 普通群成员可以双向发布消息，`readonly` 成员不能发布。
- 频道所有者、管理员和发布者可以发布，订阅读者只能消费内容。
- 频道只读不是仅依赖客户端隐藏按钮：发布、编辑、定时发送、输入状态、置顶和删除入口均由服务端再次校验。
- 频道读者仍可发送反应；这是允许的轻量互动，不授予内容发布权限。
- JSON 返回派生 `role`、`chan` 和 `subcnt`；gRPC 对应返回 `role`、`is_chan` 和 `sub_count`。
- CLI 已支持通过 `--role` 管理成员、查询离线频道读者和移除成员。
- 复用了现有订阅与 Topic 计数结构，不需要新增表或数据库迁移。

这意味着本次要求的 ACL、成员管理、普通群和只读广播频道已经实现；仍未达到 Telegram 的部分主要是大规模容量、社区治理和频道运营能力，详见 4.3 与 4.4。

### 3.6 ✅ 基础搜索与发现

协议 `0.28` 已形成统一搜索入口：

- 在 `fnd` Topic 使用 `get.what=search` 和 `scope=peers` 搜索可发现对象。
- 用户只通过配置的公开 alias Tag 被发现，不能仅凭显示昵称枚举账号。
- 群组和频道可通过 alias Tag 或 `public.fn` 命中。
- 服务端过滤私有群、暂停对象、系统 Topic、调用者本人，并按 alias 相关性、订阅数和 Topic 名称稳定排序。
- 在 P2P、普通群及频道管理 Topic 使用 `scope=topic` 搜索当前会话消息。
- 支持发送者、消息类型、起止日期、每页数量和不透明游标。
- 文本、Drafty 可见文本、附件名称和 URL 会被提取为服务端搜索文本。
- 搜索结果服从 Read ACL，并排除硬删除及当前用户已软删除的消息。
- JSON、gRPC 和 CLI 使用同一套查询与结果语义。

存储实现：

- MySQL 与 PostgreSQL 新增带字段注释的 `messages.searchtext`。
- MongoDB 与 RethinkDB 使用对应的文档字段，并在代码中记录字段用途。
- 四种数据库适配器均实现 Peer 发现和当前 Topic 消息搜索。
- 数据库版本从 `118` 升级到 `119`，迁移时为历史文本/Drafty 消息回填搜索文本。
- 当前数据库版本为 `121`；`119→120` 为 Topic 增加带字段注释的
  `clusterowner`、`clusterepoch`，`120→121` 为官方大群成员游标查询补齐复合索引。

主要代码：

- [`internal/server/search.go`](../../internal/server/search.go)
- [`server/store/store_users.go`](../../server/store/store_users.go)
- [`server/store/store_messages.go`](../../server/store/store_messages.go)
- [`server/drafty/drafty.go`](../../server/drafty/drafty.go)
- [`server/db/adapter.go`](../../server/db/adapter.go)
- [`api/pbx/model.proto`](../../api/pbx/model.proto)
- [`cli/commands.go`](../../cmd/im-cli/commands.go)

回归测试覆盖查询游标绑定、Read ACL、分页、相关性与隐私排序、Drafty 搜索文本、gRPC 往返和 CLI 参数构造。全仓测试、核心模块竞态测试、`go vet` 以及四种数据库 build tag 编译均已通过；需要真实数据库实例的集成查询仍应纳入 CI。

### 3.7 ✅ 已有的存储、集群与运维能力

- MySQL。
- PostgreSQL。
- MongoDB。
- RethinkDB。
- 一致性哈希 Topic 分片。
- etcd Lease、唯一节点注册、成员 Watch、多数派视图和成员变更专用 epoch。
- PostgreSQL、MySQL 已通过真实组件 fencing 测试；MongoDB 仅在 Replica Set 事务模式启用；RethinkDB 不参与生产控制面集群。
- 旧三节点内存故障转移和 Leader 选举只保留开发兼容。
- Docker 和 Docker Compose。
- Prometheus 和 InfluxDB 指标导出。
- Go 单机与多机分布式压测工具。

四种数据库后端均已使用对应 build tag 完成服务端编译验证。

### 3.8 ✅ 联系人、文件处理与素材消息基础能力

协议 `0.30` 已增加联系人和素材元数据，并把文件处理安全边界接入消息发布与下载：

- 联系人支持私有别名、分组、好友申请、接受、删除、拉黑状态、共同好友推荐以及
  `since + version + reset` 多设备增量同步。
- 联系人或好友关系变更会向当前用户其它设备发送 `contacts` Presence；涉及好友双方
  的操作也会通知对方同步。
- 文件上传由服务端计算 SHA-256；消息发送前验证文件所有者和扫描状态，消息保存后
  将文件 ACL 绑定到 Topic，下载与对象存储预签名重定向之前重新校验 Read 权限。
- `/v0/file/resumable/` 支持创建上传会话、按偏移追加分块、查询偏移、取消和完成上传；
  `/v0/file/meta/` 返回摘要、扫描、处理和预览状态。
- 可选后台 Worker 使用 ClamAV 扫描文件，使用 FFmpeg 生成图片预览、视频海报和压缩
  MP4、音频 Opus，使用 LibreOffice 将 Office 文档转换为 PDF。
- root 用户可管理贴纸、动态 Emoji 和 GIF 素材包；普通用户查询已发布目录。消息发布
  时服务端校验素材存在、已发布并且类型与 Drafty 实体一致。

主要实现位于：

- [`server/store/store_contacts.go`](../../server/store/store_contacts.go)
- [`server/store/file_security.go`](../../server/store/file_security.go)
- [`server/store/store_assets.go`](../../server/store/store_assets.go)
- [`internal/server/file_processing.go`](../../internal/server/file_processing.go)
- [`internal/server/hdl_file_resumable.go`](../../internal/server/hdl_file_resumable.go)
- [`internal/server/topic_meta_contacts.go`](../../internal/server/topic_meta_contacts.go)
- [`internal/server/topic_meta_assets.go`](../../internal/server/topic_meta_assets.go)
- [`server/drafty/drafty.go`](../../server/drafty/drafty.go)
- [`docs/contacts-files-assets.md`](../contacts-files-assets.md)

这三类能力的生产化边界统一记录在 4.10。

## 4. 🟡/❌ 主要功能差距

### 4.1 🟡 高级消息生态部分实现

协议 0.27 已经将此前由客户端自行编码的消息语义提升为服务端原生能力：

- 服务端解析并校验 Drafty span、实体、媒体 MIME、尺寸、时长和附件引用。
- 原生回复、跨 Topic 权限校验转发、原位编辑、图片/视频相册。
- 持久化反应聚合和置顶列表，变更通过在线事件及 `ims` 离线增量同步。
- 独立持久化定时队列；投递时才分配 Topic `seq`，支持取消、故障重试和 `cid` 幂等。
- 普通用户可以为所有参与者删除自己发送的消息，管理员可以删除任意消息；软删除仍保留“仅自己隐藏”语义。
- 媒体附件在发送、编辑、定时等待、取消和删除过程中的引用计数/垃圾回收保持一致。

这里的置顶是保存在 Topic 上、对会话双方或全群共享的消息置顶，不包含员工私有的
客户置顶、会话置顶和重点消息置顶。

主要实现位于：

- [`internal/server/message_features.go`](../../internal/server/message_features.go)
- [`internal/server/scheduled_messages.go`](../../internal/server/scheduled_messages.go)
- [`server/drafty/drafty.go`](../../server/drafty/drafty.go)
- [`api/pbx/model.proto`](../../api/pbx/model.proto)

以下功能均为 ❌ **未实现**：

- 投票和测验。
- 清单。
- 重复定时发送。
- 消息草稿云同步。
- 用户素材包安装、收藏和最近使用。
- 外部 GIF 搜索、热门推荐和结果排序。
- 按接收者投影的中英双向自动消息翻译。
- 机构级客户备注、内部资料和客户/会话/消息三级私有置顶。
- 语音转文字。
- 编辑历史与历史版本审计。

### 4.2 🟡 基础搜索已实现，全局检索未实现

当前代码已经补齐第一阶段搜索闭环：

- `fnd` Topic 支持按公开 alias Tag 搜索用户，用户不能仅凭显示昵称被枚举。
- 群组与频道支持按 alias Tag 或 `public.fn` 关键词搜索。
- 私有群、暂停对象、系统 Topic 和调用者本人会被过滤。
- 当前 P2P、群组和频道管理 Topic 支持消息正文全文搜索。
- 支持按发送者、消息类型、起止日期过滤，以及与查询条件绑定的不透明游标。
- 搜索遵守 Read ACL、硬删除和当前用户软删除可见性。
- 文本、Drafty 可见文本及附件名称/URL 会在写入时提取为 NFKC 规范化搜索文本。
- MySQL、PostgreSQL、MongoDB 和 RethinkDB 均已实现查询与 118→119 历史数据迁移。
- JSON、gRPC 和 CLI 均已接入统一搜索协议。

主要实现位于：

- [`internal/server/search.go`](../../internal/server/search.go)
- [`server/drafty/drafty.go`](../../server/drafty/drafty.go)
- [`server/store/types/search.go`](../../server/store/types/search.go)
- [`server/db/mysql/adapter.go`](../../server/db/mysql/adapter.go)
- [`server/db/postgres/adapter.go`](../../server/db/postgres/adapter.go)
- [`server/db/mongodb/adapter.go`](../../server/db/mongodb/adapter.go)
- [`server/db/rethinkdb/adapter.go`](../../server/db/rethinkdb/adapter.go)
- [`api/pbx/model.proto`](../../api/pbx/model.proto)

对照 Telegram，以下功能均为 ❌ **未实现**：

- 跨会话搜索。
- 频道公共帖子搜索。
- 搜索结果高亮和跳转上下文。
- 拼写纠正、分词、语言相关排序和相关推荐。
- Elasticsearch/OpenSearch 等独立检索后端。

当前数据库实现以跨后端一致的子串匹配为主，功能正确但不等同于 Telegram 的海量全局检索；进入大规模阶段后仍需要独立倒排索引、异步索引流水线和权限过滤层。

### 4.3 🟡 基础群组已实现，高级社区治理未实现

基础成员管理已经不再是差距：项目现已具备所有者、管理员、普通成员、只读成员和封禁角色，支持在线/离线成员查询、邀请、调整角色、移除、封禁及所有权转移。剩余差距集中在规模验证和高级治理。

当前默认配置：

- 普通群组最大成员数：128。
- 单个上传文件最大值：8 MB。
- 消息体最大值：128 KB。

参见 [`configs/im.yaml`](../../configs/im.yaml)。

Telegram 群组可达到 20 万成员，频道面向无限订阅者。当前项目已经在协议、权限和存储命名空间上区分普通群组与广播频道，但尚未提供足够证据证明可以支撑 Telegram 级大群或无限订阅读者。

仓库已经提供可复现的 Go 单机与多机分布式压测入口，能够输出连接、登录、订阅、发布、投递、错误分类以及 P50/P95/P99 延迟报告。参见 [`tests/load/README.md`](../../tests/load/README.md)。

但仓库尚未提交目标硬件上的原始测试报告、服务端硬件监控、数据库指标和持续集成容量流水线，因此仍不能把历史数据作为当前版本的已验证容量。

社区治理方面，以下功能均为 ❌ **未实现**：

- Forum Topics 和独立讨论历史。
- 多种群组邀请链接。
- 邀请链接过期时间和使用次数。
- 入群审批队列。
- Slow Mode。
- 举报和申诉。
- 自动内容审核。
- 反垃圾注册与反垃圾消息系统。
- 管理操作审计日志。
- 更细粒度的管理员角色。
- 新成员历史可见范围控制。

### 4.4 🟡 只读频道已实现，频道运营未实现

当前项目已经具备广播频道的核心权限语义：

- 平台管理 API 创建并认证官方频道，分配管理员、发布者和订阅者。
- 官方策略使用客户端不可修改的隐藏投影，角色变更进入版本化操作审计。
- 管理角色与订阅读者使用不同权限和订阅命名空间。
- 所有者、管理员和发布者可以发帖。
- 订阅读者由服务端强制只读，不能通过旧 ACL 写位或绕过客户端直接发布、编辑、定时发送、置顶或删除。
- 平台可以管理不在线的订阅读者，角色调整在重连后仍有效。
- 四种数据库后端维护一致的频道订阅总数。

因此，“只读广播频道”本身已经完成。它与 Telegram 的剩余差距主要在频道运营、内容分发和商业化：

以下频道功能均为 ❌ **未实现**：

- 消息浏览量和独立访客统计。
- 转发量。
- 频道评论区。
- 关联讨论组。
- 频道帖子建议和审批。
- 多管理员署名规则。
- 内容搜索和频道推荐。
- 频道数据分析。
- 付费订阅和付费媒体。
- 广告和收入分成。
- 频道 Direct Messages。

另外，多个数据库适配器中明确注明频道未读消息没有被计入用户总未读数，说明频道未读体系仍有已知缺口。

全部产品功能需求已经合并记录在
[`docs/im-product-requirements.md`](../im-product-requirements.md)；官方只读频道、官方
大群、客户归属私聊隔离、中英双向翻译、内部客户资料、三级置顶及业务员红包/转账
均以该文档为唯一需求基线。
官方只读频道与无固定产品人数上限的可写大群已形成服务端闭环；大群生产容量、批量
Push、客户策略、内部工作区、翻译投影和全部支付能力仍未完成。

### 4.5 🟡 P2P WebRTC 与 Agora 群组通话服务端已实现

当前 P2P 音视频实现负责：

- 呼叫邀请。
- 响铃和接听。
- SDP Offer/Answer 转发。
- ICE Candidate 转发。
- 挂断和通话状态持久化。

群组语音/视频通话已经新增 Agora 服务端实现：

- 仅群组写成员可创建通话，具有 `J+R` ACL 的成员可加入。
- 写成员签发 publisher Token，只读成员签发 subscriber Token。
- 使用 Agora AccessToken2，Token 绑定不可逆频道名和 Session 唯一数字 UID。
- App Certificate 只在服务端使用，支持 `AGORA_APP_ID`、`AGORA_APP_CERTIFICATE` 环境变量。
- Token 有效期限制在 60 秒至 Agora 官方上限 24 小时。
- 支持 `join`、`leave`、`refresh`、管理员/发起人 `hang-up`。
- 支持多端独立 UID、最大人数限制、Session 断线清理和最后成员离开自动结束。
- JSON、WebSocket、Long Polling 与 gRPC 共用同一协议语义。
- 通话接通、完成、未接和异常断开继续通过替换消息持久化。

主要代码：

- [`internal/server/topic_msg.go`](../../internal/server/topic_msg.go)
- [`internal/server/calls.go`](../../internal/server/calls.go)
- [`internal/server/calls_config.go`](../../internal/server/calls_config.go)
- [`internal/server/calls_webrtc.go`](../../internal/server/calls_webrtc.go)
- [`internal/server/calls_agora.go`](../../internal/server/calls_agora.go)
- [`server/agora/token.go`](../../server/agora/token.go)
- [`api/pbx/model.proto`](../../api/pbx/model.proto)

默认配置仍关闭通话。P2P 需要外部 STUN/TURN；群组通话需要 Agora 项目 App ID、App Certificate，并需要正式客户端接入 Agora RTC SDK。按照 Agora 官方要求，要严格强制只读成员不能发布媒体，还应在 Agora Console 开启 Co-host Token Authentication。当前自动化测试覆盖 Token 格式、HMAC、ACL 角色、协议转换和服务端群组呼叫流程，但尚未使用真实 Agora 项目完成跨端网络联调。

本仓库没有自建 SFU/MCU，以下完整产品能力仍为 ❌ **未实现**：

- 正式 Web、Android、iOS 客户端的 Agora SDK 和通话 UI。
- Voice Chat。
- 频道直播。
- 无限观众转发。
- 屏幕共享。
- 通话录制。
- 发言人管理。
- 主动说话人检测。
- 分区域媒体节点。
- 自适应码率和 Simulcast。
- 音视频质量监控。

因此，“群组语音/视频通话服务端”标记为 ✅ **已实现**；“可直接交付给最终用户的跨端群组通话产品”仍标记为 🟡 **部分实现**。Telegram 当前还具备最多 200 人端到端加密群组通话、群组 Voice Chat 和频道直播，参见 [Telegram FAQ](https://www.telegram.org/faq) 和 [Telegram Group Calls](https://core.telegram.org/api/group-calls)。

### 4.6 🟡 基础认证与 TLS 已实现，高级隐私未实现

当前项目支持传输层 TLS，但默认配置为关闭状态。消息内容会直接存储在服务端数据库，没有端到端加密消息信封、密钥交换或设备密钥模型。

以下安全与隐私功能均为 ❌ **未实现**：

- Secret Chat。
- 端到端加密消息。
- 端到端加密媒体。
- 自毁消息。
- 阅后即焚媒体。
- 截屏提示。
- 两步验证。
- Passkey。
- 登录设备和会话管理。
- 会话远程注销。
- 手机号可见性规则。
- Last Seen 精细隐私规则。
- 群组邀请隐私规则。
- 转发消息身份隐私。
- 本地应用锁相关配套接口。

Telegram Secret Chat 支持端到端加密、禁止转发和自毁消息，参见 [Telegram Secret Chats](https://www.telegram.org/faq#secret-chats)。

### 4.7 ❌ 成熟反滥用体系未实现

代码中没有发现通用 API、登录和发消息速率限制器。

目前主要依赖：

- 验证码最大尝试次数。
- ACL。
- 邮箱域名白名单。
- 用户或 Topic 权限撤销。

以下生产级反滥用功能均为 ❌ **未实现**：

- 按 IP、账号、设备和 API Key 限流。
- 登录爆破防护。
- 注册频率限制。
- 消息洪泛检测。
- 群发和拉群检测。
- URL 信誉检测、外部链接扫描和扫描结果联动风控；本地上传已具备可选 ClamAV 扫描。
- 风险账号评分。
- 设备指纹。
- 举报、封禁和申诉工作流。
- 管理后台。
- 审核队列。
- 证据保留与审计。

### 4.8 🟡 内部插件已实现，公共 Bot 平台未实现

现有能力：

- gRPC Plugin 回调。
- FireHose 消息过滤。
- 外部搜索插件。
- 示例自动回复机器人。

这更接近内部插件框架，不是 Telegram Bot Platform。

以下 Bot 与平台功能均为 ❌ **未实现**：

- 公共 Bot API。
- Bot Token 管理。
- Webhook。
- Long Polling Updates。
- Bot 命令管理。
- Inline Bot。
- Reply Keyboard。
- Inline Keyboard。
- Deep Link。
- Bot 权限和隐私模式。
- HTML5 Games。
- 支付。
- Telegram Stars 类数字资产。
- Mini Apps。
- Mini App Store。
- 商业机器人和代回复。

Telegram Bot Platform 和 Mini Apps 的当前能力参见 [Telegram Bot Features](https://core.telegram.org/bots/features)。

### 4.9 ❌ Stories、商业化和内容生态未实现

以下领域模型和接口均为 ❌ **未实现**：

- 用户 Stories。
- 群组和频道 Stories。
- Story 隐私规则。
- Story 浏览者和反应。
- Story 相册。
- Live Stories。
- Profile Music。
- Gifts。
- Stars。
- 付费消息和付费媒体。
- 频道订阅。
- Business Account。
- 广告和收入分成。
- 内容推荐。

相关 Telegram 能力可参考：

- [Telegram Stories](https://www.telegram.org/blog/stories)
- [The Evolution of Telegram](https://telegram.org/evolution/)

### 4.10 🟡 联系人、文件与素材已形成基础闭环，生产化仍未完成

#### 联系人与好友关系

当前联系人和分组状态以每用户一个压缩 PCache 值保存，素材目录也以单个全局 PCache
值保存。写入锁只在当前进程内生效，好友申请、接受和删除需要依次保存双方状态。
因此以下能力仍未实现：

- 联系人、分组、好友边和素材包的正式数据库表、索引和数据库迁移。
- 多节点并发写入所需的 CAS、分布式锁或事务；好友双方状态也没有跨用户原子事务。
- 大通讯录和大素材目录的稳定分页，当前聚合值受 PCache 单值容量约束。
- 手机号/邮箱哈希通讯录上传、隐私授权、联系人匹配与找回。
- 独立的拒绝好友、解除关系、拉黑和解除拉黑工作流及双方状态约束。
- 好友申请频率限制、骚扰防护、推荐隐私和可解释推荐策略。
- 共同好友之外的推荐信号、离线计算、去重曝光和用户反馈闭环。

#### 文件处理、ACL 与断点续传

当前文件处理队列和去重集合位于进程内存，断点续传分块写入创建会话节点的本地临时
文件。因此以下生产能力仍未实现：

- 可在重启后恢复的持久任务队列、Worker 租约、自动重试、退避、死信和处理指标。
- 跨节点断点续传；当前集群必须依据上传 `Location` 保持节点粘性。
- 节点重启后的上传会话恢复、本地孤儿临时文件清理和对象存储 Multipart Upload。
- 分块 SHA-256、最终摘要声明校验和基于摘要的安全去重。
- 消息硬删除、附件替换或 Topic 删除后的文件 ACL 引用回收。
- 对外部 HTTPS 文件的服务端 ACL、安全扫描和生命周期管理；当前只管理本地媒体 URL。
- 恶意文件的独立物理隔离区、人工释放/删除工作流、扫描引擎签名版本监控。
- 多规格缩略图、自适应视频档位、转码资源隔离和真实 ClamAV/FFmpeg/LibreOffice
  集成环境的故障测试。

#### 贴纸、动态 Emoji 与 GIF

当前实现是由 root 管理的全局素材目录，并通过素材 ID 在消息中引用。以下完整素材
生态仍未实现：

- 用户安装/卸载素材包、收藏、最近使用、个人排序和跨设备同步。
- 创作者提交、版权信息、审核、下架申诉和版本发布流程。
- 动态贴纸和动态 Emoji 的文件格式、尺寸、帧率、时长、透明通道和文件大小强校验。
- GIPHY、Tenor 等外部 GIF 提供方接入、热门榜、搜索排序、缓存和内容分级。
- 素材使用统计、推荐、地域/年龄过滤和内容治理。

### 4.11 🟡 推送已可用，通知偏好与可靠投递未实现

当前 FCM/TNPG 推送支持新消息、订阅变化、已读同步、静默推送和失效 Token 清理，
Topic Presence 权限可粗粒度决定是否接收通知，但它不是独立通知设置。

以下功能仍未实现：

- 全局和单会话静音、仅提及、关键词提醒、免打扰时段、自定义声音与通知预览设置。
- 独立于 Presence ACL 的用户、Topic、设备通知偏好模型和多设备同步。
- 持久化推送队列、失败重试、指数退避、死信、幂等键和优先级控制。
- 推送缓冲区满时的可靠背压；当前异步入口允许直接丢弃。
- FCM/TNPG 送达率、延迟、错误码、Token 健康度指标及运营告警。

## 5. ❌ 正式客户端未实现

项目 README 声明支持 Web、Android 和 iOS，但这些客户端不在当前仓库。

当前仓库内可用的客户端侧内容主要是：

- Go CLI。
- Go 示例机器人。
- gRPC Protobuf 定义。
- 客户端协议文档。

因此，当前仓库不能独立构成一个面向最终用户的全栈 IM 产品。

若不复用外部兼容客户端，以下内容均为 ❌ **未实现**：

- Web 客户端。
- Android 客户端。
- iOS 客户端。
- 桌面客户端。
- 消息本地数据库。
- 离线队列。
- 媒体缓存。
- 后台连接恢复。
- 推送点击路由。
- 通话 UI。
- 群组和频道管理 UI。
- 隐私设置。
- 国际化。
- 无障碍支持。
- 自动化 UI 测试。

客户端工作量很可能超过新增服务端功能的工作量。

## 6. 🟡 工程验证已具备，生产工程仍不完整

### 6.1 ✅ 当前代码验证结果

本次分析执行了以下验证：

```text
go test ./...
go vet ./...
go test -race ./internal/server ./server/drafty ./server/db/common ./cmd/im-cli
go test -tags mysql ./internal/server ./server/db/mysql
go test -tags postgres ./internal/server ./server/db/postgres
go test -tags mongodb ./internal/server ./server/db/mongodb
go test -tags rethinkdb ./internal/server ./server/db/rethinkdb
```

结果：

- 默认 Go 测试通过。
- Go Vet 通过。
- 服务端、存储封装和 CLI 关键包的 Race Detector 测试通过。
- 四种数据库后端均通过实际编译。
- 新增回归测试覆盖角色到 ACL 映射、管理员越权阻断、离线频道读者管理、封禁后重连、频道只读边界、订阅计数归一化、搜索 ACL/游标/排序、Drafty 搜索文本、CLI 参数以及 JSON/gRPC 字段转换。
- 另有约 227 个数据库集成测试，需要实际数据库和对应 build tag，默认没有执行。

### 6.2 🟡 测试已运行，覆盖率仍不足

本次执行 `go test -cover ./internal/server ./server/...` 得到的主要结果：

| 包 | 覆盖率 |
|---|---:|
| `chat/internal/server` | 21.7% |
| `server/db/common` | 67.3% |
| `server/drafty` | 86.2% |
| `server/logs` | 61.8% |
| `server/media` | 63.6% |
| `server/ringhash` | 93.3% |
| `server/store/types` | 8.0% |

认证、推送和真实外部文件处理器集成等关键路径仍缺少充分覆盖；联系人、文件安全、
断点续传和素材目录已有单元测试，但距离生产级回归保护与故障测试仍有差距。

### 6.3 ❌ 生产安全默认配置未完成

[`configs/im.yaml`](../../configs/im.yaml) 中存在以下开发环境默认值：

- TLS 默认关闭。
- MySQL 默认密码为 `123456`。
- PostgreSQL 默认密码为 `postgres`。
- Token HMAC Key 固定写在配置文件中。
- UID 加密 Key 固定写在配置文件中。
- API Key Salt 固定写在配置文件中。
- 邮箱固定调试验证码为 `123456`。
- 手机固定调试验证码为 `123456`。
- FCM 默认关闭。
- WebRTC 默认关闭。
- 插件默认关闭。
- 集群默认未启用。
- `/debug/vars` 默认暴露。

这些配置可以作为本地示例，但不应直接进入生产环境。

建议将配置拆成：

1. `im.example.yaml`：安全的 YAML 示例配置。
2. 生产配置：通过环境变量或 Secret Manager 注入。
3. 启动时安全检查：发现默认密钥、调试验证码或关闭 TLS 时拒绝生产模式启动。

### 6.4 ❌ 生产工程流水线未完成

当前仓库只有一个初始化提交，约 191 个跟踪文件和约 7 万行新增代码。

还存在以下问题：

- 没有 CI/CD 工作流。
- 没有自动执行数据库集成测试。
- 没有 Kubernetes 或 Helm 部署。
- 没有正式数据库迁移流水线。
- 没有 API 兼容性测试。
- 没有端到端客户端测试。
- 没有故障注入和混沌测试。
- 没有提交压测原始结果。
- 没有 `SECURITY.md`。
- 没有 `CONTRIBUTING.md`。

### 6.5 ❌ 许可证与来源材料未补齐

README 声明项目采用 GPL-3.0，但仓库没有发现 `LICENSE`、`COPYING` 或 `NOTICE` 文件。

同时，压测脚本、测试数据、User-Agent 兼容逻辑和部分文档仍保留 Tinode 名称或来源痕迹。

正式发布前需要完成：

- 确认原始代码来源和版本。
- 确认上游许可证。
- 恢复或补充版权声明。
- 添加完整许可证文本。
- 添加第三方依赖和来源 NOTICE。
- 确认对外分发和 SaaS 部署的许可证义务。

此处仅为工程风险提示，不构成法律意见。

## 7. ❌ 待建设路线

### P0：生产基线，预计 4–8 周（❌ 待完成）

目标：让当前核心 IM 服务具备安全上线条件。

建议内容：

1. ❌ 拆分示例配置和生产配置。
2. ❌ 所有密钥、密码和验证码配置外置。
3. ❌ 强制 TLS 和安全响应头。
4. ❌ 增加 API、登录、注册、上传和发消息限流。
5. ❌ 增加账号锁定和登录爆破防护。
6. ❌ 接入真实短信、邮件和 FCM。
7. ❌ 部署 TURN 服务并验证 P2P 通话。
8. ❌ 建立 CI、代码检查和四数据库集成测试。
9. ❌ 建立数据库迁移、备份、恢复和灾备演练。
10. ❌ 增加关键路径指标、Tracing 和告警。
11. ❌ 处理许可证和上游归属问题。
12. ❌ 明确首个正式客户端方案。
13. ❌ 将文件处理迁移到持久任务队列，并实现跨节点或对象存储 Multipart 断点续传。
14. ❌ 将联系人和素材目录迁移到正式数据表，补齐多节点并发控制与好友双方事务。
15. ❌ 增加独立通知偏好模型、可靠推送队列、重试、死信和送达监控。

### P1：Telegram 风格基础聊天，预计 3–6 个月（🟡 部分完成）

目标：达到可用的 Telegram 风格私有化聊天 MVP。

已经完成、不再列入待办：

- ✅ 编辑、回复、转发、相册、反应、置顶和定时消息。
- ✅ 文本、Drafty 富文本、图片、视频、语音、音频、文件及双方删除的服务端校验与生命周期管理。
- ✅ 群组/频道角色、在线/离线成员管理、普通群双向发言和服务端强制只读广播频道。
- ✅ 用户公开 alias、群组/频道 Tag 与名称发现，以及当前会话消息全文搜索和过滤。
- ✅ Agora 群组语音/视频通话服务端、短期 AccessToken2、ACL 角色和 Token 续期协议。
- ✅ 联系人/分组 CRUD、好友申请与接受、共同好友推荐和版本化多设备同步。
- ✅ 文件 ACL、安全扫描状态、预览/转码调度、元数据查询和节点内断点续传。
- ✅ root 管理的贴纸、动态 Emoji、GIF 素材目录及消息引用校验。

建议内容：

1. ❌ 消息草稿云同步、投票和测验。
2. ❌ 跨会话搜索、频道公共帖子搜索与结果上下文跳转。
3. ❌ 群组邀请链接和入群审批。
4. ❌ 举报、内容审核、申诉和管理审计日志。
5. ❌ 频道浏览量、转发量和评论区。
6. ❌ 用户素材包安装/收藏/最近使用，以及外部 GIF 搜索、热门排序和内容治理。
7. ❌ 正式 Web 或移动客户端。
8. ❌ 端到端自动化测试。
9. ✅ 官方只读频道创建、认证和平台角色分配；❌ 机构身份联调及客户归属私聊隔离。
10. 🟡 中英 P2P 纯文本自动翻译服务端首期已实现；❌ 客户身份联调、内部资料以及客户/会话/消息三级内部置顶。

### P2：大型社区与实时音视频，预计 6–12 个月（🟡 部分实现）

目标：具备大型群组、频道和群组实时通信能力。

建议内容：

1. ❌ Forum Topics。
2. 🟡 大群冷成员、ACL 刷新和成员游标分页已完成；容量压测、热点保护和批量 Push 待完成。
3. ❌ Slow Mode 和自动反垃圾。
4. ❌ 频道统计和运营后台。
5. ❌ 独立搜索服务。
6. ✅ Agora 群组语音和视频通话服务端、ACL 与 Token 生命周期。
7. ❌ 正式客户端 Agora SDK、通话 UI 和端到端自动化测试。
8. ❌ Voice Chat。
9. ❌ 屏幕共享和录制。
10. ❌ 频道直播。
11. 🟡 无固定产品人数上限的可写官方大群和冷成员模型已实现；专项容量验收待完成。

### P3：平台与生态，预计 12–24 个月以上（❌ 未实现）

目标：从聊天产品发展为平台。

建议内容：

1. ❌ 公共 Bot API。
2. ❌ Webhook、Inline Bot 和交互式键盘。
3. ❌ Mini Apps。
4. ❌ 支付和数字商品。
5. ❌ Stories 和 Live Stories。
6. ❌ 商业账号。
7. ❌ 付费频道和付费媒体。
8. ❌ 内容推荐和公共搜索。
9. ❌ 多区域部署和全球媒体网络。
10. ❌ 公司预算控制的业务员红包/转账、支付机构接入、账本、回调和对账。

## 8. 人力和工期估算

以下估算假设团队具备 Go、Web、Android、iOS、实时音视频、SRE 和安全经验。

| 目标 | 预计工作量 |
|---|---:|
| 当前服务端达到安全生产基线 | 8–15 人月 |
| Telegram 风格私有化聊天 MVP | 30–50 人月 |
| 大型群组、频道、搜索和群组通话 | 60–120 人月 |
| 当前 Telegram 主要产品广度 | 150–300+ 人月 |
| Telegram 级全球规模和生态 | 多年持续投入 |

如果复用已有且协议兼容的 Web、Android 和 iOS 客户端，MVP 工期可以显著下降；如果客户端全部从零开发，跨端工作量可能占到整体项目的一半以上。

## 9. 最终判断

当前项目最适合的定位是：

> 一个具备多数据库、集群、富文本与媒体消息、角色化群组、服务端强制只读广播频道、权限感知搜索、P2P WebRTC 和 Agora 群组通话服务端能力的 Tinode 风格 IM 服务端底座。

不建议现阶段将其定位为“Telegram 替代品”或“Telegram 完整复刻”，因为这种定位会掩盖客户端、安全、社区治理、音视频媒体层和平台生态方面的大量缺口。

更现实的产品目标是：

1. 先完成安全、稳定的私有化 IM。
2. 在已完成基础消息、群组、只读频道和当前会话搜索的前提下，补齐客户端、全局搜索、邀请审批、举报审核和频道运营能力。
3. 根据真实业务需求选择性建设 Telegram 级大群、群组通话或 Bot 平台。
4. 避免在没有用户需求验证前同时建设 Stories、支付、Mini Apps 和内容推荐。
