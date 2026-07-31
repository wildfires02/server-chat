# 双向自动翻译

服务端对 P2P 纯文本消息执行逐接收方翻译。原消息保持不变地保存，发送方看到原文；
接收方先收到同一 `seq` 的 `pending` 投影，翻译完成后收到 `completed` 投影。
历史消息也使用相同规则，并复用数据库中的翻译缓存。

## 配置

翻译配置由独立 `im-admin` 管理，翻译任务由 `im-server` 执行。两个进程必须连接
同一个数据库；`im-server` 按 `translation.refresh_interval`（默认 5 秒）读取最新
策略，所以保存配置后不需要重启聊天服务。

分别构建并启动：

```bash
go build -tags mysql -o bin/im-server ./cmd/im-server
go build -tags mysql -o bin/im-admin ./cmd/im-admin

./bin/im-admin
./bin/im-server
```

管理请求发送到 `im-admin`（开发默认 `http://127.0.0.1:6061`）。先读取
`GET /v0/admin/bootstrap`，修改响应中的
`control_plane.settings.translation`，再把完整的 `settings` 对象通过
`PUT /v0/admin/settings` 写回，并使用当前版本作为 `If-Match`。

```json
{
  "enabled": true,
  "staff_language": "zh-CN",
  "customer_language": "en",
  "keep_original": true,
  "failure_policy": "hold",
  "default_timeout_ms": 1500,
  "max_attempts": 3,
  "providers": [
    {
      "id": "azure-primary",
      "type": "azure",
      "enabled": true,
      "priority": 10,
      "region": "eastus",
      "credential_file": "/run/secrets/translation/azure",
      "timeout_ms": 1200,
      "monthly_character_limit": 1800000,
      "failure_threshold": 3,
      "open_seconds": 30
    },
    {
      "id": "google-backup",
      "type": "google",
      "enabled": true,
      "priority": 20,
      "credential_file": "/run/secrets/translation/google",
      "timeout_ms": 1500,
      "monthly_character_limit": 450000,
      "failure_threshold": 3,
      "open_seconds": 30
    },
    {
      "id": "libre-emergency",
      "type": "libretranslate",
      "enabled": true,
      "priority": 30,
      "endpoint": "https://translate.internal.example",
      "credential_file": "/run/secrets/translation/libre",
      "timeout_ms": 2500,
      "failure_threshold": 2,
      "open_seconds": 60
    }
  ],
  "routes": [
    {
      "source": "zh",
      "target": "en",
      "providers": ["azure-primary", "google-backup", "libre-emergency"]
    },
    {
      "source": "en",
      "target": "zh",
      "providers": ["google-backup", "azure-primary", "libre-emergency"]
    }
  ]
}
```

`priority` 数字越小，未匹配显式路由时越先调用。路由内的 `providers` 数组定义明确的
故障转移顺序，单条消息最多尝试 `max_attempts` 家。超时、HTTP 错误、额度耗尽或
熔断都会自动尝试下一家。`monthly_character_limit` 是单进程成本保护阈值；集群的
最终总额度仍应同时在供应商控制台设置硬限制。

`failure_policy` 有两个值：

- `hold`：翻译失败时不向接收方泄露原文，`content` 为 `null`。
- `original`：翻译全部失败时退回原文。

## 密钥

后台只保存 `credential_file`，它是只读凭据文件的绝对路径，不是密钥。实际执行
翻译的每个 `im-server` 节点都必须挂载对应文件；如果要在后台点击供应商连通性测试，
`im-admin` 也必须挂载相同路径。文件内容支持：

- Azure、Google、DeepL：直接保存 API key，或保存 `{"api_key":"..."}`。
- LibreTranslate：API key 可选；自建实例不需要 key 时省略 `credential_file`。
- AWS：保存
  `{"access_key_id":"...","secret_access_key":"...","session_token":"..."}`，
  其中 `session_token` 可省略。

支持的 `type` 为 `azure`、`google`、`aws`、`deepl` 和 `libretranslate`。
Azure、Google、AWS 和 DeepL 有默认官方 API 地址；LibreTranslate 必须配置
`endpoint`。所有供应商都可以覆盖 `endpoint`，便于走企业代理或私有网关。

## 连通性测试

配置保存后可通过 `im-admin` 单独测试任意供应商。测试不会经过路由，也不会在响应
或日志中输出密钥。

```http
POST /v0/admin/translation/providers/azure-primary/test
Authorization: Bearer <admin-token>
Content-Type: application/json

{
  "text": "你好",
  "source_language": "zh",
  "target_language": "en"
}
```

成功响应包含供应商 ID、译文、检测语言和耗时。线上消息只翻译 P2P 的纯字符串
`kind=text`；富文本、图片、文件、语音、贴纸和群聊保持原行为。

如果 `im-server` 先于 `im-admin` 启动，或暂时读取不到有效策略，默认采用
fail-closed 行为：接收方不会看到未翻译原文。数据库中出现有效策略后会在下一个刷新
周期自动生效。

## 客户端处理

`data.translation.status` 的值为：

- `original`：当前用户是发送方，或正文已经是目标语言。
- `pending`：翻译任务已进入队列；客户端应显示“翻译中”，不要把 `content=null`
  当成消息删除。
- `completed`：`content` 是译文；若启用 `keep_original`，原文位于
  `translation.original`。
- `failed`：所有供应商均失败；正文是否为空由 `failure_policy` 决定。

`pending` 和最终状态使用相同的 `topic + seq`，客户端应按该键替换现有气泡，而不是
新增一条消息。

## 供应商申请与推荐部署架构

| 翻译服务商 | 官方入口 | 免费额度 | 建议应用场景 |
| :--- | :--- | :--- | :--- |
| **LibreTranslate** | 开源 Docker 本地部署 (`docker run -d -p 5000:5000 libretranslate/libretranslate`) | 完全免费 | 0 成本本地测试、保底备份节点 |
| **DeepL API** | [deepl.com/pro-api](https://www.deepl.com/pro-api) | 每月免费 50万 字符 | 追求最高翻译精度、跨境商务聊天 |
| **Azure AI Translator** | [azure.microsoft.com](https://azure.microsoft.com) | 每月免费 200万 字符 (F0) | 企业级稳定输出，免费额度大 |
| **Google Cloud Translation** | [cloud.google.com/translate](https://cloud.google.com/translate) | 每月免费 50万 字符 | 多语种覆盖全 |
| **Amazon Translate** | [aws.amazon.com/translate](https://aws.amazon.com/translate) | 首年每月免费 200万 字符 | AWS 基础设施体系生态集成 |

### 生产环境推荐组合策略
- **主用 (Priority 10)**：配置 DeepL 或 Azure AI Translator，利用其免费额度获得高质量翻译。
- **备用 (Priority 20)**：部署本地 LibreTranslate Docker 容器。一旦云端 API 额度耗尽或网络异常，系统自动无缝降级到本地 LibreTranslate 兜底，保证聊天功能服务永不断线。

