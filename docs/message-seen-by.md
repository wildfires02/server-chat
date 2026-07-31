# 群消息 Seen by 协议与数据库升级

> 文档状态：已实现  
> 适用协议：`0.31`  
> 首次需要的数据库版本：`122`

本文档是群消息逐成员已读查询的权威说明。普通的 `{note what=read}`、
会话级 `readseqid` 和多端同步仍按 [API.md](API.md) 工作；Seen by 在此基础上
补充“哪些成员读过指定消息”以及可用时的阅读时间。

## 1. 产品行为

客户端只应在同时满足以下条件时显示 Seen by：

1. Topic 是普通 `grp...` 群聊，不是 P2P，也不是广播频道。
2. 消息由当前用户发送。
3. 消息已经取得服务端 `seq`，不是本地发送中或尚未投递的定时消息。
4. 消息创建时间不超过 7 天。
5. 群成员数不超过 100。

服务端会再次执行全部安全检查，不能依赖客户端隐藏菜单来保护数据。响应不包含
当前发送者本人；已经退出、被封禁或没有 Reader 权限的订阅不会出现在结果中。

## 2. JSON 协议

客户端必须已经订阅目标 Topic，然后发送：

```json
{
  "get": {
    "id": "readers-42",
    "topic": "grpYiqEXb4QY6s",
    "what": "readers",
    "readers": {
      "seq": 42
    }
  }
}
```

成功响应：

```json
{
  "meta": {
    "id": "readers-42",
    "topic": "grpYiqEXb4QY6s",
    "ts": "2026-07-31T08:30:00Z",
    "readers": {
      "seq": 42,
      "users": [
        {
          "user": "usrAlice",
          "date": "2026-07-31T08:29:12Z"
        },
        {
          "user": "usrBob"
        }
      ]
    }
  }
}
```

字段说明：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `get.what` | 是 | 必须包含 `readers`。 |
| `get.readers.seq` | 是 | 正整数，目标消息在当前 Topic 内的服务端序号。 |
| `meta.readers.seq` | 是 | 与查询对应的消息序号。 |
| `meta.readers.users[].user` | 是 | 已读成员的用户 ID。 |
| `meta.readers.users[].date` | 否 | 服务端记录的阅读时间；没有历史检查点时省略。 |

结果按 `date` 从新到旧排序，没有 `date` 的兼容数据排在最后。空数组表示尚无其他
成员读到该消息。

常见失败：

| Code | 场景 |
| --- | --- |
| `400` | `readers` 参数缺失，或 `seq` 不是正整数。 |
| `403` | 请求者不是 Reader，或目标消息不是请求者发送。 |
| `404` | 目标消息不存在。 |
| `405` | Topic 不是普通群、群超过 100 人、消息超过 7 天或通过频道身份查询。 |

## 3. gRPC / Protobuf

[`api/pbx/chat.proto`](../api/pbx/chat.proto) 定义：

- `GetQuery.readers: ReadParticipantsQuery`
- `ServerMeta.readers: ReadParticipants`
- `ReadParticipant.user_id`
- `ReadParticipant.date`，Epoch 毫秒；`0` 表示没有可用时间。

修改 `.proto` 后必须运行：

```bash
./api/pbx/go-generate.sh
```

不要直接编辑生成的 `chat.pb.go`。

## 4. 阅读时间如何产生

订阅仍使用单调递增的 `ReadSeqId` 表示已读水位。成员从 `oldReadSeq` 上报到
`newReadSeq` 时，服务端以自己的时间记录一个连续检查点：

```text
[oldReadSeq + 1, newReadSeq] -> server read time
```

这表示该范围在同一次已读上报中被确认，不代表用户逐条停留的时间。服务端不会
信任客户端提供的时间。

每个订阅：

- 查询窗口为最近 7 天。
- 过期检查点在下一次已读推进时清理。
- 最多保留 4096 个检查点，防止高频上报导致记录无限增长。
- 即使时间检查点被清理，`ReadSeqId >= seq` 时仍可判断成员已经读过，只是响应中
  不再返回 `date`。

数据库升级前已经读过的消息没有可信时间，服务端不会使用订阅 `updatedat`
伪造阅读时间。客户端应显示类似 `Read time unavailable` 的兼容提示。

## 5. 持久化结构

版本 `122` 为订阅增加滚动阅读检查点：

| 适配器 | 字段 |
| --- | --- |
| MySQL | `subscriptions.readhistory JSON` |
| PostgreSQL | `subscriptions.readhistory JSON` |
| MongoDB | `subscriptions.readhistory` |
| RethinkDB | `subscriptions.ReadHistory` |

SQL JSON 示例：

```json
[
  {
    "low": 35,
    "high": 42,
    "at": "2026-07-31T08:29:12Z"
  }
]
```

成员重新加入并重置订阅游标时，旧 `readhistory` 同时清空。

## 6. 数据库 `121 → 122` 升级

`121` 只有会话级 `ReadSeqId`，没有逐消息阅读时间；`122` 新增
`readhistory`，因此这是必须执行的结构迁移。版本变化原因、近期迁移历史、
开发者新增版本检查表、部署升级顺序和回滚规则统一维护在
[数据库版本、迁移记录与固定操作流程](database-migrations.md)。

## 7. 验证

服务端代码验证：

```bash
go test ./server/store/types ./internal/server
go test ./server/db/mysql
go test -tags postgres ./server/db/postgres
go test -tags mongodb ./server/db/mongodb
go test -tags rethinkdb ./server/db/rethinkdb
```

数据库验证：

```sql
SELECT `value` FROM kvmeta WHERE `key` = 'version';
SHOW COLUMNS FROM subscriptions LIKE 'readhistory';
```

MySQL 应返回版本 `122` 和 `readhistory` 字段。其他适配器使用对应的 schema
检查工具确认版本及字段存在。
