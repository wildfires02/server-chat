# IM 服务端性能与监控指南

IM 服务端可选择通过可配置的 HTTP(S) 端点将实时运行统计指标暴露为 JSON 文档。

可通过在配置文件 `im.conf` 中添加字符串参数 `expvar` 来启用该功能。`expvar` 的值为服务监控变量暴露的 URL 路径。此外，也可以在命令行中添加 `--expvar` 参数来开启。若 `expvar` 的值为空字符串 `""` 或破折号 `"-"`，则禁用此监控功能。非空的命令行参数会覆盖配置文件中的设置。

默认配置文件中已启用该功能，默认在 `/debug/vars` 端点暴露监控数据。

服务端会实时发布以下核心监控指标：

* `memstats`：Go 语言运行时的内存统计数据。
* `cmdline`：服务端启动时的命令行参数数组。
* `TotalSessions`：服务端自启动以来创建的所有 Session 会话总数。
* `LiveSessions`：当前活跃的 Session 会话数量（无论是否已完成登录认证）。
* `TotalTopics`：服务端自启动以来激活过的所有 Topic 主题总数。
* `LiveTopics`：当前活跃的 Topic 主题数量。

如需将监控指标导入 Prometheus 或 InfluxDB，请参阅 [Exporter 导出器组件](../monitoring/exporter/README.md)。
