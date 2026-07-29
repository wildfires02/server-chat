# 开发单机版性能基线

> 文档信息
>
> - 日期：2026-07-29
> - 主机：Apple M4，darwin/arm64
> - 范围：序列化、会话队列、消息生命周期、热点网络投递、长连接、垃圾回收、重连、数据库韧性和崩溃恢复
> - 用途：开发性能回归，不是生产容量承诺

## 1. 执行命令

```bash
go test ./internal/server -run '^$' \
  -bench 'BenchmarkStandalone(JSONSerialize|GRPCSerialize|SessionQueue|TopicFanout)' \
  -benchmem -benchtime=2s -count=3
```

## 2. 当前结果

| 热路径 | 延迟范围 | 内存 | 分配次数 |
| --- | ---: | ---: | ---: |
| JSON 消息序列化 | 517.9～542.6 ns/op | 328 B/op | 6 allocs/op |
| gRPC 消息结构转换 | 279.7～395.8 ns/op | 760 B/op | 7 allocs/op |
| Session 有界队列入队并消费 | 15.92～16.14 ns/op | 0 B/op | 0 allocs/op |
| Topic Fanout，100 个订阅者 | 9.615～9.689 µs/op | 33,600 B/op | 200 allocs/op |
| Topic Fanout，1000 个订阅者 | 102.155～110.478 µs/op | 336,000 B/op | 2000 allocs/op |

JSON 基准覆盖 WebSocket 和 Long Polling 共用的 `ServerComMessage` 序列化。gRPC 基准覆盖内部消息到 Protobuf 结构的转换，不包括网络写入和 Protobuf wire 编码。Session 基准验证本地投递队列本身没有堆分配。Fanout 基准覆盖普通群本地消息复制和 Session 入队，不包含数据库和 Socket 写入。

## 3. 真实进程结果

测试进程使用脚本专属的 MySQL 隔离临时库，HTTP/WS 监听 `127.0.0.1:26060`，客户端 gRPC 监听 `127.0.0.1:26061`。长连接 GC 采样使用 `GOGC=25` 强制形成可观测窗口；该 GC 数值用于回归，不代表默认 `GOGC` 下的生产容量。

| 场景 | 结果 |
| --- | --- |
| WebSocket、Long Polling、gRPC | hi、basic 登录、建群、文本发布、相同 Client ID 重试全部通过 |
| 七类消息生命周期 | text、drafty、image、video、voice、audio、file 发布和历史核对通过；file 物理删除后不再出现在历史 |
| 16 订阅者热点 Topic | 100 条消息、1600 次真实网络投递全部完成；总耗时 301.444 ms；ACK p95 4.770 ms、p99 7.596 ms、最大 8.744 ms |
| 64 路 WebSocket 重连突发 | 总耗时 4.425 ms，p95 4.158 ms，最大 4.231 ms |
| 256 个空闲 WebSocket | 建立 54.458 ms；保持 60 秒并跨过 49.5 秒 Ping 周期；Session +256；goroutine +508 |
| 256 个空闲 WebSocket 内存 | Alloc +1,849,392 B；HeapInuse +1,048,576 B；约 4,096 B/连接 |
| 强制 GC 窗口 | GC 5 次；GC p99 45.333 µs；最大 45.333 µs |
| MySQL 分级查询延迟 | 10/50/200 ms 目标分别观测到 12.246/53.073/203.533 ms |
| MySQL 连接池耗尽 | 最大连接 2；第三个请求约 151.058 ms 按上下文超时；连接释放后 Ping 恢复 |
| ACK 后进程崩溃 | 第一进程 ACK 后立即 SIGKILL；第二进程读回原 topic/cid/seq，幂等重试返回原 seq |
| MySQL 完全失联 | `readyz` 从 200 切到 503 |
| MySQL 恢复 | 同一服务进程无需重启，`readyz` 自动恢复 200 |
| SIGTERM 优雅关闭 | 20 秒边界内退出码为 0，日志包含数据库关闭和 `All done, good bye` |

空闲连接内存是单次 macOS 进程采样，包含连接建立期间的共享堆和 GC 变化，不等于稳定 RSS，也不能线性外推到生产连接上限。热点 ACK 延迟同时包含本机 Socket、业务处理和 MySQL 持久化，不是独立网络或数据库指标。

## 4. 自动回归

`scripts/test-standalone.sh` 默认执行不依赖 Docker 的配置、功能和 race 回归：

- `configs/im.standalone.yaml` 离线启动门禁。
- 独立单机配置解析。
- 显式单机模式跳过无效 cluster_config 的测试。
- 本地 Topic Resolver 和慢 Session 背压测试。
- 100/1000 订阅者本地热点扇出测试和 Benchmark。
- `internal/server` 共享业务测试。
- `internal/server` 数据竞争检查。

设置 `IM_STANDALONE_BENCHMARK=1` 时追加序列化、队列和热点扇出微基准。

完整真实进程回归使用一条命令：

```bash
./scripts/test-standalone-process.sh
```

脚本自动管理专属 MySQL 容器、构建和初始化、两轮服务进程、真实协议测试、ACK 后 SIGKILL、数据库失联以及最终 SIGTERM；任何退出路径都会清理专属容器和临时文件。可通过 `IM_STANDALONE_E2E_*` 环境变量调整镜像、端口、连接数、保持时间和热点规模。

## 5. 生产容量认证边界

开发单机版完成定义已经满足。以下是独立的生产容量认证，不属于单机版完成阻塞项，且正式业务仍必须使用集群版：

- WebSocket、Long Polling 和 gRPC 在持续并发发布下的吞吐、在线投递 p95/p99。
- 固定 Linux 下的单节点最大空闲连接数、稳定 RSS 和多小时 GC 分布。
- 1 KiB、64 KiB、4 MiB 消息吞吐。
- 普通群、频道和单热点 Topic 的广播吞吐。
- 多小时数据库退化、连接池耗尽和入口背压曲线。
- 大规模历史同步、登录恢复和重连风暴。

单机版即使通过上述测试也只能用于开发、测试和性能对照，正式流量必须部署集群版。
