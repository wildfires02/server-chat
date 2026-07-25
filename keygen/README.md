# keygen: API Key 生成与校验工具

`keygen` 是用于为 [IM 服务端](../server/) 生成与校验客户端 API Key 的命令行实用工具。

## 参数标志说明

* `-sequence`：API Key 的递增序列号（整数）。可通过提升此数值来批量废弃之前版本发放的 Key。
* `-isroot`：目前暂未使用（保留字段），用于标识超级管理员权限的 Key。
* `-validate`：待校验的 API Key 字符串。传入此参数时执行校验逻辑。
* `-salt`：[HMAC](https://en.wikipedia.org/wiki/HMAC) 随机盐值（32 字节随机数经 Standard Base64 编码）。进行 Key 校验时为必填项；生成 Key 时为可选项（如留空，将自动密码学安全地生成随机盐值）。

---

## 作用与使用流程

API Key 用于防范自动化脚本/爬虫对 IM 接口的恶意抓取，并标识不同的客户端应用类型。

* **`API Key`（公钥）**：打入客户端 App 源码中。
* **`HMAC Salt`（私钥盐值）**：配置在服务端 `im.conf` 配置文件中校验客户端 Key。

### 1. 运行生成器

```bash
./keygen
```

命令行输出范例：

```text
API key v1 seq1 [ordinary]: AQAAAAABAACGOIyP2vh5avSff5oVvMpk
HMAC salt: TC0Jzr8f28kAspXrb4UYccJUJ63b7CSA16n1qMxxGpw=
```

### 2. 配置与部署

1. **服务端**：将 `HMAC salt` 的值复制粘贴到 IM 服务端配置文件 `im.conf` 的 `api_key_salt` 参数项中。
2. **客户端**：将 `API Key` 复制到对应的客户端应用源码中。

修改或更新 API Key 后，需重新编译对应的客户端工程。
