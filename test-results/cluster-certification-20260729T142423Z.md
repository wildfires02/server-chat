# 集群进程认证报告

- 开始时间：2026-07-29T14:24:23Z
- 完成时间：2026-07-29T14:25:23Z
- Git commit：89eb0025180885e53641d70b17a24b47edf484ca
- Owner SIGKILL RTO：5s（门限 15s）
- 在线拓扑：3→5→3 通过
- etcd 多数派丢失：fail-closed 与自动租约恢复通过
- 数据库失联：全节点摘流与恢复通过
- 滚动重启：3/3 节点通过
- 容量门禁：32 接收者 × 300 消息，ACK p99≤300ms，投递 p99≤500ms

## 容量测试原始输出

```text
=== RUN   TestStandaloneWebSocketHotTopic
    message_lifecycle_test.go:322: 真实热点 Topic 完成：订阅者=32，消息=300，网络投递=9600，总耗时=1.709399875s，ACK p95=9.902875ms，ACK p99=12.840125ms，ACK max=22.214083ms，投递 p95=10.013916ms，投递 p99=12.925708ms，投递 max=22.501541ms
--- PASS: TestStandaloneWebSocketHotTopic (3.76s)
=== RUN   TestStandaloneWebSocketReconnectBurst
    protocol_consistency_test.go:206: 重连突发完成：连接=256，总耗时=31.843916ms，p95=30.825667ms，最大=31.682ms
--- PASS: TestStandaloneWebSocketReconnectBurst (0.03s)
PASS
ok  	chat/tests/standalone	4.511s
```
