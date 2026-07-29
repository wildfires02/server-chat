# IM 命令行客户端

`im-cli` 是基于 Go 语言实现的交互式及批处理命令行客户端。它使用
[gRPC API](../../api/pbx/) 与服务端通信，支持实时订阅、状态变更和管理指令。

---

## 目录

- [编译与安装](#编译与安装)
- [启动参数与配置](#启动参数与配置)
- [核心概念与变量机制](#核心概念与变量机制)
- [控制命令与语法](#控制命令与语法)
  - [1. 本地控制指令](#1-本地控制指令)
  - [2. 基础 gRPC 指令](#2-基础-grpc-指令)
  - [3. 高阶管理宏命令 (Macros)](#3-高阶管理宏命令-macros)
- [自动化批处理与示例](#自动化批处理与示例)

---

## 编译与安装

确保本地已安装 Go (1.20+) 环境：

```bash
# 编译可执行文件到当前目录
go build -o bin/im-cli ./cmd/im-cli

# 或安装到 GOPATH/bin
go install ./cmd/im-cli
```

---

## 启动参数与配置

命令行启动示例：

```bash
./bin/im-cli -host=localhost:16060 -login-basic=alice:alice123 -verbose
```

### 常用参数标志

| 参数标志 | 类型 | 默认值 | 作用与说明 |
| :--- | :--- | :--- | :--- |
| `-host` | string | `localhost:16060` | IM gRPC 服务端连接地址 |
| `-web-host` | string | `localhost:6060` | IM Web 服务端连接地址（用于附件及文件传输） |
| `-ssl` | bool | `false` | 是否开启 SSL/TLS 加密连接 |
| `-ssl-host` | string | `""` | TLS 握手自定义 SNI 域名（适用于本地测试映射） |
| `-login-basic` | string | `""` | 基础账号密码登录，格式：`username:password` |
| `-login-token` | string | `""` | 使用 Token 密钥身份验证登录 |
| `-login-cookie` | bool | `false` | 使用保存的凭据 Cookie 文件（`.cli-cookie`）直接登录 |
| `-no-login` | bool | `false` | 禁用自动登录（批处理模式下默认启用） |
| `-no-cookie` | bool | `false` | 登录成功后不自动保存凭据 Cookie 文件 |
| `-cookie-file` | string | `.cli-cookie` | 指定凭据 Cookie 文件保存/读取路径 |
| `-verbose` | bool | `false` | 详细日志模式，在控制台打印底层收发的完整 JSON 报文 |
| `-version` | bool | `false` | 打印当前客户端版本号并退出 |

---

## 核心概念与变量机制

在交互模式或批处理脚本中，`im-cli` 提供了强大的变量与结果引用能力：

1. **结果变量语法 (`$varname`)**：
   在命令前使用 `.must $var` 或 `.await $var`，可将命令的响应结果（如 ServerCtrl、ServerMeta 等）暂存到变量 `$var` 中。
2. **变量解引用**：
   后续命令可直接使用 `$var.params[user]` 或 `$var.params[token]` 引用先前获取的字段。
3. **指令同步断言 (`.must`)**：
   如果在批处理执行中某个指令要求必须成功，使用 `.must` 开头；若服务端返回错误码（`>= 400`），客户端将立即抛出异常并终止脚本执行。

---

## 控制命令与语法

在交互控制台提示符 `tn> ` 下可输入以下三大类指令：

### 1. 本地控制指令

| 指令 | 参数说明 | 示例 | 作用 |
| :--- | :--- | :--- | :--- |
| `.must` | `$var [command]` | `.must $res login basic alice:alice123` | 执行指令并等待，若失败则立即退出 |
| `.await` | `$var [command]` | `.await $sub sub me` | 执行指令并同步等待响应结果 |
| `.use` | `<user_or_topic>` | `.use usrAbc123` | 设置后续命令默认针对的目标用户或 Topic |
| `.sleep` | `<ms>` | `.sleep 2000` | 暂停脚本执行指定的毫秒数 |
| `.log` | `<message_or_var>`| `.log $res.params[token]` | 输出字符串或变量内容到控制台 |
| `.verbose`| 无 | `.verbose` | 开启或关闭底层 JSON 报文日志显示 |
| `.exit` | 无 | `.exit` | 退出客户端（也可以使用 `.quit`） |

---

### 2. 基础 gRPC 指令

* **`hi`**：向服务端发送客户端握手问候报文。
* **`login`**：登录身份验证。
  - `login basic username:password`：使用基本账号密码登录。
  - `login token <token_string>`：使用 Token 登录。
* **`acc`**：创建账号或更改账号设置。
  - `acc --uname=bob --password=bob123 --fn="Bob Smith" --email=bob@example.com`：创建新账号。
  - `acc --user=usrAbc123 --as_root --suspend=true`：以管理员身份解冻/冻结账号。
* **`sub`**：订阅 Topic。
  - `sub me`：订阅个人主题（个人数据/会话通知）。
  - `sub grpXyz123`：订阅群组或会话主题。
* **`leave`**：取消订阅或离开 Topic。
  - `leave grpXyz123`
* **`pub`**：向当前或指定 Topic 发布消息。
  - `pub grpXyz123 "Hello, World!"`
* **`get`**：查询主题元数据或消息。
  - `get grpXyz123 --desc --sub`：获取群组的基本描述与成员列表。
  - `get grpXyz123 --data`：拉取历史聊天消息。
* **`set`**：更新主题描述或个人 Profile。
  - `set me --fn="新名字" --photo="./avatar.jpg"`
* **`del`**：删除消息、主题或账号。
  - `del --user=usrAbc123 --as_root`：管理员强制删除指定用户。
* **`note`**：发送输入状态或已读通知。
  - `note grpXyz123`

---

### 3. 高阶管理宏命令 (Macros)

宏命令为常用且繁重的管理员运维操作提供了便捷的一键式封装：

* **`useradd`**：快速创建新用户。
  ```bash
  useradd testuser test123456 --name="测试用户" --email="test@example.com"
  ```
* **`usermod`**：修改用户属性（需要 Root 权限）。
  ```bash
  usermod usrAbc123 --name="新名称" --suspend      # 冻结账号
  usermod usrAbc123 --unsuspend                  # 解冻账号
  ```
* **`passwd`**：重置指定用户密码（需要 Root 权限）。
  ```bash
  passwd usrAbc123 newPassword123
  ```
* **`chacs`**：修改用户默认访问权限 (ACS)。
  ```bash
  chacs usrAbc123 --auth=JRWPA --anon=N
  ```
* **`chcred`**：管理用户绑定的凭据（邮箱/手机）。
  ```bash
  chcred usrAbc123 --add="email:newemail@example.com"
  chcred usrAbc123 --del="email:oldemail@example.com"
  ```
* **`userdel`**：删除指定账号（需要 Root 权限）。
  ```bash
  userdel usrAbc123
  ```
* **`resolve`**：根据用户名查询对应的 ID。
  ```bash
  resolve alice
  ```
* **`thecard`**：获取指定用户的个人 Profile 卡片。
  ```bash
  thecard usrAbc123
  ```

---

## 自动化批处理与示例

可以使用 Linux 重定向或管道运行批处理脚本文件：

```bash
./bin/im-cli --no-login < cmd/im-cli/examples/sample-script.txt
```

### 示例脚本解析

以下为 `sample-script.txt` 节选：

```bash
# 1. 创建测试用户并捕获响应变量 $user
.must $user acc --uname=testuser --password=test123 --fn='Test User'

# 2. 打印返回的 Token 字段
.log $user.params[token]

# 3. 使用凭据登录
.must login --scheme=token --secret=$user.params[token]

# 4. 订阅个人主题
sub me

# 5. 暂停 2000 毫秒后退出
.sleep 2000
.exit
```
