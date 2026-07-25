# 用于从本地源码构建 IM 监控指标导出器 (Metrics Exporter) 的多阶段 Dockerfile

# 第一阶段：编译构建阶段 (Stage 1: Builder)
FROM golang:alpine AS builder

# 开启 Go 工具链自动下载匹配版本
ENV GOTOOLCHAIN=auto

WORKDIR /src

# 复制依赖定义文件并下载依赖
COPY go.mod go.sum ./
RUN go mod download

# 复制项目全部源码
COPY . .

# 编译 exporter 静态二进制可执行文件
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/exporter ./monitoring/exporter

# 第二阶段：最终运行镜像 (Stage 2: Runtime)
FROM alpine:latest

# 构建参数与元数据定义
ARG VERSION=0.25
ENV VERSION=$VERSION

LABEL maintainer="IM Team <info@im.co>"
LABEL name="IMMetricExporter"
LABEL version=$VERSION

# 基础控制环境变量：服务类型与等待目标
ENV SERVE_FOR=""
ENV WAIT_FOR=""

# IM 统计指标收集端点及实例名称
ENV IM_ADDR=http://localhost/stats/expvar/
ENV INSTANCE="exporter-instance"

# InfluxDB 数据库配置参数
ENV INFLUXDB_VERSION=1.7
ENV INFLUXDB_ORGANIZATION="org"
ENV INFLUXDB_PUSH_INTERVAL=60
ENV INFLUXDB_PUSH_ADDRESS=""
ENV INFLUXDB_AUTH_TOKEN=""

# Prometheus 监控配置参数
ENV PROM_NAMESPACE="im"
ENV PROM_METRICS_PATH="/metrics"

# 设定运行工作目录
WORKDIR /opt/im

# 安装运行基础依赖（bash）
RUN apk add --no-cache bash

# 从第一阶段构建结果中复制二进制文件及启动入口脚本
COPY --from=builder /out/exporter .
COPY docker/exporter/entrypoint.sh .
RUN chmod +x exporter entrypoint.sh

# 容器启动入口点
ENTRYPOINT ["./entrypoint.sh"]

# 暴露 Exporter Web 服务端口
EXPOSE 6222
