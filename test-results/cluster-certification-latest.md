# 集群进程认证报告

- 开始时间：2026-07-29T14:44:16Z
- 完成时间：2026-07-29T14:45:25Z
- Git commit：89eb0025180885e53641d70b17a24b47edf484ca
- 工作区存在未提交变更：yes
- 被测 im-server SHA-256：ad8a5e1d719ff5a7a0c939a1a4a4238109ebe1847074ef7998616fd8f3829d9b
- Owner SIGKILL RTO：4s（门限 15s）
- 在线拓扑：3→5→3 通过
- etcd 多数派丢失：fail-closed 与自动租约恢复通过
- 单节点隔离超过 lease TTL：旧视图拒写与自动重新注册通过
- 数据库失联：全节点摘流与恢复通过
- 滚动重启：3/3 节点通过
- 容量门禁：32 接收者 × 300 消息，ACK p99≤300ms，投递 p99≤500ms

## 容量测试原始输出

```text
=== RUN   TestStandaloneWebSocketHotTopic
    message_lifecycle_test.go:322: 真实热点 Topic 完成：订阅者=32，消息=300，网络投递=9600，总耗时=1.987043708s，ACK p95=11.209ms，ACK p99=31.683541ms，ACK max=46.240792ms，投递 p95=11.370792ms，投递 p99=31.771666ms，投递 max=46.411375ms
--- PASS: TestStandaloneWebSocketHotTopic (4.04s)
=== RUN   TestStandaloneWebSocketReconnectBurst
    protocol_consistency_test.go:206: 重连突发完成：连接=256，总耗时=124.2085ms，p95=123.349834ms，最大=123.560791ms
--- PASS: TestStandaloneWebSocketReconnectBurst (0.12s)
PASS
ok  	chat/tests/standalone	5.038s
```
