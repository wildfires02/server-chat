# REST 或 JSON-RPC 身份认证器

本认证器允许使用独立的外部进程作为“单一事实来源”（Source of Truth）来对 IM 用户进行身份验证及账号创建。例如，如果企业账号由 LDAP 统一管理，则可以通过该服务使用同一个 LDAP 服务来处理 IM 的用户认证。

该认证器通过 HTTP(S) POST 方式调用指定的认证服务。在 [rest-auth](../../../rest-auth/) 目录中提供了一个参考服务端骨架实现。请求可以由单一 Endpoint 处理，也可以由针对每个请求的独立 Endpoint 处理。

请求和响应的载荷格式均为 JSON。部分请求或响应字段取决于上下文，可以省略。

---

## 目录

- [配置说明](#配置说明)
- [请求格式 (Request)](#请求格式-request)
- [响应格式 (Response)](#响应格式-response)
- [可识别的错误响应 (Recognized error responses)](#可识别的错误响应-recognized-error-responses)
- [服务端必须实现的 API 端点](#服务端必须实现的-api-端点)
	- [`add` 添加新认证记录](#add-添加新认证记录)
	- [`auth` 认证请求](#auth-认证请求)
	- [`checkunique` 检查认证记录唯一性](#checkunique-检查认证记录唯一性)
	- [`del` 请求删除认证记录](#del-请求删除认证记录)
	- [`gen` 生成认证密钥](#gen-生成认证密钥)
	- [`link` 请求关联新账号 ID 到认证记录](#link-请求关联新账号-id-到认证记录)
	- [`upd` 更新认证记录](#upd-更新认证记录)
	- [`rtagns` 获取受限标签命名空间列表](#rtagns-获取受限标签命名空间列表)

---

## 配置说明

在 [im.conf](../../im.conf) 的 `auth_config` 配置项中添加以下内容：

```js
...
"auth_config": {
  ...
  "rest": {
    // ServerUrl 为要调用的认证服务器的 URL。URL 必须是绝对路径：
    // 必须包含 scheme（如 http 或 https）以及主机名。
    "server_url": "http://127.0.0.1:5000/",
    // 是否允许认证服务器创建新账号。
    "allow_new_accounts": true,
    // 是否使用独立 Endpoint，即发送请求时在 serverUrl 路径后追加端点名称：
    // 例如：http://127.0.0.1:5000/add
    "use_separate_endpoints": true
  },
  ...
},
```

如果你想使用自定义的认证器**替代**默认的 `basic`（用户名密码）认证，可以配置逻辑重命名并在原名称处禁用 `rest`：

```js
...
"auth_config": {
  "logical_names": ["basic:rest", "rest:"],
  "rest": { ... },
  ...
},
...
```

---

## 请求格式 (Request)

```js
{
  "endpoint": "auth",           // 字符串，如下所述的端点之一，可选。
  "secret": "Ym9iOmJvYjEyMw==", // 客户端提供的认证密钥，Base64 编码字节切片，可选。
  "addr": "2001:0db8:85a3:0000:0000:8a2e:0370:7334", // 字符串，发起请求客户端的 IPv4 或 IPv6 地址，可选。
  "rec": {                      // 认证记录，可选。
    "uid": "LELEQHDWbgY",       // 用户 ID，int64 的 Base64 编码字符串
    "authlvl": "auth",          // 认证级别
    "lifetime": "10000s",       // 此记录的有效生命周期（秒数或 time.Duration 格式字符串）
    "features": 1,              // 特性位图（整数或字符表示："validated" (V) 或 "no login" (L)）
    "tags": ["email:alice@example.com"], // 与此认证记录关联的标签。
    "state": "ok"               // 可选的账号状态。
  }
}
```

---

## 响应格式 (Response)

```js
{
  "err": "internal",                // 字符串，发生错误时的错误消息。
  "rec": {                          // 认证记录。
    ...                             // 格式与 `request.rec` 相同
  },
  "byteval": "Ym9iOmJvYjEyMw==",    // 字节数组，可选
  "ts": "2018-12-04T15:17:02.627Z", // 时间戳，可选
  "boolval": true,                  // 布尔值，可选
  "strarr": ["abc", "def"],         // 字符串数组，可选
  "newacc": {                       // 用于创建新账号的数据。
    // 默认访问模式
    "auth": "JRWPS",
    "anon": "N",
    "public": {...},                // 用户的公开数据 (Public Data)
    "trusted": {...},               // 用户的可信数据 (Trusted Data)
    "private": {...}                // 用户的私有数据 (Private Data)
  }
}
```

---

## 可识别的错误响应

错误以 JSON 格式返回：

```json
{ "err": "error-message" }
```

受支持的错误消息列表（参见 [types.go](../../store/types/types.go#L24)）：

* `"internal"`: 数据库故障或其它内部兜底捕获错误。
* `"malformed"`: 请求无法解析或格式错误。
* `"failed"`: 认证失败（用户名或密码错误等）。
* `"duplicate value"`: 凭据重复，即尝试使用非唯一用户名创建记录。
* `"unsupported"`: 不支持该操作。
* `"expired"`: 密钥已过期。
* `"policy"`: 违背策略（例如密码太弱）。
* `"credentials"`: 凭据（如邮箱或验证码）必须先完成校验。
* `"not found"`: 对象未找到。
* `"denied"`: 操作不允许/被拒绝。

---

## 服务端必须实现的 API 端点

### `add` 添加新认证记录

该端点请求服务器添加新的认证记录。通常在创建账号时调用。
如果账号由外部服务完全管理，则此端点一般不使用，应返回错误 `"unsupported"`。

#### 请求示例
```json
{
  "endpoint": "add",
  "secret": "Ym9iOmJvYjEyMw==",
  "addr": "111.22.33.44",
  "rec": {
    "uid": "LELEQHDWbgY",
    "lifetime": "10000s",
    "features": 2,
    "tags": ["email:alice@example.com"]
  }
}
```

#### 响应示例（rec 的值可能会根据服务端逻辑发生变化）
```json
{
  "rec": {
    "uid": "LELEQHDWbgY",
    "authlvl": "auth",
    "lifetime": "5000s",
    "features": 1,
    "tags": ["email:alice@example.com", "uname:alice"]
  }
}
```

---

### `auth` 认证请求

请求对用户进行身份校验。客户端（IM）提供凭据密钥，认证服务器响应用户记录。
如果是首次登录且由认证服务器管理账号，服务器可返回 `newacc` 对象，客户端（IM）将使用该对象在本地创建账号。服务器还可以选择性地在 `byteval` 中返回 Challenge 数据。

#### 请求示例
```json
{
  "endpoint": "auth",
  "secret": "Ym9iOmJvYjEyMw==",
  "addr": "111.22.33.44"
}
```

#### 账号已存在时的响应示例（包含可选的 Challenge 数据）
```json
{
  "rec": {
    "uid": "LELEQHDWbgY",
    "authlvl": "auth",
    "state": "ok"
  },
  "byteval": "9X6m3tWeBEMlDxlcFAABAAEAbVs"
}
```

#### 需由 IM 创建新账号时的响应示例
```js
{
  "rec": {
    "state": "suspended", // 或 "ok"
    "authlvl": "auth",
    "lifetime": "5000s",
    "features": 1,
    "tags": ["email:alice@example.com", "uname:alice"]
  },
  "newacc": {
    "auth": "JRWPS",
    "anon": "N",
    "public": {/* 用户的公开数据 */},
    "trusted": {/* 用户的可信数据 */},
    "private": {/* 用户的私有数据 */}
  }
}
```

---

### `checkunique` 检查认证记录唯一性

请求用于创建账号时。如果账号由远程服务器管理，服务器应响应错误 `"unsupported"`。

#### 请求示例
```json
{
  "endpoint": "checkunique",
  "secret": "Ym9iOmJvYjEyMw==",
  "addr": "111.22.33.44"
}
```

#### 响应示例
```json
{
  "boolval": true
}
```

---

### `del` 请求删除认证记录

如果账号由远程服务器管理，服务器应响应错误 `"unsupported"`。

#### 请求示例
```json
{
  "endpoint": "del",
  "rec": {
    "uid": "LELEQHDWbgY"
  }
}
```

#### 响应示例
```json
{}
```

---

### `gen` 生成认证密钥

如果账号由远程服务器管理，服务器应响应错误 `"unsupported"`。

#### 请求示例
```json
{
  "endpoint": "gen",
  "rec": {
    "uid": "LELEQHDWbgY",
    "authlvl": "auth"
  }
}
```

#### 响应示例
```json
{
  "byteval": "9X6m3tWeBEMlDxlcFAABAAEAbVs",
  "ts": "2018-12-04T15:17:02.627Z"
}
```

---

### `link` 请求关联新账号 ID 到认证记录

当远程服务器指示 IM 创建新账号后，此端点用于将生成的 IM 用户 ID (UID) 与远程服务器的认证记录进行绑定。关联成功后，服务器应响应非空的 JSON。

#### 请求示例
```json
{
  "endpoint": "link",
  "secret": "Ym9iOmJvYjEyMw==",
  "rec": {
    "uid": "LELEQHDWbgY",
    "authlvl": "auth"
  }
}
```

#### 响应示例
```json
{}
```

---

### `upd` 更新认证记录

如果账号由远程服务器管理，服务器应响应错误 `"unsupported"`。

#### 请求示例
```json
{
  "endpoint": "upd",
  "secret": "Ym9iOmJvYjEyMw==",
  "addr": "111.22.33.44",
  "rec": {
    "uid": "LELEQHDWbgY",
    "authlvl": "auth"
  }
}
```

#### 响应示例
```json
{}
```

---

### `rtagns` 获取受限标签命名空间列表

服务器可以限制某些标签命名空间（标签前缀），即不允许用户自行修改。
这些标签也用于 IM 的发现机制（例如搜索用户、联系人同步）。

服务器还可以选择性地提供正则表达式，以便在将搜索 Token 重写为带前缀标签之前进行校验。
例如，如果服务器仅允许 3-8 位 ASCII 字母和数字的登录名，正则表达式可以为 `^[a-z0-9_]{3,8}$`，Base64 编码后为 `XlthLXowLTlfXXszLDh9JA==`。

#### 请求示例
```json
{
  "endpoint": "rtagns"
}
```

#### 响应示例
```json
{
  "strarr": ["basic", "email", "tel"],
  "byteval": "XlthLXowLTlfXXszLDh9JA=="
}
```
