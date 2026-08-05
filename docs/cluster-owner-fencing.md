# 集群 Topic Owner Fencing

> 文档信息
>
> - 类型：架构说明
> - 相关操作：[生产集群操作手册](cluster-operations.md)

## 目标

仅靠“节点本地认为自己是 Owner”无法阻止网络分区中的旧节点继续写入。生产控制面因此同时使用：

- etcd Lease 和多数派成员视图；
- 仅随 IM 成员 Put/Delete 变化的 `Cluster View epoch`；
- 数据库全局 `cluster fence`；
- Topic 行上的 `clusterowner`、`clusterepoch`。

任一环节无法确认时均采用故障关闭策略：节点退出写就绪状态，消息事务返回
`cluster owner fenced`。

## 提交流程

1. etcd Watch 观察到成员变化，将事件 Revision 单调写入专用 `view-epoch` marker。
2. 节点线性一致读取 members 与 marker，生成确定性的成员视图。
3. 应用新视图前，节点先调用 `ClusterFenceAdvance` 单调推进共享数据库 fence。
4. fence 成功后才切换本地一致性哈希 Ring，并公开新的 `viewEpoch`。
5. Topic Owner 为消息附加 `cluster_id`、`cluster_epoch`、`cluster_owner`。
6. 数据库事务锁定并精确校验全局 fence，再声明 Topic Owner、推进 seq、保存消息。
7. 数据库提交成功后才向客户端 ACK。

这样，新视图一旦推进数据库 fence，旧 epoch 的事务就无法提交；同一 epoch 下不同 Owner 也无法接管同一 Topic。

## 数据库支持

| 适配器 | 控制面集群支持 | 约束 |
| --- | --- | --- |
| PostgreSQL | ✅ | `FOR SHARE` 锁定 fence 行，与 fence 推进互斥 |
| MySQL 8 | ✅ | `FOR SHARE` 锁定 fence 行，与 fence 推进互斥 |
| MongoDB | ✅ | 必须配置 Replica Set，使用跨文档事务和 fence 条件写 |
| RethinkDB | ❌ | 缺少同等级跨文档事务，配置门禁明确拒绝 |

数据库 Schema 当前版本为 `123`。PostgreSQL 与 MySQL 的新增字段均带中文 COMMENT；
MongoDB、RethinkDB 的字段语义记录在 Go 模型中文注释中。`120→121` 为官方大群
成员游标查询补齐复合索引；MySQL/PostgreSQL 复用既有 `(topic, userid)` 唯一
索引。后续版本历史统一见[数据库版本与迁移流程](database-migrations.md)。

## 已验证场景

- 三节点 etcd Lease 注册、Watch、多数派和租约注销；
- 无关 etcd key 写入不会推动 IM Cluster View epoch；
- 成员删除后存活节点收敛到同一更高 epoch；
- PostgreSQL/MySQL 首个 Owner 写入成功；
- fence 推进后旧 Owner 写入被拒绝；
- 新 Owner 使用新 epoch 接管成功；
- 已提交 fence 不能回退。

完整生产认证状态见 [`planning/cluster.md`](planning/cluster.md)；gRPC Lane、可靠重试、
Readiness/Drain、mTLS 和本地网络故障矩阵已经完成，目标环境 72 小时与发布评审仍待执行。
