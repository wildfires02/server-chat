# 接口调用示例

> 文档信息
>
> - 类型：示例
> - 权威协议说明：[服务端协议参考](API.md)

本文档提供常用连接、认证、消息、群组和文件接口的请求示例。字段语义、
权限规则和完整报文定义以[服务端协议参考](API.md)为准。

## 1. 通讯协议与接入点 (Endpoints & Headers)

### 1.1 传输通道与基础 URL
- **WebSocket 主通道**：`ws://<host>:<port>/v0/channels?apikey=<API_KEY>`
- **长轮询备用通道 (Long Polling)**：`http://<host>:<port>/v0/channels/lp`
- **大文件上传服务**：`POST http://<host>:<port>/v0/file/u/`
- **大文件下载/查看**：`GET http://<host>:<port>/v0/file/s/<filename>`
- **gRPC 接口**：`<host>:16060`

### 1.2 HTTP 请求头规范
在进行 HTTP/REST 请求（如文件上传下载、长轮询）时，支持以下请求头：
- `X-IM-APIKey`: 客户端 App 校验密钥（例如 `AQAAAAABAAAoeOI7tA3HsYvdzDhYhZJy`）
- `X-IM-Auth`: 身份验证令牌，格式为 `Token <auth_token>` 或 `Basic <base64_secret>`

## 2. 客户端握手与身份认证 (Handshake & Auth)

所有 WebSocket 通信建立后，客户端**必须**首先发送 `{hi}` 报文进行协议握手，成功后再发送 `{login}` 或 `{acc}`。

### 2.1 协议握手 `{hi}`
- **客户端请求**：
  ```json
  {
    "hi": {
      "id": "1",
      "ver": "0.25",
      "useragent": "IMWeb/1.0",
      "lang": "en"
    }
  }
  ```
- **服务端响应**：
  ```json
  {
    "ctrl": {
      "id": "1",
      "code": 200,
      "text": "ok",
      "params": {
        "build": "20260724T10:00:00Z",
        "ver": "0.25"
      }
    }
  }
  ```

### 2.2 基础账号密码登录 `{login scheme: "basic"}`
- **客户端请求** (secret 字段为 `base64(username:password)`):
  ```json
  {
    "login": {
      "id": "2",
      "scheme": "basic",
      "secret": "YWxpY2U6YWxpY2UxMjM="
    }
  }
  ```
- **服务端响应**：
  ```json
  {
    "ctrl": {
      "id": "2",
      "code": 200,
      "text": "ok",
      "params": {
        "id": "usrExaGKbPeJ6k",
        "token": "ExaGKbPeJ6muT3VqFAABAAEArA8A5in3voUHveeMnJCS6p0kDExSXssboT9Z0bPxDGU=",
        "expires": "2026-08-07T11:00:00Z"
      }
    }
  }
  ```

### 2.3 Token 令牌快速登录 `{login scheme: "token"}`
- **客户端请求**：
  ```json
  {
    "login": {
      "id": "3",
      "scheme": "token",
      "secret": "ExaGKbPeJ6muT3VqFAABAAEArA8A..."
    }
  }
  ```
- **服务端响应**：同 2.2，成功返回 Code 200 及对应 User ID。

### 2.4 新用户注册账号 `{acc}`
- **客户端请求**：
  ```json
  {
    "acc": {
      "id": "4",
      "user": "new",
      "scheme": "basic",
      "secret": "bmV3dXNlcjpwYXNzMTIz",
      "login": true,
      "public": { "fn": "新用户显示名" }
    }
  }
  ```
- **服务端响应**：Code 200，并返回生成的新 `id` (如 `usrAbCDe...`) 与 `token`。

## 3. 即时通讯与消息收发 (Messaging & Data)

### 3.1 订阅个人中心主题 `{sub topic: "me"}`
用于拉取并实时监听当前用户的联系人列表、群组列表以及会话消息未读计数。
- **客户端请求**：
  ```json
  {
    "sub": {
      "id": "5",
      "topic": "me",
      "get": { "sub": {} }
    }
  }
  ```
- **服务端响应数据包**：返回包含所有订阅列表的 `{meta topic: "me"}`。

### 3.2 订阅聊天主题并拉取历史消息 `{sub}`
在进入特定单聊 (P2P)、群组 (Group) 或频道 (Channel) 时发送：
- **客户端请求**：
  ```json
  {
    "sub": {
      "id": "6",
      "topic": "usrExaGKbPeJ6k",
      "get": {
        "data": { "limit": 24 }
      }
    }
  }
  ```
- **服务端响应消息报文 `{data}`**：
  ```json
  {
    "data": {
      "topic": "usrExaGKbPeJ6k",
      "from": "usr4LJ9Qf0lWMQ",
      "timestamp": "2026-07-24T11:34:00Z",
      "seq": 105,
      "content": "你好，这是一条测试消息！"
    }
  }
  ```

### 3.3 发送/发布消息 `{pub}`
- **客户端请求**：
  ```json
  {
    "pub": {
      "id": "7",
      "topic": "usrExaGKbPeJ6k",
      "noecho": false,
      "content": "你好，这是一条测试消息！"
    }
  }
  ```
- **服务端控制响应**：
  ```json
  {
    "ctrl": {
      "id": "7",
      "code": 200,
      "text": "accepted",
      "params": { "seq": 106 }
    }
  }
  ```
- **实时广播**：服务端同时向该 Topic 下的所有在线订阅者推送 `{data}` 包。

## 4. 群组与频道管理 (Groups & Channels)

### 4.1 创建新群组 `{sub topic: "new"}`
- **客户端请求**：
  ```json
  {
    "sub": {
      "id": "8",
      "topic": "new",
      "set": {
        "public": {
          "fn": "Golang 技术交流群",
          "note": "欢迎加入 Golang 开发者讨论"
        }
      }
    }
  }
  ```
- **服务端响应**：
  ```json
  {
    "ctrl": {
      "id": "8",
      "code": 200,
      "text": "created",
      "topic": "grpHkA5yu_Decc",
      "params": { "seq": 0 }
    }
  }
  ```

### 4.2 创建新广播频道 `{sub topic: "nch"}`
频道（Channel）允许创建者/管理员发布广播，普通订阅者默认具备只读权限（无法发贴）：
- **客户端请求**：
  ```json
  {
    "sub": {
      "id": "9",
      "topic": "nch",
      "set": {
        "public": {
          "fn": "官方公告频道",
          "note": "系统发布重要更新与通知"
        }
      }
    }
  }
  ```
- **服务端响应**：
  ```json
  {
    "ctrl": {
      "id": "9",
      "code": 200,
      "text": "created",
      "topic": "nchDartoSIcAMc",
      "params": { "seq": 0 }
    }
  }
  ```

### 4.3 退出 / 删除主题 `{leave}`
- **客户端请求** (设置 `unsub: true` 代表彻底取消订阅/退群)：
  ```json
  {
    "leave": {
      "id": "10",
      "topic": "grpHkA5yu_Decc",
      "unsub": true
    }
  }
  ```
- **服务端响应**：Code 200 代表已退群。

### 4.4 踢出群成员 / 取消成员订阅 `{del what: "sub"}`
群组创建者/管理员可以通过发送 `{del}` 报文踢出指定的群成员：
- **客户端请求**：
  ```json
  {
    "del": {
      "id": "11",
      "topic": "grpHkA5yu_Decc",
      "what": "sub",
      "user": "usr3eHgBLr7ex4"
    }
  }
  ```
- **服务端控制响应**：
  ```json
  {
    "ctrl": {
      "id": "11",
      "topic": "grpHkA5yu_Decc",
      "code": 200,
      "text": "ok"
    }
  }
  ```
- **实时广播通知**：服务端会向被踢出成员发送 `{pres what: "del"}` 通知，该成员在 `me` 主题的联系人列表中该群权限将置为 `"N"`。

### 4.5 拉取群成员列表与主题元数据 `{get}`
客户端必须在 `{get}` 报文中显式指定 `"what"` 参数（如 `"sub"`、`"desc"`、`"data"` 等），以获取指定主题的成员列表、属性或历史消息：
- **客户端请求**：
  ```json
  {
    "get": {
      "id": "12",
      "topic": "grpHkA5yu_Decc",
      "what": "sub desc",
      "sub": {},
      "desc": {}
    }
  }
  ```
- **服务端响应**：
  服务端向客户端推送 `{meta topic: "grpHkA5yu_Decc", sub: [...], desc: {...}}`，其中 `sub` 包含群内所有成员信息及其 Access Control (ACS) 模式。

### 4.6 状态码 304 规范与二次订阅处理
- **`code: 304` (Not Modified / Already Subscribed)**：当客户端对当前 Session 已经订阅的主题再次调用 `{sub}` 时，服务端返回 Code 304。
- **客户端处理**：SDK 会将 200-399 范围内的响应代码统一判定为成功操作。

### 4.7 访问控制与成员权限判定 (AccessMode)
IM 服务端使用字符编码管理用户访问权限：
- `J` (Join): 允许加入主题
- `R` (Read): 允许读取消息与订阅
- `W` (Write): 允许发送消息
- `P` (Presence): 实时在线状态与打字提示
- `A` (Approve): 管理员审批权限
- `O` (Owner): 主题所有者/群主
- `N` (None): 无访问权限（已被踢出或未加入）

**前端展示规则**：侧边栏会话列表仅展示 `acs.mode` 包含 `R` / `J` / `O` 且不为 `"N"` 的生效主题；已被踢出 (`"N"`) 或取消订阅的主题会自动过滤。

### 4.8 客户端活跃窗口持久化 (Topic Persistence)
- **机制**：进入群聊或单聊时，前端会在 `localStorage` 中记录 `im_active_topic_name`。
- **效果**：刷新网页完成自动登录后，客户端会自动寻找并恢复对应的激活窗口，并重新订阅 WebSocket 消息队列，保持聊天上下文不中断。

## 5. 大文件/附件传输接口 (File API)

### 5.1 上传文件 `POST /v0/file/u/`
- **请求方式**：`POST`
- **Content-Type**：`multipart/form-data`
- **Headers**：
  - `X-IM-APIKey: AQAAAAABAAAoeOI7tA3HsYvdzDhYhZJy`
  - `X-IM-Auth: Token <auth_token>`
- **Form Data**：`file: <binary_blob>`
- **响应示例**：
  ```json
  {
    "ctrl": {
      "code": 200,
      "text": "ok",
      "params": {
        "url": "/v0/file/s/img_92837419.jpg"
      }
    }
  }
  ```

### 5.2 下载/查看文件 `GET /v0/file/s/<filename>`
- **请求方式**：`GET`
- **说明**：支持浏览器 `<img src="/v0/file/s/..." />` 或通过带有 `X-IM-Auth` 头的 GET 请求直接获取文件字节流。
