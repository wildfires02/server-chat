# IM 自动客服与聊天机器人

本模块是基于 **Go 语言** 原生实现的 IM 自动客服/聊天机器人示例项目。

它同时展示了 IM 的 **gRPC 客户端** 与 **Plugin 插件服务器** 机制：
1. **Plugin 插件模式**：服务端通过 gRPC Plugin 接口向机器人通知账号创建等系统事件。
2. **gRPC 客户端**：机器人作为普通 IM 账号登录，订阅消息、处理会话，并在收到用户消息时随机从名言库中抽取回复。

---

## 运行方法

### 1. 启动机器人的基本命令

使用 Basic 账号密码登录：

以下命令均在仓库根目录执行：

```bash
go run ./cmd/chatbot \
  -login-basic="alice:alice123" \
  -host="localhost:16060" \
  -listen=":40051" \
  -quotes="./cmd/chatbot/quotes.txt"
```

或者使用编译后的二进制文件：

```bash
go build -o bin/chatbot ./cmd/chatbot
./bin/chatbot \
  -login-basic="alice:alice123" \
  -quotes="./cmd/chatbot/quotes.txt"
```

### 2. 命令行参数标志

| 参数标志 | 类型 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `-host` | string | `localhost:16060` | IM 服务端 gRPC 端点地址 |
| `-ssl` | bool | `false` | 连接服务端时是否启用 TLS/SSL 加密 |
| `-ssl-host` | string | `""` | TLS/SSL 自定义 ServerName 域名重写 |
| `-listen` | string | `:40051` | 机器人暴露的 Plugin API 监听地址，供 IM 服务端回调 |
| `-login-basic` | string | `""` | 使用用户名与密码登录（格式：`username:password`） |
| `-login-token` | string | `""` | 使用 Token 进行身份认证 |
| `-login-cookie` | string | `.chatbot-cookie` | 保存与读取登录 Cookie 的本地文件路径 |
| `-quotes` | string | `quotes.txt` | 名言回复素材文本文件路径（每行一条回复） |

---

## 工作机制

1. **握手与登录**：启动后向服务端发送 `{hi}` 握手与 `{login}` 认证报文，成功后订阅 `me` 个人主题。
2. **状态感知与订阅**：监听 `me` 主题推送的在线状态 Notification (`{pres}`)。当对方用户上线或发送消息时，机器人自动与其建立订阅联系。
3. **已读标记与自动回复**：收到对方发来的消息（`{data}`）后：
   - 自动向服务端发送 `{note what="read"}` 将消息标记为已读。
   - 延时 100ms 随机从 [quotes.txt](./quotes.txt) 提取一条消息自动广播回复（`{pub}`）。
