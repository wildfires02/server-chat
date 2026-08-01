# 权限模型与规模化演进方案

> 文档信息
>
> - 更新日期：2026-08-01
> - 类型：架构决策与实施路线图
> - 当前状态：保留 Topic ACL；新增细粒度能力位；按阶段演进
> - 文档入口：[文档导航](README.md)

## 1. 架构决策

服务端不在 ACL、RBAC 和 ABAC 中三选一，而是按职责组合使用：

| 层次 | 模型 | 负责内容 |
| --- | --- | --- |
| Topic 数据面 | ACL 与能力位 | 加入、读取、发送、Presence 等高频判断 |
| Topic 管理面 | RBAC | owner、admin、moderator、publisher、member 等角色模板 |
| 资源关系 | ReBAC | 用户属于 Topic、Topic 属于机构、员工负责客户等关系 |
| 动态约束 | ABAC | 账号状态、禁言期限、机构范围、官方状态和请求上下文 |
| 最终执行 | 编译后的有效能力 | Topic Actor 内只进行状态判断和位运算 |

目标模型可以概括为：

```text
关系决定作用域，角色提供能力集合，属性进一步收紧，ACL 投影负责快速执行。
```

消息发布、广播、历史读取和在线投递不得逐次调用数据库、Casbin 或远程权限服务。
权限变更先生成版本化投影，再由 Topic Actor 在本地执行。

## 2. 范围与非目标

本文覆盖：

- Topic 成员、频道订阅者、群管理员和平台管理员权限。
- 普通群、频道、官方大群、P2P 和群组通话的授权边界。
- 现有 `AccessMode` 的兼容方式和细粒度能力位设计。
- 数据量增长后的存储、缓存、一致性、审计和迁移方案。
- 从当前单服务权限模型演进到跨服务关系授权的触发条件。

本文不负责：

- 用户密码、验证码和第三方登录等身份认证流程。
- Agora、对象存储等供应商自身的 IAM 配置。
- 钱包、余额和订单权限的业务事实存储；这些事实仍由对应业务服务管理。
- 仅依靠客户端隐藏入口实现安全。客户端隐藏入口属于交互要求，服务端校验才是授权边界。

## 3. 当前实现基线

### 3.1 Topic ACL

当前 `AccessMode` 使用八个权限位：

| 位 | 含义 |
| --- | --- |
| `J` | 加入 Topic |
| `R` | 读取消息 |
| `W` | 发布消息 |
| `P` | 接收 Presence |
| `A` | 审批或管理成员 |
| `S` | 分享或邀请成员 |
| `D` | 硬删除消息 |
| `O` | Topic 所有者 |

用户有效权限为：

```text
effectiveMode = ModeWant & ModeGiven
```

该模型已经进入订阅协议、消息热路径、数据库和 Topic 内存状态，应继续保留。它适合
执行加入、读取和写入等基础判断，不适合继续承载不断增长的全部管理动作。

### 3.2 Topic 角色

当前服务端已经把以下角色映射为 ACL 预设：

- 群组：`owner`、`admin`、`member`、`readonly`、`banned`。
- 频道：`owner`、`admin`、`publisher`、`subscriber`、`banned`。

角色目前可从最终 ACL 反向推导。反向推导适合协议兼容，但多个不同能力集合可能得到
相同角色名称，不能作为未来审计和授权事实的唯一来源。

### 3.3 Subscription 关系

`subscriptions` 同时保存：

- 用户与 Topic 的成员关系。
- `ModeWant` 和 `ModeGiven`。
- 已接收、已读、删除和逐消息已读历史游标。
- 用户在该 Topic 下的私有资料。

即使后续采用 RBAC 或关系授权，该记录仍不可取消，因为消息同步游标天然属于
“用户—Topic”关系。权限扩展不应破坏当前 `(topic, userid)` 唯一关系和两个方向的查询。

### 3.4 平台控制面

后台已经提供基于 Casbin 的角色、权限目录、Domain、绑定过期时间和版本化更新。
目前仍存在以下规模化边界：

- 管理 HTTP API 主要使用单个 bootstrap token，审计主体固定为 `bootstrap-admin`。
- 角色、绑定、官方 Topic、设置和最近审计保存在一个整体控制面文档中。
- 单次更新会复制完整文档并重建 Enforcer。
- 审计只保留控制面文档中的有限条目。
- 集群节点之间缺少独立的权限版本订阅和缓存失效通道。

这些实现适合本地管理和早期部署，不作为多管理员、多租户生产控制面的最终形态。

## 4. 设计原则

1. **默认拒绝**：无法识别的权限、状态、策略版本或主体默认拒绝。
2. **硬拒绝优先**：账号冻结、Topic 冻结和成员封禁优先于任何允许权限。
3. **最小权限**：操作者不能授予自己没有的能力，也不能管理同级或更高角色。
4. **角色不是状态**：`banned`、`removed` 和 `left` 是成员状态，不是权限为零的角色别名。
5. **能力名称稳定**：公开能力键一旦发布不得改变含义；废弃的位不得复用。
6. **热路径本地化**：正常消息请求只使用 Topic Actor 内存中的有效能力投影。
7. **权限和业务事实分离**：IM 不复制钱包、客户归属等业务主表，只消费版本化关系投影。
8. **变更可解释**：管理操作和拒绝结果应能说明命中的角色、覆盖、条件和策略版本。
9. **服务端为准**：客户端隐藏无权限入口，但不得替代服务端校验。
10. **兼容迁移**：新旧客户端和数据库适配器在迁移窗口内可以同时运行。

## 5. 目标架构

```mermaid
flowchart LR
    I["认证主体"] --> H["硬拒绝状态"]
    H --> R["关系与资源作用域"]
    R --> B["角色能力模板"]
    B --> O["Topic/成员 Allow-Deny 覆盖"]
    O --> C["受限 ABAC 条件"]
    C --> P["版本化有效能力投影"]
    P --> T["Topic Actor 本地检查"]
```

授权上下文至少包含：

```text
subject: 用户、员工、服务账号
resource: Topic、消息、成员、群通话、后台资源
action: send、delete、invite、ban、manage_call 等
tenant: 机构或业务租户
state: 账号、Topic、成员当前状态
context: 当前时间、会话类型和受信业务关系版本
```

## 6. 能力位设计

### 6.1 保留基础 AccessMode

现有八位继续用于协议兼容和最高频的 Topic 数据面判断：

```text
J -> topic.join
R -> message.read
W -> message.send
P -> presence.read
A -> legacy.member.manage
S -> member.invite
D -> message.delete_any
O -> topic.owner
```

`A`、`D` 和 `O` 的旧行为在兼容期仍然有效，但新代码不得继续仅凭 `A` 判断所有管理
操作。新接口优先检查细粒度能力；没有新能力数据时，才使用受控的旧 ACL 映射。

### 6.2 新增细粒度能力

控制面使用稳定字符串作为权威名称，Topic 热路径使用编译后的 `uint64` 位图。初始能力
目录建议如下，最终位号由一个集中注册表分配：

| 分类 | 能力键 | 用途 |
| --- | --- | --- |
| Topic | `topic.view` | 查看 Topic 和基本资料 |
| Topic | `topic.change_info` | 修改名称、头像和描述 |
| Topic | `topic.change_policy` | 修改加入、私聊、全员禁言等策略 |
| Topic | `topic.delete` | 删除整个 Topic |
| Topic | `topic.transfer_owner` | 转移所有权 |
| 消息 | `message.read` | 读取历史和实时消息 |
| 消息 | `message.send` | 发送文本消息 |
| 消息 | `message.send_media` | 发送文件、图片、视频和语音 |
| 消息 | `message.edit_own` | 编辑自己的消息 |
| 消息 | `message.delete_own` | 删除自己的消息 |
| 消息 | `message.delete_any` | 删除其他成员的消息 |
| 消息 | `message.pin` | 置顶和取消置顶消息 |
| 消息 | `message.react` | 添加和撤销反应 |
| 成员 | `member.list` | 查看允许公开的成员列表 |
| 成员 | `member.invite` | 邀请成员 |
| 成员 | `member.approve` | 审批加入请求 |
| 成员 | `member.restrict` | 禁言或设置只读期限 |
| 成员 | `member.remove` | 移出成员但允许以后重新加入 |
| 成员 | `member.ban` | 封禁成员并阻止重新加入 |
| 成员 | `member.assign_moderator` | 分配受限管理员 |
| 成员 | `member.assign_admin` | 分配管理员 |
| 频道 | `channel.publish` | 以频道发布消息 |
| 频道 | `channel.manage_publishers` | 管理发布者 |
| 通话 | `call.join` | 加入群组通话 |
| 通话 | `call.start` | 创建群组通话 |
| 通话 | `call.manage` | 结束通话和管理参与者 |

能力注册表必须满足：

- 位号只增加、不重排、不复用。
- 数据库存储使用十进制字符串或固定宽度二进制，不能依赖数据库有符号 `BIGINT` 的差异。
- 协议输出使用字符串，客户端不能用 JavaScript `Number` 解析超过安全范围的整数。
- 服务端加载 Topic 时只解析一次为 `uint64`，消息热路径不解析字符串。
- 超过 64 个高频能力前先评审，不通过无限扩位替代清晰的资源边界。

### 6.3 角色模板

建议的 Topic 角色如下：

| 角色 | 能力边界 |
| --- | --- |
| `owner` | Topic 全部能力，包括转移所有权 |
| `admin` | 群资料、成员和内容管理；不能转移所有权或管理同级管理员 |
| `moderator` | 删除消息、置顶、禁言、移出；不能分配管理员或删除 Topic |
| `publisher` | 频道发布、编辑自己的频道消息 |
| `member` | 读取、发送、编辑和删除自己的消息、加入通话 |
| `readonly` | 读取消息、允许时添加反应和加入通话 |
| `subscriber` | 频道只读订阅者 |

`banned` 不再作为正式角色。兼容协议接收到 `role=banned` 时，服务端将其转换为
`membership.state=banned`，并清空有效能力投影。

角色模板需要带 `role_version`。模板升级时不能静默扩大全部历史角色权限；扩大权限必须
显式迁移，缩小高风险权限可以通过策略版本立即生效。

## 7. 权限计算顺序

有效权限必须按固定顺序计算，禁止不同接口自行拼接判断：

```text
1. 校验认证主体和租户边界。
2. 检查 account/topic/membership 硬拒绝状态。
3. 读取用户与 Topic、机构和业务对象的有效关系。
4. 合并角色模板的基础能力。
5. 应用 Topic 级和成员级 allow/deny 覆盖。
6. 计算禁言期限、官方状态、加入策略等受限条件。
7. 生成包含 policy_version 和 expires_at 的有效能力投影。
8. 使用 action 对应的能力位执行最终判断。
```

推荐判断公式：

```text
if hardDenied(subject, topic, membership) {
    deny
}

effective = roleCapabilities | relationCapabilities | allowOverrides
effective = effective &^ denyOverrides

allow = effective.contains(requiredCapability) && conditionsMatch(context)
```

硬拒绝状态不能被成员级 `allow` 覆盖。`denyOverrides` 的优先级高于普通允许，以减少
权限冲突时的意外放行。

## 8. 成员关系与状态

### 8.1 保留 Subscription 主关系

现有 Subscription 继续作为以下数据的权威来源：

```text
(topic, user) -> membership + message cursors + private data
```

建议逐步增加或拆分以下字段：

```text
membership_state: active | left | removed | banned | suspended
role_id
role_version
cap_allow
cap_deny
capability_version
muted_until
ban_until
state_reason_code
state_changed_by
state_changed_at
```

其中：

- `left`：用户主动退出，可以通过公开或有效邀请再次加入。
- `removed`：管理员移出，可以通过新的有效邀请或公开策略再次加入。
- `banned`：禁止重新加入，只有具备解封能力的管理员可以恢复。
- `suspended`：临时安全状态，不删除成员关系和同步游标。

### 8.2 关系授权

当权限跨越 Topic 后，使用关系表达作用域：

```text
user:u1 member topic:g1
user:u2 admin topic:g1
topic:g1 belongs_to organization:o1
user:u3 org_owner organization:o1
employee:e1 assigned_to customer:c1
```

早期阶段不新增独立关系数据库：Topic 成员继续来自 Subscription，机构和客户关系通过
业务服务的版本化投影提供。只有多个服务需要统一执行同一关系策略时，才抽取独立授权
服务。

## 9. ABAC 使用边界

ABAC 只用于角色和关系无法稳定表达的动态限制：

- `account.state == active`
- `topic.official_status == verified`
- `membership.muted_until < request.time`
- `subject.org_id == topic.org_id`
- `topic.join_policy` 是否允许当前加入方式
- 受信业务关系版本是否仍有效
- 高风险后台操作是否处于审批窗口内

禁止：

- 在每次消息广播或逐接收者投递时执行通用表达式引擎。
- 从客户端可写的 `Public`、`Private`、消息 `Head` 或任意 JSON 字段读取安全属性。
- 允许上传 JavaScript、SQL 或不受限脚本作为权限策略。
- 因远程属性服务超时而默认允许。

条件语言应采用白名单字段和受限操作符，先支持等值、集合包含、时间比较和固定前缀。
只有控制面确实需要复杂条件时再引入 CEL 等表达式，并在保存策略时完成解析和类型检查。

## 10. 管理控制面升级

### 10.1 身份化管理 API

bootstrap token 只保留为首次初始化和紧急恢复凭据。正常后台请求必须使用可识别主体：

```text
Authorization -> admin subject -> organization/domain -> required permission
```

每个管理路由声明唯一权限键，例如：

```text
PUT /roles/*                 -> system.roles.write
PUT /settings                -> system.settings.write
POST /official-topics        -> official_topics.create
PATCH /official-topics/*     -> official_topics.manage
POST /members/*/ban          -> moderation.ban
```

统一中间件在进入业务处理前调用 `Evaluate`。Web Admin 使用同一权限目录控制菜单、按钮和
路由，但服务端必须再次校验。

### 10.2 规范化存储

数据量和管理员数量增长后，将当前整体控制面文档拆为：

```text
auth_roles
auth_permissions
auth_role_permissions
auth_bindings
auth_policy_conditions
auth_policy_versions
auth_audit_events
auth_outbox
```

要求：

- 所有变更在数据库事务中写入策略和 Outbox。
- 审计事件追加写入并按保留策略归档，不随快照截断。
- `expected_version` 继续作为乐观并发控制。
- 集群节点订阅版本事件并原子切换本地策略快照。
- Redis 可以缓存策略，不能成为唯一事实来源。

## 11. 热路径缓存与一致性

### 11.1 Topic Actor 投影

Topic Actor 中为活跃成员保存：

```text
effectiveCapabilities uint64
policyVersion          uint64
validUntil             time.Time
membershipState        enum
```

消息发布、编辑、删除、置顶和通话入口只检查该投影。后台任务和连接处理器仍必须通过
Topic 主循环更新权限，不能跨协程直接修改 `perUser`。

### 11.2 失效机制

权限变更事务提交后发布：

```text
AuthorizationChanged {
    subject
    topic
    tenant
    policy_version
    change_kind
}
```

处理规则：

- Topic Owner 节点收到事件后重新计算受影响成员，不扫描全部 Topic。
- 边缘节点删除对应 Session 的授权缓存。
- 事件重复必须幂等；旧版本事件不得覆盖新版本。
- 丢失事件时由短 TTL 或版本检查最终收敛。
- 封禁、冻结和撤销管理员等高风险操作必须主动断开或降权已有 Session。

### 11.3 一致性等级

| 操作 | 一致性要求 |
| --- | --- |
| 普通消息读取 | 可使用短期缓存和版本检查 |
| 普通消息发送 | 使用 Topic Owner 当前投影 |
| 封禁、冻结、撤销管理员 | 提交成功后必须立即使旧投影失效 |
| 分配管理员、转移所有权 | 强一致校验当前角色和策略版本 |
| 后台高风险操作 | 强身份、强审计、失败关闭 |

## 12. 大数据量设计

权限数据规模由成员关系边决定：

```text
subscription rows = 用户数 × 平均会话数
```

例如一千万用户、平均五十个会话，会产生约五亿条 Subscription。RBAC 可以减少角色配置，
但不能消除这些关系和已读游标。

规模增长时执行以下策略：

1. Topic 成员和消息以 Topic 为主要分片键，保证广播和历史读取局部化。
2. 维护以用户为键的会话列表投影，避免用户首页跨 Topic 分片扫描。
3. 大群和频道成员查询必须使用稳定游标，禁止深 OFFSET 和全量返回。
4. 频道普通读者不全部常驻 Topic Actor；只维护在线参与者和必要聚合状态。
5. 离线推送、审计和权限投影更新使用有界异步队列，不能阻塞消息 Sequencer。
6. 权限指标不得把 user/topic 作为常驻标签，避免监控高基数失控。
7. 消息和 Subscription 分片前先完成双向查询投影、备份恢复和重分片演练。

## 13. 分阶段演进路线

### 阶段 0：冻结语义和补齐测试

目标：在修改存储之前固定当前行为。

- 建立权限键注册表和旧 ACL 映射表。
- 为消息、成员、Topic、频道、通话建立授权矩阵测试。
- 固定 `left`、`removed`、`banned` 的不同语义。
- 增加“不越权授权”“管理员不能管理同级”“频道订阅者永远不能发布”等性质测试。
- 记录当前授权判断延迟和 Topic 内存基线。

完成标准：所有现有客户端行为有兼容测试，拒绝原因可以稳定分类。

### 阶段 1：新增能力位并保持双读

目标：细化 Topic 管理权限，不中断旧客户端。

- 新增能力注册表、角色模板版本和有效能力计算器。
- Subscription 增加成员状态、角色、allow/deny 和策略版本字段。
- 从旧 `ModeWant & ModeGiven` 回填初始能力。
- 新接口写入 ACL 和能力数据，旧接口继续只写旧 ACL 时触发兼容映射。
- 同时计算旧 ACL 结果和新能力结果，记录差异但暂不改变生产结果。

完成标准：影子校验无未知差异，新增细粒度操作不再依赖 `A` 一个权限位。

### 阶段 2：切换 Topic 授权执行

目标：新能力成为管理动作的权威来源。

- 消息基础读写继续使用 ACL 快速路径。
- 删除他人消息、置顶、成员治理、角色分配和群通话切换到细粒度能力。
- Topic Actor 保存版本化有效能力投影。
- 权限变化通过 Topic 主循环立即生效。
- API 返回稳定角色和 capabilities，客户端按 capabilities 隐藏入口。

完成标准：旧 ACL 与新能力并存，旧客户端仍可使用，所有敏感操作由服务端细粒度校验。

### 阶段 3：升级平台 RBAC 和多租户边界

目标：支持多管理员、多机构和集群一致策略。

- 管理 API 接入真实管理员身份，不再把 bootstrap token 当日常账号。
- 为所有后台路由增加权限中间件。
- 规范化角色、绑定、审计和策略版本存储。
- 加入 `tenant_id/org_id` 硬边界和少量受限 ABAC 条件。
- 使用事务 Outbox 分发权限版本事件。

完成标准：每次后台变更可归属到真实主体，跨机构访问默认拒绝，节点策略版本可观测。

### 阶段 4：关系授权服务

只有满足以下任一条件时才进入本阶段：

- 三个及以上业务服务需要执行相同的用户—机构—客户—Topic 权限。
- 关系不再能通过一次 Subscription 或机构查询完成，需要多跳继承。
- 权限关系达到数据库单实例和本地快照无法稳定承载的规模。
- 需要统一的 `Check`、`BatchCheck`、`ListObjects` 和权限解释接口。

此阶段把关系和策略事实抽取到独立授权服务，但 `server-chat` 仍消费编译后的能力投影，
不得让每条消息依赖一次远程授权 RPC。

## 14. 数据库迁移与回滚

每次权限结构升级都遵循：

```text
扩展 Schema -> 双写 -> 回填 -> 影子读取比较 -> 分批切换读取 -> 停止旧写 -> 清理旧字段
```

具体要求：

- 数据库版本升级必须走 `init-db` 迁移流程，禁止手工修改版本号。
- 回填按主键或 Topic 游标分批执行，避免长事务和全表锁。
- 双写必须使用相同事务或可恢复 Outbox，不能出现 ACL 成功而能力写入丢失。
- 影子阶段输出聚合差异原因，不记录用户敏感数据。
- 切换按租户、Topic 哈希或节点逐批进行，支持快速回退到旧 ACL 判断。
- 不删除旧字段，直到所有客户端兼容窗口结束且备份恢复演练通过。

## 15. API 与错误语义

内部授权接口建议统一为：

```go
type AuthorizationRequest struct {
    Subject  string
    Tenant   string
    Topic    string
    Resource string
    Action   string
    Context  AuthorizationContext
}

type AuthorizationDecision struct {
    Allowed       bool
    ReasonCode    string
    PolicyVersion uint64
    ExpiresAt     time.Time
}
```

稳定拒绝原因至少包含：

```text
unauthenticated
account_suspended
topic_suspended
membership_required
membership_banned
tenant_mismatch
capability_missing
condition_not_met
policy_stale
```

客户端只展示适合用户理解的英文提示；服务端日志和审计记录稳定错误码，不依赖提示文字
做业务判断。

## 16. 可观测性与审计

建议新增低基数指标：

```text
authz_decisions_total{action,result,reason}
authz_decision_duration_seconds{path}
authz_projection_cache_hits_total{result}
authz_policy_version_lag
authz_invalidation_queue_depth
authz_invalidation_failures_total
```

审计范围：

- 角色和能力模板变更。
- 管理员绑定、到期和撤销。
- 成员禁言、移出、封禁和解封。
- 管理员提升、降级和所有权转移。
- 官方 Topic 状态和策略变更。
- 高风险操作拒绝。

普通成功消息发送不逐条写授权审计，避免审计量与消息量相同；消息自身的持久化记录承担
业务追踪，授权系统只记录权限变化和高风险决定。

## 17. 测试与发布门禁

### 17.1 权限矩阵

至少覆盖：

- 群组和频道所有角色的每项能力。
- owner、admin、moderator 的层级限制。
- 普通用户、只读用户、被移出用户和封禁用户。
- Topic 冻结、全员禁言和临时禁言。
- 权限变更前后的已有 WebSocket Session。
- P2P、普通群、频道和官方大群差异。
- 群通话创建、退出、重新加入和管理。

### 17.2 一致性测试

- 重复和乱序失效事件不产生权限回退。
- 节点重启后从数据库恢复相同策略版本。
- 权限撤销后旧 Session 不能继续执行敏感操作。
- 集群 Owner 迁移后权限投影与消息 seq 同时保持正确。
- 策略服务不可用时高风险写入失败关闭，已有安全读取按明确策略处理。

### 17.3 性能门禁

- 消息发送本地授权判断不得产生数据库或网络访问。
- 位图判断不得引入热路径堆分配。
- 权限投影更新不能阻塞 Topic Sequencer 的消息持久化。
- 权限变更风暴需要记录 p95/p99 生效时间、队列水位和内存增长。
- 百人群、千人群、万人群和频道分别建立容量基线。

## 18. 明确不采用的方案

- 删除现有 Topic ACL，所有请求改为实时调用通用 ABAC 引擎。
- 把所有细粒度权限继续塞进 `A`、`D` 和 `O` 三个旧标志。
- 把 `ModeNone`、软删除和封禁视为同一成员状态。
- 只通过客户端隐藏入口实现权限控制。
- 在消息广播循环中逐成员执行 Casbin、SQL 或远程 RPC。
- 把全部控制面和无限审计长期保存在一个 JSON 文档中。
- 权限变更只依赖 TTL，允许已撤销管理员在 TTL 内继续操作。
- 在关系模型尚未复杂化前直接建设完整的全局授权服务。

## 19. 实施优先级

| 优先级 | 项目 | 原因 |
| --- | --- | --- |
| P0 | 分离 `left/removed/banned` 状态 | 直接影响邀请、重新加入和安全语义 |
| P0 | 后台 API 真实身份和逐路由授权 | 当前共享管理凭据无法支持多管理员审计 |
| P0 | 权限矩阵与越权测试 | 后续迁移的兼容基线 |
| P1 | 细粒度能力注册表和角色模板 | 解决 `A` 权限过宽问题 |
| P1 | Topic Actor 能力投影和版本失效 | 保证热路径性能和撤权时效 |
| P1 | 持久化审计与 Outbox | 支持集群一致性和问题追踪 |
| P2 | 控制面数据规范化 | 管理员、租户和审计增长后的必要升级 |
| P2 | 受限 ABAC 条件 | 支持禁言期限、机构范围和官方策略 |
| P3 | 独立关系授权服务 | 仅在跨服务、多跳关系和超大规模后建设 |

## 20. 相关实现与参考

项目内实现：

- [`server/store/types/access_mode.go`](../server/store/types/access_mode.go)
- [`server/store/types/models.go`](../server/store/types/models.go)
- [`internal/server/topic_roles.go`](../internal/server/topic_roles.go)
- [`internal/server/topic_msg.go`](../internal/server/topic_msg.go)
- [`internal/server/topic_meta_subscriptions.go`](../internal/server/topic_meta_subscriptions.go)
- [`internal/server/message_interactions.go`](../internal/server/message_interactions.go)
- [`internal/server/calls_agora.go`](../internal/server/calls_agora.go)
- [`internal/server/admin_http.go`](../internal/server/admin_http.go)
- [`server/admin/control_plane.go`](../server/admin/control_plane.go)
- [`server/admin/catalog.go`](../server/admin/catalog.go)
- [代码架构与维护边界](code-architecture.md)
- [数据库版本与迁移流程](database-migrations.md)
- [集群容量基线](cluster-capacity-baseline.md)

外部架构参考：

- [Google Zanzibar：一致的全球授权系统](https://research.google/pubs/zanzibar-googles-consistent-global-authorization-system/)
- [Discord 权限与频道覆盖](https://docs.discord.com/developers/topics/permissions)
- [Telegram 管理员细粒度权限](https://core.telegram.org/type/ChatAdminRights)
- [Google Cloud IAM 角色、资源与条件](https://cloud.google.com/iam/docs/overview)
- [AWS 基于属性的访问控制](https://docs.aws.amazon.com/IAM/latest/UserGuide/introduction_attribute-based-access-control.html)
