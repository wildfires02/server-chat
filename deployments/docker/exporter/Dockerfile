# syntax=docker/dockerfile:1.7

ARG GO_IMAGE=golang:1.26.5-alpine3.23
ARG RUNTIME_IMAGE=alpine:3.23.5

FROM ${GO_IMAGE} AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/exporter ./cmd/exporter

FROM ${RUNTIME_IMAGE} AS runtime

ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown

LABEL org.opencontainers.image.title="IM Metrics Exporter" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"

RUN apk add --no-cache ca-certificates && \
    addgroup -S -g 10001 im && \
    adduser -S -D -H -u 10001 -G im im

COPY --from=builder /out/exporter /usr/local/bin/im-exporter
COPY --chmod=0555 deployments/docker/exporter/entrypoint.sh /usr/local/bin/exporter-entrypoint

ENV SERVE_FOR=prometheus \
    IM_ADDR=http://im-server:6060/debug/vars \
    INSTANCE=im-server \
    PROM_NAMESPACE=im \
    PROM_METRICS_PATH=/metrics \
    WAIT_FOR= \
    WAIT_TIMEOUT=120

USER 10001:10001
EXPOSE 6222
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -q --spider http://127.0.0.1:6222/metrics || exit 1

ENTRYPOINT ["/usr/local/bin/exporter-entrypoint"]
