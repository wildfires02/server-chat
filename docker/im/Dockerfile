# 用于从本地 Go 源码构建 IM 聊天服务器主程序的多阶段 Dockerfile

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

# 编译目标数据库标记参数 (mysql, postgres, mongodb, rethinkdb 或 alldbs)
ARG TARGET_DB=mysql

# 根据数据库标记编译 server 和 init-db 可执行文件
RUN if [ "$TARGET_DB" = "alldbs" ]; then \
		BUILDTAGS="mysql postgres mongodb rethinkdb"; \
	else \
		BUILDTAGS="$TARGET_DB"; \
	fi && \
	CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -tags "$BUILDTAGS" -o /out/im ./server && \
	CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -tags "$BUILDTAGS" -o /out/init-db ./init-db

# 第二阶段：最终运行镜像 (Stage 2: Runtime)
FROM alpine:latest

# 构建版本信息与元数据定义
ARG VERSION=0.25
ENV VERSION=$VERSION
ARG BINVERS=$VERSION

LABEL maintainer="IM Team <info@im.co>"
LABEL name="IMChatServer"
LABEL version=$VERSION

# 数据库选择构建参数
ARG TARGET_DB=mysql
ENV TARGET_DB=$TARGET_DB

# 运行期控制参数：等待服务连通、重置/升级数据库、跳过数据库初始化等
ENV WAIT_FOR=
ENV RESET_DB=false
ENV UPGRADE_DB=false
ENV NO_DB_INIT=false

# 初始化数据源文件
ARG SAMPLE_DATA=data.json
ENV SAMPLE_DATA=$SAMPLE_DATA

# 国家代码及数据库连接 DSN 参数
ENV DEFAULT_COUNTRY_CODE=CN
ENV MYSQL_DSN='root@tcp(mysql)/im?parseTime=true&collation=utf8mb4_0900_ai_ci'
ENV POSTGRES_DSN='postgresql://postgres:postgres@postgres:5432/im?sslmode=disable&connect_timeout=10'
ENV PLUGIN_PYTHON_CHAT_BOT_ENABLED=false
ENV MEDIA_HANDLER=fs
ENV FS_CORS_ORIGINS='["*"]'
ENV AWS_CORS_ORIGINS='["*"]'
ENV AWS_ACCESS_KEY_ID=
ENV AWS_SECRET_ACCESS_KEY=
ENV AWS_REGION=
ENV AWS_S3_BUCKET=
ENV AWS_S3_ENDPOINT=
ENV SMTP_HOST_URL='http://localhost:6060'
ENV SMTP_SERVER=
ENV SMTP_PORT=
ENV SMTP_SENDER=
ENV SMTP_LOGIN=
ENV SMTP_PASSWORD=
ENV SMTP_AUTH_MECHANISM=
ENV SMTP_HELO_HOST=
ENV EMAIL_VERIFICATION_REQUIRED=
ENV DEBUG_EMAIL_VERIFICATION_CODE=
ENV SMTP_DOMAINS=''

# 密钥与盐值设置
ENV API_KEY_SALT=T713/rYYgW7g4m3vG6zGRh7+FM1t0T8j13koXScOAj4=
ENV AUTH_TOKEN_KEY=wfaY2RgF2S1OQI/ZlK+LSrp1KB2jwAdGAIHQ7JZn+Kc=
ENV UID_ENCRYPTION_KEY=la6YsO+bNX/+XIkOqc5Svw==

# HTTPS / TLS 设置
ENV TLS_ENABLED=false
ENV TLS_DOMAIN_NAME=
ENV TLS_CONTACT_ADDRESS=

# FCM 推送通知参数
ENV FCM_PUSH_ENABLED=false
ENV FCM_API_KEY=
ENV FCM_APP_ID=
ENV FCM_SENDER_ID=
ENV FCM_PROJECT_ID=
ENV FCM_VAPID_KEY=
ENV FCM_MEASUREMENT_ID=
ENV FCM_INCLUDE_ANDROID_NOTIFICATION=true

# IM 网关推送参数
ENV TNPG_PUSH_ENABLED=false
ENV TNPG_AUTH_TOKEN=
ENV TNPG_ORG=

# WebRTC 视频通话参数
ENV WEBRTC_ENABLED=false
ENV ICE_SERVERS_FILE=

# 存储适配器及性能分析参数
ENV STORE_USE_ADAPTER=$TARGET_DB
ENV SERVER_STATUS_PATH=''
ENV ACC_GC_ENABLED=false

# 安装运行基础依赖包
RUN apk update && \
	apk add --no-cache ca-certificates bash grep

# 设定运行工作目录
WORKDIR /opt/im

# 从第一阶段构建结果中复制二进制可执行文件
COPY --from=builder /out/im .
COPY --from=builder /out/init-db .

# 从本地源码目录复制配置、模板、凭据脚本及 Web 静态资源
COPY server/im.conf .
COPY init-db/data.json .
COPY init-db/credentials.sh .
RUN mkdir -p ./static /botdata
COPY server/stat[i]c ./static/
COPY docker/im/config.template .
COPY docker/im/entrypoint.sh .

# 创建机器人数据存储目录
RUN mkdir -p /botdata

# 赋予可执行权限
RUN chmod +x entrypoint.sh credentials.sh im init-db

# 健康检查：检查 HTTP 6060 端口连通状态
HEALTHCHECK --interval=1m --timeout=3s --start-period=30s \
  CMD nc -z localhost 6060 || exit 1

# 生成配置文件并启动 IM 服务器
ENTRYPOINT ["./entrypoint.sh"]

# HTTP, gRPC, cluster ports
EXPOSE 6060 16060 12000-12003
