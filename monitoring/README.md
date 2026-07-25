# IM 监控支持服务 (`monitoring`)

本目录包含 IM 服务端的性能指标收集与服务监控工具模块。

## 目录结构

- **[exporter/](./exporter/README.md)**：包含原生 Go 语言实现的 `IM Metric Exporter` 导出器源码与使用指南，支持将 IM 运行指标实时暴露给 Prometheus 拉取，或定时推送至 InfluxDB 1.7 / 2.0 监控系统中。
