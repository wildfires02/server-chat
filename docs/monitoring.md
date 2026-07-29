# 监控与健康检查

本文说明服务端健康端点、运行指标和监控导出器。生产集群的告警阈值与故障处理
还应结合[生产集群操作手册](cluster-operations.md)。

## 1. 健康端点

默认配置提供：

| 端点 | 用途 | 成功响应 |
| --- | --- | --- |
| `/livez` | 判断进程是否存活 | HTTP 200 |
| `/readyz` | 判断节点是否可以接收新流量和写请求 | HTTP 200 |
| `/drainz` | 从本机触发节点排空 | HTTP 200 |
| `/clusterz` | 从本机提交集群拓扑变更 | HTTP 200 |

`/drainz` 和 `/clusterz` 是管理接口，只允许回环地址或受控运维通道访问，不得
暴露到公网或普通负载均衡入口。

单机检查：

```bash
curl --fail http://127.0.0.1:6060/livez
curl --fail http://127.0.0.1:6060/readyz
```

集群环境只有在数据库、etcd 租约、成员视图、路由环、节点间通道和队列水位
均满足条件时才会进入就绪状态。

## 2. 运行指标

`expvar` 配置项决定 JSON 指标端点：

```yaml
expvar: /debug/vars
```

空字符串或 `-` 表示关闭。命令行参数可以覆盖配置。默认开发配置通过以下地址
提供指标：

```bash
curl --fail http://127.0.0.1:6060/debug/vars
```

常用指标包括：

- `Uptime`、`NumGoroutines` 和 `memstats`。
- `LiveSessions`、`TotalSessions`。
- `LiveTopics`、`TotalTopics`。
- 数据库连接池统计 `DbStats`。
- 集群节点、成员视图、可靠通道和队列相关指标。
- 请求延迟、消息大小等直方图。

实际指标集合以当前进程的 `/debug/vars` 输出为准；新增或删除指标时必须同步
更新告警和导出器配置。

## 3. Prometheus 与 InfluxDB

独立导出器读取服务端 JSON 指标，并转换为 Prometheus 拉取格式或推送到
InfluxDB。完整参数见[监控导出器](../cmd/exporter/README.md)。

构建：

```bash
go build -o bin/exporter ./cmd/exporter
```

Prometheus 示例：

```bash
./bin/exporter \
  --serve_for=prometheus \
  --im_addr=http://127.0.0.1:6060/debug/vars \
  --listen_at=:6222
```

随后检查：

```bash
curl --fail http://127.0.0.1:6222/metrics
```

每个服务端实例应由独立导出器采集，避免一个导出器地址掩盖节点级异常。

## 4. 最小告警集合

生产环境至少监控：

- `/readyz` 连续失败和可用节点低于多数派。
- 数据库连接失败、连接池耗尽和请求延迟异常。
- etcd 租约丢失、成员视图不一致和隔离栅栏拒绝。
- 节点间可靠队列接近容量、重试耗尽和通道持续断开。
- 活跃会话、活跃 Topic、协程数和内存持续异常增长。
- 发布、持久化、投递和客户端写入延迟超出服务目标。

告警必须关联运行手册，不能只配置阈值而缺少恢复步骤。

## 5. 安全要求

- 指标端点可能包含命令行、运行时和数据库统计，不应直接暴露到公网。
- 生产环境通过内网、服务网格或受控抓取网络访问。
- 日志和指标中不得记录认证秘密、消息正文、上传文件名或对象存储位置。
- 监控系统中的访问令牌和数据库凭据必须由 Secret 管理。
