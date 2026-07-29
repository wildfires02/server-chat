# IM REST 外部认证服务器示例

本模块是基于 Go 语言实现的 IM 外部 [REST 认证适配服务](../../server/auth/rest/)。

当企业的整体架构中已有独立的用户中心或现成数据库，希望将 IM 融入现有系统时，可以通过配置 IM 服务端将登录校验委派给本 REST 认证服务。

---

## 目录

- [工作原理与架构](#工作原理与架构)
- [编译与运行](#编译与运行)
- [在 IM 服务端中的配置](#在-im-服务端中的配置)
- [接口报文规范](#接口报文规范)
  - [1. 用户认证与登录 (POST /auth)](#1-用户认证与登录-post-auth)
  - [2. 账号绑定关联 (POST /link)](#2-账号绑定关联-post-link)
  - [3. 标签命名空间查询 (POST /rtagns)](#3-标签命名空间查询-post-rtagns)
  - [4. 其他扩展接口](#4-其他扩展接口)
- [测试数据库格式 (dummy_data.json)](#测试数据库格式-dummy_datajson)

---

## 工作原理与架构

1. **认证委托**：客户端通过 `basic` 认证方式发送 `username:password` 登录请求到 IM 主服务端。
2. **HTTP 转发**：IM 服务端将 Base64 编码后的凭据封装为 JSON POST 请求发往本 REST 认证服务。
3. **校验与响应**：
   - 如果账号密码正确且该用户已存在 IM 用户 ID（`uid`），REST 认证服务返回其账户属性与权限。
   - 如果账号密码正确但属于**首次登录**，REST 服务将返回 `newacc` 配置，要求 IM 自动在系统种为其初始化对应账号。
   - 账号首次创建成功后，IM 会调用 `POST /link` 接口，将生成的 `uid` 反向绑定并持久化回外部数据库。

---

## 编译与运行

确保本地已安装 Go (1.20+) 环境：

### 1. 直接通过源码运行

```bash
go run ./cmd/rest-auth \
  -port=8080 \
  -data=./cmd/rest-auth/dummy_data.json
```

### 2. 编译为二进制文件运行

```bash
# 编译可执行文件
go build -o bin/rest-auth ./cmd/rest-auth

# 启动服务
./bin/rest-auth \
  -port=8080 \
  -data=./cmd/rest-auth/dummy_data.json
```

### 命令行参数说明

| 参数标志 | 类型 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `-port` | int | `8080` | HTTP Web 服务的监听端口 |
| `-data` | string | `dummy_data.json` | 模拟用户数据库 JSON 文件的存放路径 |

---

## 在 IM 服务端中的配置

若要让 IM 主服务端启用本 REST 认证服务，需修改服务端配置文件 `im.yaml` 中的 `auth_config` 配置节：

```yaml
auth_config:
  logical_names:
    - basic
    - token
  rest:
    server_url: http://localhost:8080
```

---

## 接口报文规范

所有请求与响应数据格式均为 JSON，`Content-Type: application/json`。

### 1. 用户认证与登录 (`POST /auth`)

#### 请求报文
```json
{
  "secret": "YWxpY2U6YWxpY2UxMjM=" // Base64 编码的 "username:password" 字符串
}
```

#### 响应报文：已有账号关联登录
```json
{
  "rec": {
    "uid": "usrAbc123Def",
    "authlvl": "auth",
    "features": "V"
  }
}
```

#### 响应报文：首次登录创建新账号 (`newacc`)
```json
{
  "rec": {
    "authlvl": "auth",
    "tags": ["email:alice@example.com"],
    "features": "V"
  },
  "newacc": {
    "auth": "JRWPA",
    "anon": "N",
    "public": {
      "fn": "Alice Johnson"
    },
    "private": "email:alice@example.com"
  }
}
```

#### 错误响应示例
```json
{
  "err": "failed"     // 密码错误
}
// 或
{
  "err": "not found"  // 用户不存在
}
```

---

### 2. 账号绑定关联 (`POST /link`)

当首登用户在 IM 服务端成功建号后，IM 会调用此接口将生成的 `uid` 保存到外部系统。

#### 请求报文
```json
{
  "secret": "YWxpY2U6YWxpY2UxMjM=",
  "rec": {
    "uid": "usrAbc123Def"
  }
}
```

#### 响应报文
```json
{}  // 空对象表示绑定成功
```

---

### 3. 标签命名空间查询 (`POST /rtagns`)

告知 IM 服务端本 REST 适配器支持的标签命名空间及校验正则表达式。

#### 响应报文
```json
{
  "strarr": ["rest", "email"],
  "byteval": "XlthLXowLTlfXXszLDh9JA==" // Base64 编码的正则表达式 "^[a-z0-9_]{3,8}$"
}
```

---

### 4. 其他扩展接口

包含 `POST /add`、`POST /checkunique`、`POST /del`、`POST /gen`、`POST /upd` 等，默认均响应：
```json
{
  "err": "unsupported"
}
```

---

## 测试数据库格式 (`dummy_data.json`)

`dummy_data.json` 保存了预设测试账号的数据结构：

```json
{
  "alice": {
    "password": "alice123",
    "authlvl": "auth",
    "features": "V",
    "auth": "JRWPA",
    "anon": "N",
    "tags": ["email:alice@example.com"],
    "public": {
      "fn": "Alice Johnson"
    },
    "private": "email:alice@example.com"
  }
}
```
