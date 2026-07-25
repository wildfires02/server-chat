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

通过订阅 `fnd` 主题发送 `set.tags` 或 `get` 查询，可以在系统中搜索其他用户或公开群组。

### 点对点 P2P 主题

双人私聊会话通道。主题名称直接对应对方用户的 ID（如 `usr2il9suCbuko`）。

### 群组主题 (Group Topics)

多人聊天会话通道（前缀 `grp`，如 `grpYiqEXb4QY6s`）。支持将其配置为只读频道 (Channel) 模式。

---

## 消息报文详细规范 (Messages Spec)

### 客户端发往服务端报文 (C2S)

```json
// {hi} 握手
{ "hi": { "id": "100", "user_agent": "MyApp/1.0", "ver": "0.22" } }

// {login} 登录
{ "login": { "id": "101", "scheme": "basic", "secret": "dXNlcm5hbWU6cGFzc3dvcmQ=" } }

// {sub} 订阅主题
{ "sub": { "id": "102", "topic": "grpYiqEXb4QY6s" } }

// {pub} 发布消息
{ "pub": { "id": "103", "topic": "grpYiqEXb4QY6s", "content": "Hello World" } }

// {note} 状态/已读/正在输入通知
{ "note": { "topic": "grpYiqEXb4QY6s", "what": "read", "seq_id": 15 } }
```

### 服务端发往客户端报文 (S2C)

```json
// {ctrl} 控制响应
{ "ctrl": { "id": "103", "code": 200, "text": "ok", "params": { "seq": 15 } } }

// {data} 聊天消息广播
{ "data": { "topic": "grpYiqEXb4QY6s", "from": "usr2il9suCbuko", "seq": 15, "content": "Hello World" } }

// {pres} 在线状态变更通知
{ "pres": { "topic": "me", "src": "usr2il9suCbuko", "what": "on" } }
```
