# 联系人、文件处理与素材消息

> 本页仅是现有协议与实现参考，不是独立产品需求文档。联系人、文件、贴纸、动态
> Emoji、GIF 及其它功能的统一需求和状态以
> [`im-product-requirements.md`](im-product-requirements.md) 为准。

本文描述协议 `0.30` 新增的服务端联系人同步、文件处理和贴纸/GIF 素材目录。
JSON、WebSocket、长轮询和 gRPC 使用相同的字段语义。

## 联系人

联系人操作只能在当前用户的 `me` Topic 上执行。创建分组：

```json
{
  "set": {
    "id": "group-1",
    "topic": "me",
    "contact": {
      "op": "upsert_group",
      "group": {"id": "team", "name": "团队"}
    }
  }
}
```

本地联系人 CRUD 使用 `upsert_contact`、`delete_contact`；双向好友关系使用
`request_friend`、`accept_friend`、`remove_friend`：

```json
{"set":{"id":"friend-1","topic":"me","contact":{"op":"request_friend","user":"usr..."}}}
{"set":{"id":"friend-2","topic":"me","contact":{"op":"accept_friend","user":"usr..."}}}
```

首次登录设备请求全量快照；后续设备保存服务端 `version` 并请求增量：

```json
{"get":{"id":"contacts-1","topic":"me","what":"contacts","contacts":{"since":0,"recommendations":true}}}
{"get":{"id":"contacts-2","topic":"me","what":"contacts","contacts":{"since":18,"limit":100}}}
```

当服务端返回 `reset:true` 时，客户端必须丢弃本地通讯录并应用本次全量快照。
通讯录变更会向当前用户其它设备发送 `{pres what:"contacts"}`；好友请求和接受
也会通知另一方重新同步。

## 文件 ACL、断点续传和在线预览

普通上传仍使用 `/v0/file/u/`。服务端计算 SHA-256，不信任客户端摘要。附件发布前
验证上传所有者；消息落库后，文件 ACL 绑定到 Topic，下载时再次验证当前订阅读权限。
对象存储预签名重定向也只会在 ACL 校验通过后生成。

断点续传使用 `/v0/file/resumable/`：

1. `POST` 携带 `Upload-Length` 和可选 `Upload-Mime-Type`，响应 `201` 与 `Location`。
2. `PATCH Location` 携带 `Upload-Offset`，正文是原始分块；响应返回新的偏移。
3. `HEAD Location` 查询服务端偏移。
4. 最后一块完成后，响应头 `Upload-Result-URL` 返回最终文件 URL。
5. `DELETE Location` 取消会话。

所有请求都需要与普通文件接口相同的 API Key 和登录认证。偏移不一致返回 `409`，
上传会话 24 小时未完成会被回收。

上传会话、偏移、共享分块清单和短期写租约保存在持久缓存中；分块本体直接写入配置的
媒体存储。集群使用 S3 等共享处理器后，请求不需要节点粘性，任意节点都能从数据库
确认的偏移继续。生产或预发布集群会拒绝本地 `fs` handler；开发集群使用 `fs` 时
`upload_dir` 必须是所有节点可见的共享目录。

文件处理状态通过以下接口轮询：

```text
GET /v0/file/meta/?url=<经过 URL 编码的文件 URL>
```

响应的 `processing` 包含 `sha256`、`scan_status`、`process_status`、`preview`、
`attempts`、`next_retry_at` 和失败原因。扫描处于 `pending`、`scanning`、`error`
或隔离状态时，服务端拒绝附件发布和文件下载。文件所有者可对同一地址发送 `POST`，
把已完成或死信任务重新排入持久处理队列。

可在 `media.processing` 启用真实后台处理：

```json
{
  "media": {
    "use_handler": "fs",
    "processing": {
      "enabled": true,
      "workers": 2,
      "queue_size": 256,
      "timeout": 120,
      "poll_interval": 2,
      "max_attempts": 5,
      "retry_base": 5,
      "lease_seconds": 180,
      "clamav_addr": "clamav:3310",
      "ffmpeg": "/usr/bin/ffmpeg",
      "libreoffice": "/usr/bin/libreoffice"
    }
  }
}
```

- ClamAV 使用 `INSTREAM` 协议扫描实际上传字节，命中后进入隔离状态。
- FFmpeg 为图片/GIF 生成 WebP，为视频生成海报和压缩 MP4，为音频生成 Opus。
- LibreOffice 将 Word、Excel、PowerPoint 和 OpenDocument 转换为 PDF。
- 服务端生成的预览继承原文件 ACL。PDF 预览 URL 带 `preview=true`，只允许安全的
  `application/pdf` 内联响应；HTML/XML 始终强制下载。
- 每个处理任务先持久化再唤醒 Worker。节点通过数据库 CAS 抢占租约；节点退出或租约
  到期后其它节点自动恢复。失败按指数退避重试，超过 `max_attempts` 后标记为 `dead`。
- expvar 提供任务领取、租约恢复、完成、重试、死信、上传分块和租约冲突计数。

处理器未启用时状态为 `scan_status:"disabled"`，不会伪装成已完成安全扫描。

## 贴纸、动态 Emoji 和 GIF

素材包和素材由 root 用户在 `me` Topic 管理：

```json
{
  "set": {
    "id": "asset-1",
    "topic": "me",
    "asset": {
      "op": "upsert_pack",
      "pack": {"id": "animals", "name": "Animals", "published": true}
    }
  }
}
```

素材操作为 `upsert_pack`、`delete_pack`、`upsert_asset`、`delete_asset`。已发布素材
文件自动获得“已登录用户可读”的公共 ACL；下架或删除时撤销。客户端查询目录：

```json
{"get":{"id":"assets-1","topic":"me","what":"assets","assets":{"kind":"sticker","limit":100}}}
```

消息按主流 IM 的方式只保存稳定素材 ID，图片或动画二进制不写入消息正文。服务端目录
保存素材的 `url`、`mime`、`sha256`、`size`、`revision` 和可选 `variants`；
客户端负责本地包、内存/磁盘缓存和 CDN 的分级解析。当前视口出现多个未命中素材时，
应合并成一次精确查询，单次最多 200 个 ID：

```json
{
  "get": {
    "id": "assets-visible-1",
    "topic": "me",
    "what": "assets",
    "assets": {"asset_ids": ["cat-wave", "party-face"]}
  }
}
```

root 发布素材示例：

```json
{
  "set": {
    "id": "asset-2",
    "topic": "me",
    "asset": {
      "op": "upsert_asset",
      "asset": {
        "id": "party-face",
        "pack_id": "official",
        "kind": "animated-emoji",
        "url": "/v0/file/s/primary",
        "mime": "application/gzip",
        "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "size": 2048,
        "alt": "🎉",
        "published": true,
        "variants": [{
          "name": "webm",
          "url": "/v0/file/s/webm",
          "mime": "video/webm",
          "width": 100,
          "height": 100,
          "size": 1024,
          "sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
        }]
      }
    }
  }
}
```

本地上传文件的 MIME、大小和 SHA-256 由服务端上传记录回填并核对；外部 HTTPS CDN
素材由 root 提交这些字段。素材首次写入后 `revision` 由服务端设为 1。同一个 ID 的
包、类型、URL、预览、MIME、尺寸、时长、摘要、大小和变体不可原地替换；换图必须
创建新 ID。关键词、替代文本和发布状态仍可修改。`delete_asset`/`delete_pack` 是
软下架：保留管理员可审计的不可变目录记录，但撤销公共文件访问并禁止新消息引用。

客户端解析顺序为：

1. 内置官方小素材包（可选，用于冷启动和离线）。
2. 内存或磁盘缓存，缓存键使用 `asset_id + revision + variant`。
3. 按 `asset_ids` 向服务端批量解析目录，再从对象存储/CDN 下载合适规格。
4. 下载后校验 SHA-256；失败、下架或不支持格式时显示消息中的 `alt` 或统一占位图。

因此“图片放前端”只适合少量默认素材，不应把不断增长的全量素材编译进每个客户端；
完整素材应由对象存储/CDN 承载，服务端目录控制发布、版本、权限和下架。

Drafty 实体类型分别为：

- `SK`：贴纸，消息 `kind` 为 `sticker`。
- `AE`：动态 Emoji，消息 `kind` 为 `animated-emoji`。
- `GF`：GIF，消息 `kind` 为 `gif`。

实体必须携带已发布的服务端 `asset_id`：

```json
{
  "pub": {
    "id": "sticker-1",
    "topic": "grp...",
    "kind": "sticker",
    "content": {
      "txt": " ",
      "fmt": [{"at": 0, "len": 1, "key": 0}],
      "ent": [{"tp": "SK", "data": {"asset_id": "cat-wave", "alt": "👋"}}]
    }
  }
}
```

`alt` 是可选的纯文本降级内容，最大 64 字节。服务端不接受客户端任意 URL 冒充素材；
`asset_id` 不存在、未发布或类型不匹配时拒绝消息。

### 首批素材

仓库已收录一组可用于开发和初始部署的
[Animated Noto Emoji 素材](../assets/starter-media/noto/README.md)：

- 8 个 Animated WebP 贴纸。
- 8 个 Animated WebP 动态 Emoji。
- 8 个 GIF 反应。

素材清单包含本地路径、官方来源 URL、Unicode codepoint、中英文关键词和 SHA-256。
生产部署应上传本地副本并使用服务端文件 URL；直接使用官方 URL 只适合快速验证。
Animated Noto Emoji 按 CC BY 4.0 许可，分发时必须保留目录中的归属说明。

## 生产化边界与未实现能力

本页前述接口已经可用，但以下事项尚未实现，进入生产集群前必须单独排期。

### 联系人

- 当前每个用户的联系人、分组和事件日志保存在一个压缩 PCache 值中，进程内锁不能
  解决多节点并发写入；需要迁移到正式数据表并增加版本 CAS 或事务。
- 好友申请、接受和删除依次写入双方状态，尚无跨用户原子事务，部分写入失败时可能
  出现双方关系不一致。
- 聚合状态受 PCache 单值容量限制，尚不适合超大通讯录。
- 手机号/邮箱哈希上传、隐私授权、通讯录匹配、明确的拒绝/拉黑工作流、好友申请
  限流和反骚扰尚未实现。
- 推荐仅依据共同好友，尚无推荐隐私、曝光去重、用户反馈和离线排序系统。

### 文件

- 文件处理现已使用持久任务、Worker 租约、自动重试、指数退避、死信和 expvar 指标；
  完成状态仍以文件处理元数据为业务事实。
- 断点续传现已支持共享媒体分块、跨节点续传和重启恢复。当前采用独立共享对象分块，
  尚未改为对象存储原生 Multipart Upload，也未增加每个分块的独立摘要协商。
- 文件 ACL 当前以追加 Topic/用户授权为主，消息硬删除、附件替换或 Topic 删除后尚未
  回收对应授权。
- 外部 HTTPS 文件不由本服务的 ACL、扫描和生命周期管理；如产品允许外链，需要新增
  代理下载、URL 信誉检测和隔离扫描。
- 命中恶意文件时会阻止访问，但尚无独立物理隔离存储、人工释放/删除、病毒库版本
  监控和审计流程。
- 尚无多规格缩略图、自适应视频档位、内容去重，以及真实 ClamAV、FFmpeg、
  LibreOffice 组合环境的故障和容量验证。

### 贴纸、动态 Emoji 和 GIF

- 当前只有 root 管理的全局素材目录；用户安装/卸载素材包、收藏、最近使用、个人排序
  和跨设备同步尚未实现。
- 创作者提交、版权信息、审核、版本发布、下架申诉和内容治理尚未实现。
- 目录已校验 ID、URL、SHA-256、大小、尺寸和变体结构，并保证发布后同 ID 内容不可
  替换；但尚未解析文件本体来强制校验真实格式、帧率、时长、透明通道和解压后大小。
- 当前搜索只匹配本地素材 ID 与关键词，尚无外部 GIF 提供方、热门榜、相关推荐、
  内容分级和缓存策略。

### 相关公共缺口

- 推送已有 FCM/TNPG 入口，但没有独立的全局/单会话通知偏好，也没有持久化推送队列、
  自动重试、死信和送达监控。
- 群组通话的服务端控制面使用 Agora，项目自身没有 SFU/MCU；屏幕共享、录制、直播、
  Simulcast、自适应码率和音视频质量监控仍未实现。
- 群组基础角色和成员管理已完成，邀请链接、入群审批、Slow Mode、Forum Topics、
  举报审核和管理审计仍未实现。

总体差距与优先级参见
[`docs/planning/product-gap.md`](planning/product-gap.md#410-联系人文件与素材已形成基础闭环生产化仍未完成)。
