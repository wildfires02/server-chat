# 数据库版本、迁移记录与固定操作流程

> 文档类型：运维与开发规范  
> 当前数据库版本：`123`
>
> 版本含义：服务端持久化结构修订号，不是产品版本或协议版本

本文档必须记录每一次数据库版本变化的原因、结构差异、升级方式、验证方法和回滚
影响。以后新增数据库版本时，代码和本页必须在同一个变更中更新。

## 1. 为什么会从 `122` 升级到 `123`

聊天记录现在执行统一的 90 天保留策略。清理任务会持续查找“尚未清理且创建时间
早于截止时间”的消息，并按创建时间、消息 ID 分批处理。如果没有专用索引，消息量
增长后每轮清理都可能扫描并排序整张消息表，影响在线收发消息。

版本 `123` 因此只增加清理索引，不修改消息协议：

| 适配器 | 新结构 |
| --- | --- |
| MySQL | `messages_retention_deletedat_createdat(deletedat, createdat, id)` |
| PostgreSQL | `messages_retention_createdat(createdat, id) WHERE deletedat IS NULL` |
| MongoDB | `messages.createdat` 索引 |
| RethinkDB | `messages.CreatedAt` 二级索引 |

迁移不删除历史消息；服务启动后才由 `message_retention` 后台任务按配置分批清理。
因此升级本身可安全执行，但回滚旧二进制仍受严格版本检查限制。

### 历史说明：为什么会从 `121` 升级到 `122`

群消息 Seen by 需要回答两个不同问题：

1. 哪些成员已经读到指定消息。
2. 成员是什么时间读到该消息。

版本 `121` 的订阅只有 `ReadSeqId`，能回答第一个问题，但没有逐消息阅读时间。
`Subscription.UpdatedAt` 还会被权限、私有数据等其他操作更新，不能作为阅读时间。

版本 `122` 因此新增订阅阅读检查点：

| 适配器 | 新结构 |
| --- | --- |
| MySQL | `subscriptions.readhistory JSON` |
| PostgreSQL | `subscriptions.readhistory JSON` |
| MongoDB | `subscriptions.readhistory` |
| RethinkDB | `subscriptions.ReadHistory` |

检查点以连续序号范围保存服务端时间，例如：

```json
[
  {
    "low": 35,
    "high": 42,
    "at": "2026-07-31T08:29:12Z"
  }
]
```

因此 `121 → 122` 是真实的数据库结构升级，不能只修改代码中的版本常量，也不能
只把 `kvmeta` 改为 `122`。完整功能说明见
[群消息 Seen by 协议与数据库升级](message-seen-by.md)。

## 2. 版本什么时候应该变化

以下修改必须提升数据库版本：

- 新增、删除或修改表、集合、字段或索引。
- 修改已有持久化数据的编码格式、约束或唯一性规则。
- 需要对历史数据执行回填或清理。
- 新代码无法在旧结构上安全读写。

以下修改通常不提升数据库版本：

- UI、日志、注释、测试和文档。
- 不涉及持久化格式的业务逻辑。
- 只增加内存字段或临时协议字段。
- 与旧数据库结构完全兼容的查询优化。

判断标准不是“代码改了多少”，而是“新旧二进制是否要求不同的持久化结构”。

## 3. 近期迁移记录

| 迁移 | 原因 | 主要变化 |
| --- | --- | --- |
| `118 → 119` | 跨数据库消息全文搜索 | 为消息增加 `searchtext`，并从历史文本/Drafty 内容回填。 |
| `119 → 120` | 集群 Topic Owner 隔离栅栏 | 为 Topic 增加 `clusterowner` 与 `clusterepoch`。 |
| `120 → 121` | 官方大群成员游标分页 | MongoDB、RethinkDB 增加 Topic + User 复合索引；MySQL、PostgreSQL 复用已有唯一索引并同步版本。 |
| `121 → 122` | 群消息 Seen by 阅读时间 | 为订阅增加滚动 `readhistory` 阅读检查点。 |
| `122 → 123` | 90 天聊天记录保留 | 为四种数据库增加消息保留任务使用的时间索引，避免定时全表扫描。 |

新增 `123` 或更高版本时，必须在此表追加一行，不能只在迁移代码中留注释。

## 4. 开发者每次新增版本怎么做

假设当前版本为 `122`，目标版本为 `123`。

### 第一步：写清迁移目的

在本页“近期迁移记录”追加 `122 → 123`，说明：

- 为什么旧结构无法满足需求。
- 新增、删除或修改了哪些结构。
- 历史数据是否需要回填。
- 是否可以安全回滚。

### 第二步：同时更新四个适配器版本

修改：

```text
server/db/mysql/adapter.go
server/db/postgres/adapter.go
server/db/mongodb/adapter.go
server/db/rethinkdb/adapter.go
```

四个 `adpVersion` 必须保持一致，禁止只升级当前开发使用的 MySQL。

### 第三步：补全新数据库结构

全新建库必须直接得到目标版本结构。按适配器更新：

```text
server/db/mysql/schema.go
server/db/mysql/schema.sql
server/db/postgres/schema.go
server/db/mongodb/schema.go
server/db/rethinkdb/schema.go
```

如果 MongoDB 或 RethinkDB 不需要固定字段，也要确认索引和初始版本正确。

### 第四步：新增连续迁移

每个适配器的 `UpgradeDb` 必须包含连续步骤：

```go
if a.version == 122 {
    // 执行 122 → 123 的结构或数据迁移。
    // ...
    if err := bumpVersion(a, 123); err != nil {
        return err
    }
}
```

要求：

- 不能跳过中间版本。
- 只有本次迁移全部成功后才 `bumpVersion`。
- SQL DDL、索引和回填失败必须返回错误。
- 迁移应尽量可重复判断，但不要假设所有数据库 DDL 都支持事务回滚。

### 第五步：更新模型和读写映射

检查：

- `server/store/types/` 中的领域模型。
- MySQL/PostgreSQL 的 SELECT、INSERT、UPDATE 和 Scan 顺序。
- MongoDB 的 BSON 字段名称。
- RethinkDB 的 JSON/结构体字段名称。
- 订阅重建、软删除恢复和清理路径是否需要重置新字段。

只添加 DDL 而漏掉查询扫描，会造成启动成功但运行时报错。

### 第六步：补测试

至少执行：

```bash
go test ./server/store/types ./internal/server
go test ./server/db/mysql
go test -tags postgres ./server/db/postgres
go test -tags mongodb ./server/db/mongodb
go test -tags rethinkdb ./server/db/rethinkdb
```

有条件时还必须对每种真实数据库执行：

1. 从前一版本升级。
2. 直接创建全新数据库。
3. 运行关键读写路径。
4. 验证失败迁移不会错误提升版本。

### 第七步：同步操作文档

同一变更必须更新：

- 本页的迁移记录。
- [`cmd/init-db/README.md`](../cmd/init-db/README.md) 中的操作说明。
- 受影响功能的专题文档。
- 如果用户会直接看到新协议，同时更新 `API.md` 和调用示例。

## 5. 部署者每次升级怎么做

以下是所有版本升级的固定流程，不限于某一次具体迁移。

### 5.1 确认目标

先从启动日志确认：

```text
DB adapter mysql 123
Invalid database version 122. Expected 123
```

含义是：

- 当前二进制要求 `123`。
- 当前数据库仍是 `122`。
- 必须执行 `122 → 123` 迁移。

如果数据库版本大于二进制要求，禁止降级运行旧二进制。

### 5.2 备份并停止写入

生产环境必须：

1. 停止或排空旧服务写入。
2. 创建可恢复备份。
3. 实际验证恢复命令、权限和备份位置。
4. 记录升级前数据库版本。

开发环境也建议先保留数据库快照；不要用 `--reset` 代替升级来掩盖迁移问题。

### 5.3 使用目标代码执行升级

在 `server-chat` 根目录：

```bash
go run ./cmd/init-db \
  --config=./configs/im.yaml \
  --upgrade=true
```

使用预构建二进制时，build tag 必须与数据库一致：

```bash
go build -tags mysql -o bin/init-db ./cmd/init-db

./bin/init-db \
  --config=./configs/im.yaml \
  --upgrade=true
```

PostgreSQL、MongoDB、RethinkDB 分别替换对应 build tag。

### 5.4 确认迁移成功

日志必须包含：

```text
Database successfully upgraded.
```

然后检查版本及本次新增结构。历史 `121 → 122` 的 MySQL 检查方式为：

```sql
SELECT `value` FROM kvmeta WHERE `key` = 'version';
SHOW COLUMNS FROM subscriptions LIKE 'readhistory';
```

对于当前 `122 → 123` 迁移，MySQL 应验证：

```sql
SELECT `value` FROM kvmeta WHERE `key` = 'version';
SHOW INDEX FROM messages WHERE Key_name = 'messages_retention_deletedat_createdat';
```

版本应为 `123`，并且消息保留索引存在。

### 5.5 启动目标服务并验证

```bash
go run ./cmd/im-server
```

然后：

```bash
curl --fail http://127.0.0.1:6060/livez
curl --fail http://127.0.0.1:6060/readyz
```

还要验证本次迁移对应的业务功能，而不只是进程和健康检查。

### 5.6 失败处理

- 迁移失败时不要手工把 `kvmeta` 改成目标版本。
- 保存完整错误日志，确认 DDL 是否已经部分执行。
- 根据本次迁移文档判断继续修复还是恢复备份。
- 未确认结构完整之前不得启动目标服务承载写入。

## 6. 为什么服务启动时不自动升级

`im-server` 只校验版本，不自动调用 `UpgradeDb`，这是有意设计：

- 防止普通服务启动意外修改生产数据库。
- 防止多个实例同时执行 DDL 或历史数据回填。
- 让备份、停写、迁移和验证成为显式发布步骤。
- 让迁移失败与业务进程启动失败分开处理。

开发环境如果希望一条命令启动，可以在外部脚本中按顺序执行
`init-db --upgrade=true` 和 `im-server`，但生产服务本身仍不应自动迁移。

## 7. 回滚规则

数据库版本升级后，要求旧版本的二进制会在启动时拒绝连接。回滚不能只把
`kvmeta` 数字改小。

发布前必须选择并验证一种回滚方式：

1. 使用能够读取新结构的兼容二进制回滚业务代码。
2. 执行经过测试的反向迁移。
3. 恢复升级前数据库备份。

当前 `122 → 123` 只新增索引，但旧二进制仍会因严格版本校验拒绝启动。
因此即使 DDL 本身向后兼容，也需要兼容二进制或备份恢复方案。

## 8. 禁止事项

- 禁止只修改四个 `adpVersion` 而不写迁移。
- 禁止只支持一种数据库适配器。
- 禁止手工修改 `kvmeta` 冒充迁移完成。
- 禁止对有数据的生产库使用 `--reset=true`。
- 禁止在未备份、未停写的情况下执行生产 DDL。
- 禁止新增版本后不更新本页迁移历史。
