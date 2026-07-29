# syntax=docker/dockerfile:1.7

# IM 服务端采用可复现的多阶段构建；基础镜像固定到 Go 与 Alpine 小版本，
# 发布流水线可继续使用 --build-arg 覆盖为企业镜像或完整 digest。
ARG GO_IMAGE=golang:1.26.5-alpine3.23
ARG RUNTIME_IMAGE=alpine:3.23.5

FROM ${GO_IMAGE} AS builder

ARG TARGET_DB=mysql
ARG VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# alldbs 只影响编译标签，不会成为运行时适配器名称。
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    case "${TARGET_DB}" in \
      mysql|postgres|mongodb|rethinkdb) build_tags="${TARGET_DB}" ;; \
      alldbs) build_tags="mysql postgres mongodb rethinkdb" ;; \
      *) echo "不支持的 TARGET_DB=${TARGET_DB}" >&2; exit 1 ;; \
    esac && \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X chat/internal/server.buildstamp=${VERSION}" \
      -tags "${build_tags}" \
      -o /out/im-server ./cmd/im-server && \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w" \
      -tags "${build_tags}" \
      -o /out/init-db ./cmd/init-db

FROM ${RUNTIME_IMAGE} AS runtime

ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
ARG TARGET_DB=mysql

LABEL org.opencontainers.image.title="IM Chat Server" \
      org.opencontainers.image.description="IM chat server and database migration utility" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.source="https://github.com/example/im"

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S -g 10001 im && \
    adduser -S -D -H -u 10001 -G im im && \
    install -d -o im -g im \
      /var/lib/im \
      /var/lib/im/.local/uploads \
      /var/lib/im/uploads \
      /opt/im/static \
      /botdata

COPY --from=builder /out/im-server /usr/local/bin/im-server
COPY --from=builder /out/init-db /usr/local/bin/init-db
COPY --chmod=0555 deployments/docker/im/entrypoint.sh /usr/local/bin/im-entrypoint
COPY --chown=im:im configs/im.yaml /etc/im/im.yaml
COPY --chown=im:im configs/im.standalone.yaml /etc/im/im.standalone.yaml
COPY --chown=im:im configs/im.cluster.yaml /etc/im/im.cluster.yaml
COPY --chown=im:im cmd/init-db/data.json /opt/im/data.json
COPY --chown=im:im web/static/ /opt/im/static/

ENV TARGET_DB="${TARGET_DB}" \
    IM_CONFIG_FILE=/etc/im/im.standalone.yaml \
    IM_STATIC_DIR=/opt/im/static \
    IM_DB_INIT_MODE=skip \
    IM_DB_SAMPLE_DATA= \
    IM_DB_WAIT_FOR= \
    IM_DB_WAIT_TIMEOUT=120 \
    IM_RUN_SERVER=true \
    IM_VALIDATE_CONFIG=true

WORKDIR /var/lib/im
USER 10001:10001

EXPOSE 6060 16060 12000

HEALTHCHECK --interval=30s --timeout=3s --start-period=30s --retries=3 \
  CMD wget -q --spider http://127.0.0.1:6060/livez || exit 1

ENTRYPOINT ["/usr/local/bin/im-entrypoint"]
