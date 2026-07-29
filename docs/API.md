<!-- TOC depthfrom:1 depthto:6 withlinks:true updateonsave:true orderedlist:false -->

- [服务端 API 规范说明文档 (Server API)](#服务端-api-规范说明文档-server-api)
  - [工作原理与设计理念](#工作原理与设计理念)
  - [通用约定与数据格式](#通用约定与数据格式)
  - [连接服务端的方式](#连接服务端的方式)
    - [gRPC 端点](#grpc-端点)
    - [WebSocket 端点](#websocket-端点)
    - [Long Polling 长轮询端点](#long-polling-长轮询端点)
    - [带外大文件上传与下载](#带外大文件上传与下载)
    - [运行在反向代理（如 Nginx）之后](#运行在反向代理如-nginx-之后)
  - [用户与账号系统 (Users)](#用户与账号系统-users)
    - [身份认证机制 (Authentication)](#身份认证机制-authentication)
      - [创建账号 ({acc})](#创建账号-acc)
      - [登录认证 ({login})](#登录认证-login)
      - [更新认证参数](#更新认证参数)
      - [重置密码 (&#34;忘记密码&#34; 流程)](#重置密码-忘记密码-流程)
    - [冻结/挂起用户](#冻结挂起用户)
    - [凭据验证 (Credential Validation)](#凭据验证-credential-validation)
    - [访问权限控制 ACL (Access Control)](#访问权限控制-acl-access-control)
  - [主题与会话 (Topics)](#主题与会话-topics)
    - [me 个人主题](#me-个人主题)
    - [fnd 与 Tag 标签：用户与主题搜索](#fnd-与-tag-标签用户与主题搜索)
    - [点对点 P2P 主题](#点对点-p2p-主题)
    - [群组主题 (Group Topics)](#群组主题-group-topics)
    - [sys 系统主题](#sys-系统主题)
  - [使用服务端序列号 (Sequence ID)](#使用服务端序列号-sequence-id)
  - [User Agent 与在线状态 Notification](#user-agent-与在线状态-notification)
  - [Trusted, Public, Private, Auxiliary 扩展字段](#trusted-public-private-auxiliary-扩展字段)
  - [消息内容格式 (Content Format)](#消息内容格式-content-format)
  - [带外大文件处理规约](#带外大文件处理规约)
  - [推送通知 (Push Notifications)](#推送通知-push-notifications)
  - [音视频通话 (Video Calls)](#音视频通话-video-calls)
  - [链接预览 (Link Previews)](#链接预览-link-previews)
  - [消息报文详细规范 (Messages Spec)](#消息报文详细规范-messages-spec)
    - [客户端发往服务端报文 (C2S)](#客户端发往服务端报文-c2s)
      - [{hi} 握手报文](#hi-握手报文)
      - [{acc} 建号/修改账号报文](#acc-建号修改账号报文)
      - [{login} 登录报文](#login-登录报文)
      - [{sub} 订阅主题报文](#sub-订阅主题报文)
      - [{leave} 退订/离开报文](#leave-退订离开报文)
      - [{pub} 发布消息报文](#pub-发布消息报文)
      - [{get} 查询元数据报文](#get-查询元数据报文)
      - [{set} 修改元数据报文](#set-修改元数据报文)
      - [{del} 删除报文](#del-删除报文)
      - [{note} 状态通知报文](#note-状态通知报文)
    - [服务端发往客户端报文 (S2C)](#服务端发往客户端报文-s2c)
      - [{data} 聊天数据报文](#data-聊天数据报文)
      - [{ctrl} 控制响应报文](#ctrl-控制响应报文)
      - [{meta} 元数据响应报文](#meta-元数据响应报文)
      - [{pres} 在线状态通知报文](#pres-在线状态通知报文)
      - [{info} 客户端通知副本报文](#info-客户端通知副本报文)

<!-- /TOC -->

# 服务端 API 规范说明文档 (Server API)

## 工作原理与设计理念

IM 是一个轻量级、高并发的即时通讯 (IM) 消息路由与存储服务器。在概念上，它遵循 [发布-订阅 (Publish-Subscribe)](https://en.wikipedia.org/wiki/Publish%E2%80%93subscribe_pattern) 消息模型。

服务端将网络连接划分为三个核心概念：**会话 (Session)**、**用户 (User)** 与 **主题 (Topic)**：

- **Session**：客户端与 IM 服务端之间建立的单个网络连接（如 WebSocket、gRPC Stream 或 Long Polling）。
- **User**：连接到服务端的真实用户实体。单个用户可以同时拥有多个在线 Session（多设备登录）。
- **Topic**：用于在多个 Session 之间路由消息内容的命名通道。

用户与主题均被赋予全局唯一的字符串 ID：

- **用户 ID**：以 `usr` 为前缀，后跟 Base64-URL 编码的 64 位随机数值，例如 `usr2il9suCbuko`。
- **主题 ID**：包含 `me`、`fnd`、P2P 主题（格式如 `usr...`）以及群组主题（前缀 `grp` 后跟 11 位随机字符，如 `grpYiqEXb4QY6s`）。

客户端（如 Web、Android、iOS 应用）通过连接服务端建立 Session。大部分操作均需要进行身份认证。客户端通过发送 `{login}` 报文完成 Session 认证，认证成功后获取 Token 令牌用于后续快速登录。

认证成功后，用户可以通过加入不同类型的主题与其他用户交流：

* **`me` 主题**：每个用户专属的个人主主题，用于管理个人 Profile 资料以及接收来自其他主题的状态变更 Notice Notification。
* **`fnd` 主题**：用于查找/搜索其他用户或群组主题的通道。
* **点对点 (P2P) 主题**：严格存在于两个用户之间的双人私聊通道。
* **群组 (Group) 主题**：多人聊天通道。需显式创建。

Session 通过发送 `{sub}` 报文加入主题。加入成功后，用户通过发送 `{pub}` 报文发布消息，消息将被服务端广播分发为 `{data}` 报文并送达其他在线 Session。

---

## 通用约定与数据格式

1. **时间戳**：一律表示为 [RFC 3339](http://tools.ietf.org/html/rfc3339) 格式的 UTC 字符串，精确到毫秒，如 `"2026-07-23T18:07:29.841Z"`。
2. **Base64 编码**：本文档涉及的 Base64 均为剥离了尾部填充符 `=` 的 URL 安全编码格式（详见 [RFC 4648](http://tools.ietf.org/html/rfc4648)）。
3. **Sequence ID**：`{data}` 数据报文拥有服务端自增的 32 位整数 ID (`seq_id`)，自 `1` 开始在单个 Topic 内连续单调递增，保证单 Topic 内部严格唯一且有序。
4. **客户端 Packet ID (`id`)**：为了将请求与响应相互关联，客户端可以在发往服务端的每个报文中附带一个自定义字符串 `id`。服务端处理完毕后会在对应的响应报文中原样返回该 `id`。

---

## 连接服务端的方式

IM 支持三种网络接入方式：**WebSocket**、**Long Polling (长轮询)** 以及 **gRPC**。

HTTP(S) 服务对外暴露以下接口端点：

* `/v0/channels`：WebSocket 握手端点。
* `/v0/channels/lp`：Long Polling 长轮询端点。
* `/v0/file/u`：带外大文件上传端点。
* `/v0/file/s`：带外大文件下载服务端点。

客户端发起的每个 HTTP(S) 请求均须附带 API Key。服务端会依次检查以下位置：

1. HTTP 请求头 `X-IM-APIKey`
2. URL Query 参数 `apikey` (`/v0/file/s/abcdef.jpg?apikey=...`)
3. Form 表单字段 `apikey`
4. Cookie 字段 `apikey`

生成生产环境专用的 API Key 请使用 [keygen](../keygen) 工具。

建立连接后，客户端必须首先发送 `{hi}` 握手报文，服务端将回复包含服务协议版本号的 `{ctrl}` 响应。

---

### gRPC 端点

Protocol Buffer 协议定义文件参见 [model.proto](../pbx/model.proto)。gRPC 接口提供双向流 `MessageLoop(stream ClientMsg) returns (stream ServerMsg)`，并额外允许超级管理员 (`ROOT`) 权限代发消息和管理用户。

### WebSocket 端点

消息在文本帧 (Text Frame) 中传输，一帧包含一条 JSON 消息。

### Long Polling 长轮询端点

基于 `HTTP POST` 或 `GET` 传输。首个请求返回的 `{ctrl}` 报文中包含 `sid`（Session ID），后续每个请求均须带上 `sid`。

### 运行在反向代理（如 Nginx）之后

IM 支持挂载在反向代理之后，并支持开启 `unix:/run/im.sock` Unix Domain Socket 通信，开启 `use_x_forwarded_for: true` 解析真实客户端 IP。

---

## 用户与账号系统 (Users)

用户具有 3 种认证权限级别：

- `auth`：已验证身份的正式用户。
- `anon`：匿名临时用户。
- `root`：仅在 gRPC 中可用的超级管理员权限。

每个用户拥有以下核心属性：

- `created` / `updated`：创建与最后更新时间戳。
- `status`：账号状态（`ok`-正常，`susp`-冻结，`del`-已删除）。
- `defacs`：默认访问权限设置 (`auth` 与 `anon`)。
- `public` / `private` / `trusted`：公开资料、个人私有数据与管理员受信任字段。
- `tags`：用于用户发现的搜索标签及凭据。

### 身份认证机制 (Authentication)

IM 内置多种认证适配器：

* `token`：基于密码学 Token 的轻量级快速认证（推荐作为主要登录手段，不校验数据库，纯内存高效处理）。
* `basic`：基于 `username:password` 账号密码登录。
* `anonymous`：基于匿名账号机制。
* `rest`：通过 [REST 外部认证服务](../server/auth/rest/) 对接企业现有的 OAuth / 用户中心。

#### 创建账号 ()

发送 `{acc}` 报文创建新用户。当设置 `login: true` 时，服务端将在建号成功后自动为当前 Session 完成登录认证并返回 Token。

#### 登录认证 ()

发送 `{login}` 报文登录。登录成功后返回 200 响应及 Token 令牌。

#### 重置密码 ("忘记密码" 流程)

发送 `scheme: "reset"` 的 `{login}` 请求，参数为 Base64 编码的 `"basic:email:user@example.com"`。服务端验证后将向目标邮箱发送重置验证码/Token，随后使用带 Token 的 `{acc}` 请求更新密码。

### 访问权限控制 ACL (Access Control)

IM 使用基于位图的 ACL 权限控制体系。用户的实际权限由 **用户申请的权限 (Want)** 与 **管理员授予的权限 (Given)** 进行按位与 (Bitwise AND) 计算决定。

权限位说明：

* `N`：无权限 (No Access)。
* `J`：订阅加入主题权限 (Join)。
* `R`：接收读取消息权限 (Read)。
* `W`：发布发送消息权限 (Write)。
* `P`：接收在线状态 Notification 权限 (Presence)。
* `A`：管理/审批主题权限 (Approve / Admin)。
* `S`：邀请其他成员权限 (Sharing)。
* `D`：删除消息权限 (Delete)。
* `O`：主题所有者权限 (Owner)。

---

## 主题与会话 (Topics)

### me 个人主题

每个用户拥有的核心主主题。用于接收个人订阅列表的更新通知、他人在线状态 Notifications (`{pres}`) 等。

### fnd 与 Tag 标签：用户与主题搜索

协议 `0.28` 通过订阅 `fnd` 主题发送 `set.tags` 或 `get` 查询，可以在系统中搜索其他用户或公开群组。除原有精确 Tag 组合查询外，`get.what=search` 支持关键词发现：

```json
{
  "get": {
    "id": "peer-search-1",
    "topic": "fnd",
    "what": "search",
    "search": {
      "q": "release",
      "scope": "peers",
      "limit": 20
    }
  }
}
```

用户仅能通过配置的公开 alias Tag（默认 `alias:<name>`）被发现，不能仅凭显示昵称枚举账号；群组和频道可通过 alias Tag 或 `public.fn` 命中。服务端过滤暂停账号、内部 Topic 和默认 ACL 不允许加入的私有群，并按“alias 精确、alias 前缀、alias 子串、公开名称、订阅数、Topic 名称”的稳定顺序排序。

响应通过 `{meta.search}` 返回，`private` 中只包含本次命中的公开 alias，不会返回邮箱、手机号等其它索引 Tag：

```json
{
  "meta": {
    "id": "peer-search-1",
    "topic": "fnd",
    "search": {
      "scope": "peers",
      "peers": [
        {
          "topic": "grpYiqEXb4QY6s",
          "public": { "fn": "Release Notes" },
          "private": ["alias:release"]
        }
      ],
      "next": "opaque-cursor"
    }
  }
}
```

`next` 是与关键词及过滤条件绑定的不透明游标；下一页把它原样放到 `search.cursor`，不要解析或跨查询复用。

#### 当前会话消息全文搜索

已订阅 P2P、普通群或广播频道管理 Topic 的读者，可以在当前 Topic 中搜索文本、Drafty 可见文本及附件名称/URL：

```json
{
  "get": {
    "id": "message-search-1",
    "topic": "grpYiqEXb4QY6s",
    "what": "search",
    "search": {
      "q": "版本说明",
      "scope": "topic",
      "from": "usr2il9suCbuko",
      "kinds": ["text", "file"],
      "min_date": "2026-01-01T00:00:00Z",
      "max_date": "2027-01-01T00:00:00Z",
      "limit": 20
    }
  }
}
```

- `q` 为 2–256 个 Unicode 字符。
- `kinds` 可取 `text`、`drafty`、`image`、`video`、`voice`、`audio`、`file`。
- `min_date` 包含边界，`max_date` 不包含边界。
- 结果按 `seq` 倒序返回；下一页继续使用响应中的 `next`。
- 服务端再次校验 Read ACL，并排除硬删除及当前用户已软删除的消息。
- 搜索结果仍使用标准消息字段，位于 `{meta.search.messages}`。

CLI 对应命令示例：

```text
get --search="版本说明" --scope=topic --from=usr2il9suCbuko --kinds=text,file --limit=20 grpYiqEXb4QY6s
get --search=release --scope=peers --limit=20 fnd
```

### 点对点 P2P 主题

双人私聊会话通道。主题名称直接对应对方用户的 ID（如 `usr2il9suCbuko`）。

### 群组主题 (Group Topics)

多人聊天会话通道（前缀 `grp`，如 `grpYiqEXb4QY6s`）。支持将其配置为只读频道 (Channel) 模式。

#### 创建普通群组与广播频道

- 订阅临时 Topic `new` 创建普通群组；成员按 ACL 获得读写权限。
- 订阅临时 Topic `nch` 创建广播频道。创建者和发布者使用返回的 `grp...` 名称管理、发布，普通读者使用对应的 `chn...` 名称订阅。
- 服务端会在 `{meta.desc}` 中返回 `chan` 和 `subcnt`；gRPC 的对应字段为 `is_chan` 和 `sub_count`。

```json
{ "sub": { "id": "create-group", "topic": "new", "set": { "desc": { "public": { "fn": "研发群" } } } } }
{ "sub": { "id": "create-channel", "topic": "nch", "set": { "desc": { "public": { "fn": "产品公告" } } } } }
```

#### 成员角色与 ACL

管理员先订阅 `grp...` 管理 Topic，再通过 `{set.sub}` 的 `role` 字段管理成员。`role` 与底层 `mode` 互斥：

- 普通群：`admin`、`member`、`readonly`、`banned`。
- 广播频道：`admin`、`publisher`、`subscriber`、`banned`；`readonly` 是 `subscriber` 的兼容别名。
- 只有群主可以提升或调整管理员。普通管理员不能授予自己不具备的 ACL。
- `banned` 会保留拒绝加入的订阅记录；`del what=sub` 是移出成员，之后仍可重新邀请。

```json
{ "set": { "id": "mute-1", "topic": "grpYiqEXb4QY6s", "sub": { "user": "usrTarget", "role": "readonly" } } }
{ "set": { "id": "publisher-1", "topic": "grpYiqEXb4QY6s", "sub": { "user": "usrTarget", "role": "publisher" } } }
{ "set": { "id": "ban-1", "topic": "grpYiqEXb4QY6s", "sub": { "user": "usrTarget", "role": "banned" } } }
{ "del": { "id": "kick-1", "topic": "grpYiqEXb4QY6s", "what": "sub", "user": "usrTarget" } }
```

`{meta.sub[].acs.role}` 会返回服务端根据最终 ACL 推导的角色。频道管理员默认查询 `grp...` 发布者；要列出包括离线用户在内的频道读者，在 `get.sub.topic` 指定对应的 `chn...`：

```json
{ "get": { "id": "list-readers", "topic": "grpYiqEXb4QY6s", "what": "sub", "sub": { "topic": "chnYiqEXb4QY6s", "limit": 100 } } }
```

广播频道读者在发布、编辑、定时发送和输入状态路径上都由服务端强制只读，即使旧订阅数据错误地含有 `W` 位也不能发言。普通群的 `member` 角色保留双向读写能力。

---

## 音视频通话 (Video Calls)

协议 `0.29` 同时保留两种媒体通话路径：

- P2P Topic 使用原有 WebRTC 信令，由业务 WebSocket 转发 `ringing`、`accept`、`offer`、`answer`、`ice-candidate` 和 `hang-up`。
- 普通群组 Topic 使用 Agora RTC。业务服务端只负责邀请、ACL、短期 AccessToken2、参与状态和通话日志，音频和视频媒体流由 Agora SDK 传输。

当前交付状态：

| 范围 | 状态 | 说明 |
|---|---|---|
| P2P WebRTC 服务端信令 | ✅ 已实现 | 需要部署真实 STUN/TURN 并进行生产网络验证 |
| Agora 群组通话服务端 | ✅ 已实现 | 已实现 Token、ACL、加入、离开、续期、断线清理和状态持久化 |
| 正式客户端 Agora SDK | 🟡 待接入 | Web、Android、iOS 客户端需根据下发凭证调用 Agora SDK |
| Agora 真实项目跨端联调 | 🟡 待验证 | 当前仓库测试不连接外部 Agora 网络 |

服务端 `{hi}` 响应中的 `groupCallProvider: "agora"` 表示已启用群组通话。群组邀请仍使用兼容的 `webrtc` 消息头，服务端会写入可信的 `call-provider: "agora"`：

```json
{
  "pub": {
    "id": "call-create-1",
    "topic": "grpYiqEXb4QY6s",
    "head": {
      "webrtc": "started",
      "mime": "application/x-im-call"
    },
    "content": {
      "type": "video",
      "title": "周会"
    }
  }
}
```

只有群组 `W` 权限成员可以创建通话。收到邀请后，具有 `J+R` 权限的成员发送 `join`：

```json
{
  "note": {
    "id": "call-join-1",
    "topic": "grpYiqEXb4QY6s",
    "what": "call",
    "seq": 120,
    "event": "join"
  }
}
```

服务端仅向请求 Session 返回加入凭证，Token 不会广播给其他成员：

```json
{
  "info": {
    "topic": "grpYiqEXb4QY6s",
    "from": "usr2il9suCbuko",
    "what": "call",
    "seq": 120,
    "event": "join",
    "payload": {
      "provider": "agora",
      "app_id": "Agora App ID",
      "channel": "im_4b2f...",
      "uid": 1938475621,
      "user_id": "usr2il9suCbuko",
      "token": "007...",
      "expires_at": 1785302400,
      "role": "publisher",
      "call_seq": 120
    }
  }
}
```

客户端必须用响应中的同一组 `app_id + channel + uid + token` 加入 Agora 频道：

- `publisher` 对应群组可写成员，可发布音频、视频和数据流。
- `subscriber` 对应只读成员，只获取加入频道权限。生产项目应在 Agora Console 开启 Co-host Token Authentication，确保发布权限在 Agora 网络侧强制生效。
- Agora SDK 在 Token 即将过期时会触发 `onTokenPrivilegeWillExpire`。客户端发送 `event: "refresh"` 获取新 Token，然后调用 SDK 的 `renewToken`。
- 客户端离开时发送 `event: "leave"`；最后一个在线参与者离开会结束当前群组通话。
- 发起人或群管理员可以发送 `event: "hang-up"` 结束整个群组通话。
- 服务端为每个 Session 分配独立数字 UID，同一账号可以通过多个设备同时加入。

续期和离开示例：

```json
{ "note": { "id": "call-refresh-1", "topic": "grpYiqEXb4QY6s", "what": "call", "seq": 120, "event": "refresh" } }
{ "note": { "id": "call-leave-1", "topic": "grpYiqEXb4QY6s", "what": "call", "seq": 120, "event": "leave" } }
{ "note": { "id": "call-end-1", "topic": "grpYiqEXb4QY6s", "what": "call", "seq": 120, "event": "hang-up" } }
```

服务端配置位于 `webrtc.agora`。生产环境建议不在配置文件保存证书，而是设置 `AGORA_APP_ID` 和 `AGORA_APP_CERTIFICATE`。`token_ttl` 范围为 60–86400 秒，默认 3600 秒：

```json
{
  "webrtc": {
    "enabled": true,
    "call_establishment_timeout": 30,
    "agora": {
      "enabled": true,
      "app_id": "",
      "app_certificate": "",
      "token_ttl": 3600,
      "channel_prefix": "im",
      "max_participants": 128
    }
  }
}
```

`app_certificate` 是服务端密钥，不得写入客户端、下发协议或业务日志。群组通话不使用业务服务端 ICE 配置；`ice_servers` 只服务于兼容的 P2P WebRTC 通话。

---

## 消息报文详细规范 (Messages Spec)

### 客户端发往服务端报文 (C2S)

```json
// {hi} 握手
{ "hi": { "id": "100", "user_agent": "MyApp/1.0", "ver": "0.29" } }

// {login} 登录
{ "login": { "id": "101", "scheme": "basic", "secret": "dXNlcm5hbWU6cGFzc3dvcmQ=" } }

// {sub} 订阅主题
{ "sub": { "id": "102", "topic": "grpYiqEXb4QY6s" } }

// {pub} 发布消息；cid 是同一发送者在该 Topic 内持久化的幂等键，最长 64 字节
{ "pub": { "id": "103", "topic": "grpYiqEXb4QY6s", "cid": "device-a:00000103", "content": "Hello World" } }

// 断线后从 seq=16 开始按升序追赶；服务端将查询固定在请求时的 high 水位
{ "get": { "id": "104", "topic": "grpYiqEXb4QY6s", "what": "data", "data": { "since": 16, "limit": 100, "forward": true } } }

// {note} 已读/送达通知；提供 id 时服务端在状态持久化后返回确认
{ "note": { "id": "read-15", "topic": "grpYiqEXb4QY6s", "what": "read", "seq": 15 } }
```

协议 `0.27` 的消息字段：

- `kind`：可选客户端声明；服务端从内容重新推导并校验，只接受 `text`、`drafty`、`image`、`video`、`voice`、`audio`、`file`。
- `reply`：当前 Topic 内被回复消息的 `seq`。
- `replace`：原位编辑目标消息；原作者或管理员可操作，`seq` 不变，服务端返回 `edited`。
- `forward`：`{"topic":"源 Topic","seq":12}`；服务端检查读取权限并复制源内容及可信来源信息。
- `group`：客户端相册 ID，同一发送者同组最多连续 10 项；服务端返回按发送者命名空间化后的可信组 ID。
- `schedule`：RFC 3339 UTC 投递时间，最多提前 366 天；必须提供 `cid`，10 秒内按立即发送处理，其余在投递时才分配 `seq`。

Drafty 图片示例（视频使用 `VD`、音频/语音使用 `AU`、文件使用 `EX`）：

```json
{
  "pub": {
    "id": "media-1",
    "topic": "grpYiqEXb4QY6s",
    "cid": "device-a:media-1",
    "kind": "image",
    "content": {
      "txt": "caption",
      "fmt": [{"at": -1, "key": 0}],
      "ent": [{
        "tp": "IM",
        "data": {
          "mime": "image/jpeg",
          "ref": "/v0/file/s/AbCdEf12345.jpg",
          "width": 1280,
          "height": 720,
          "size": 245760
        }
      }]
    }
  },
  "extra": {"attachments": ["/v0/file/s/AbCdEf12345.jpg"]}
}
```

服务端会校验 Drafty span、实体、MIME、尺寸/时长和附件引用；`extra.attachments` 不能声明内容中未引用的文件。语音消息在 `AU.data.voice` 中设置 `true`。

消息交互示例：

```json
// 回复
{ "pub": { "id": "reply-1", "topic": "grpYiqEXb4QY6s", "reply": 15, "content": "收到" } }

// 编辑 seq=15
{ "pub": { "id": "edit-15", "topic": "grpYiqEXb4QY6s", "replace": 15, "content": "修正后的文本" } }

// 跨 Topic 转发
{ "pub": { "id": "fwd-1", "topic": "grpYiqEXb4QY6s", "forward": {"topic": "grpSource", "seq": 9}, "content": null } }

// 定时发送
{ "pub": { "id": "sched-1", "topic": "grpYiqEXb4QY6s", "cid": "device-a:sched-1", "schedule": "2026-07-30T08:00:00Z", "content": "明早发送" } }

// 添加/移除反应
{ "note": { "id": "react-1", "topic": "grpYiqEXb4QY6s", "what": "react", "seq": 15, "reaction": "👍" } }
{ "note": { "id": "react-2", "topic": "grpYiqEXb4QY6s", "what": "react", "seq": 15, "reaction": "👍", "remove": true } }

// 置顶/取消置顶
{ "note": { "id": "pin-1", "topic": "grpYiqEXb4QY6s", "what": "pin", "seq": 15 } }
{ "note": { "id": "pin-2", "topic": "grpYiqEXb4QY6s", "what": "pin", "seq": 15, "remove": true } }

// 为自己软删除；hard=true 为双方/全体删除。普通用户只能 hard-delete 自己发送的消息。
{ "del": { "id": "del-15", "topic": "grpYiqEXb4QY6s", "what": "msg", "delseq": [{"low": 15}], "hard": true } }

// 取消尚未投递的定时消息
{ "del": { "id": "cancel-1", "topic": "grpYiqEXb4QY6s", "what": "sched", "scheduled": "AbCdEf12345" } }

// 拉取 ims 之后的编辑和反应聚合状态；使用响应 params.modified 继续分页
{ "get": { "id": "modified-1", "topic": "grpYiqEXb4QY6s", "what": "data", "data": {"ims": "2026-07-29T08:00:00Z", "limit": 100} } }
```

### 服务端发往客户端报文 (S2C)

```json
// {ctrl} 发布确认
{ "ctrl": { "id": "103", "code": 202, "text": "accepted", "params": { "seq": 15, "cid": "device-a:00000103" } } }

// {data} 聊天消息广播
{ "data": { "topic": "grpYiqEXb4QY6s", "from": "usr2il9suCbuko", "cid": "device-a:00000103", "seq": 15, "kind": "text", "content": "Hello World" } }

// 一页追赶完成。下一页使用 since=cursor+1，直到 hasMore=false
{ "ctrl": { "id": "104", "code": 208, "text": "delivered", "params": { "what": "data", "count": 100, "low": 16, "high": 240, "cursor": 115, "hasMore": true } } }

// 相同 cid 重试不会再次落库、广播、增加未读或触发推送
{ "ctrl": { "id": "105", "code": 208, "text": "delivered", "params": { "seq": 15, "cid": "device-a:00000103", "duplicate": true } } }

// 已读/送达状态已持久化；重复状态返回 208 及当前游标
{ "ctrl": { "id": "read-15", "code": 200, "text": "ok", "params": { "what": "read", "seq": 15 } } }

// {pres} 在线状态变更通知
{ "pres": { "topic": "me", "src": "usr2il9suCbuko", "what": "on" } }
```
