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
      -o /out/chatbot ./cmd/chatbot

FROM ${RUNTIME_IMAGE} AS runtime

ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown

LABEL org.opencontainers.image.title="IM Chatbot" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"

RUN apk add --no-cache ca-certificates && \
    addgroup -S -g 10001 im && \
    adduser -S -D -H -u 10001 -G im im && \
    install -d -o im -g im /var/lib/im-bot /botdata

COPY --from=builder /out/chatbot /usr/local/bin/chatbot
COPY --chown=im:im cmd/chatbot/quotes.txt /var/lib/im-bot/quotes.txt

ENV LOGIN_AS= \
    IM_HOST=im-server:16060

WORKDIR /var/lib/im-bot
USER 10001:10001

EXPOSE 40051
HEALTHCHECK --interval=30s --timeout=3s --start-period=15s --retries=3 \
  CMD nc -z -w 2 127.0.0.1 40051 || exit 1

CMD ["/bin/sh", "-ec", "exec /usr/local/bin/chatbot -login-basic=\"${LOGIN_AS}\" -login-cookie=/botdata/.tn-cookie -host=\"${IM_HOST}\" -quotes=/var/lib/im-bot/quotes.txt"]
