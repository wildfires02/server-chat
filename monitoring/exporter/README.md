# IM 监控指标导出器 (`IM Metric Exporter`)

`Exporter` 是用于收集并转化 IM 服务端监控指标的独立导出器程序。它通过 HTTP 抓取 IM 使用 Go [expvar](https://golang.org/pkg/expvar/) 暴露的 JSON 格式实时监控数据，并将其转化为其它主流监控平台支持的数据格式。

目前支持以下两种导出模式：

1. **[Prometheus](https://prometheus.io/) 导出模式**：提供标准的 Prometheus 数据格式端点（默认 `/metrics`），由 Prometheus 监控服务定时进行**拉取（Pull / Scrape）**。
2. **[InfluxDB](https://www.influxdata.com/) 导出模式**：定时将收集到的时序指标数据直接**推送（Push）** 到指定的 InfluxDB 服务端。

---

## 部署与使用建议

Exporter 建议与 IM 服务端一对一独立部署（在相同机器或 Docker 容器侧车容器中）：每个 Exporter 专职采集一台 IM 实例的运行指标。

```bash
# 编译可执行文件
cd monitoring/exporter
go build -o exporter .
```

---

## 启动参数标志说明

命令行标志用于指定运行模式与目标服务参数：

### 1. 通用参数标志

| 参数标志 | 类型 | 默认值 | 作用与说明 |
| :--- | :--- | :--- | :--- |
| `--serve_for` | string | `influxdb` | 目标监控系统类型，可选：`prometheus` 或 `influxdb` |
| `--im_addr` | string | `http://localhost:6060/stats/expvar` | 待抓取的 IM 实例 expvar 监控指标 HTTP 地址 |
| `--listen_at` | string | `:6222` | Exporter 服务监听的主机与端口 |
| `--instance` | string | `exporter` | 当前 Exporter 实例名称（用于区分多节点标签） |
| `--metric_list` | string | `Version,LiveTopics,TotalTopics,LiveSessions,ClusterLeader,TotalClusterNodes,LiveClusterNodes,memstats.Alloc` | 导出的常用数值指标列表（逗号分隔） |
| `--histo_metric_list` | string | `RequestLatency,OutgoingMessageSize` | 导出的直方图指标列表（逗号分隔） |

---

### 2. Prometheus 模式专用参数

| 参数标志 | 类型 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `--prom_namespace` | string | `im` | 指标名称前缀，例如 `<namespace>_topics_live_count` |
| `--prom_metrics_path` | string | `/metrics` | Prometheus 拉取指标的 HTTP 路径 |
| `--prom_timeout` | int | `15` | 抓取 HTTP 连接超时时间（单位：秒） |

#### 运行范例 (Prometheus 模式)

```bash
./exporter \
    --serve_for=prometheus \
    --im_addr=http://localhost:6060/stats/expvar \
    --listen_at=:6222 \
    --prom_namespace=im \
    --prom_metrics_path=/metrics
```

服务启动后，指标将在 `http://localhost:6222/metrics` 开放拉取。可在 Prometheus 配置文件 `prometheus.yml` 中添加该 Target 节点。

---

### 3. InfluxDB 模式专用参数

| 参数标志 | 类型 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `--influx_push_addr` | string | `http://localhost:9999/write` | InfluxDB 数据写入的目标 HTTP 端点 |
| `--influx_db_version` | string | `1.7` | InfluxDB 版本（仅支持 `1.7` 和 `2.0`） |
| `--influx_organization` | string | `test` | InfluxDB 组织名称 (Organization) |
| `--influx_bucket` | string | `test` | InfluxDB 2.0 存储桶名称 (Bucket) |
| `--influx_auth_token` | string | `""` | InfluxDB 认证 Token 密钥 |
| `--influx_push_interval` | int | `30` | 自动推送的时间间隔（单位：秒，最小为 10 秒） |

#### 运行范例 (InfluxDB 模式)

```bash
./exporter \
    --serve_for=influxdb \
    --im_addr=http://localhost:6060/stats/expvar \
    --listen_at=:6222 \
    --influx_push_addr=http://my-influxdb-backend:8086/write \
    --influx_db_version=2.0 \
    --influx_organization=myOrg \
    --influx_bucket=im_metrics \
    --influx_auth_token=myToken123 \
    --influx_push_interval=30
```

Exporter 将自动每隔 30 秒将捕获到的性能指标推送到指定的 InfluxDB 存储库中。此外也可以通过访问 `http://localhost:6222/push` 强制触发一次立即推送。
