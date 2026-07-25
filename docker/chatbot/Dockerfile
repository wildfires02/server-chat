# 用于从本地 Go 源码构建 IM 聊天机器人 (Chatbot) 的多阶段 Dockerfile

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

# 编译 Go 版本的 chatbot 静态二进制文件
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/chatbot ./chatbot

# 第二阶段：最终运行镜像 (Stage 2: Runtime)
FROM alpine:latest

# 构建参数与环境变量
ARG VERSION=0.25
ARG LOGIN_AS=
ARG IM_HOST=im-srv:16060
ENV VERSION=$VERSION
ARG BINVERS=$VERSION

LABEL maintainer="IM Team <info@im.co>"
LABEL name="IMChatbot"
LABEL version=$VERSION

# 安装运行基础依赖
RUN apk add --no-cache ca-certificates bash

# 设定机器人运行工作目录
WORKDIR /usr/src/bot

# 从构建阶段复制二进制文件并复制本地 quotes 语料文本
COPY --from=builder /out/chatbot .
COPY chatbot/quotes.txt .

RUN chmod +x chatbot

# 健康检查：检查 gRPC 插件端口 40051 是否可连通
HEALTHCHECK --interval=1m --timeout=3s --start-period=15s \
  CMD nc -z localhost 40051 || exit 1

# 启动聊天机器人的命令行指令
CMD ["/bin/sh", "-c", "./chatbot -login-basic=${LOGIN_AS} -login-cookie=/botdata/.tn-cookie -host=${IM_HOST} -quotes=quotes.txt >> /var/log/chatbot.log 2>&1"]

# 暴露 gRPC 插件服务端口
EXPOSE 40051
