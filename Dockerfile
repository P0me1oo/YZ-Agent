# 构建工具链、功能标签和来源提交与 Release 保持一致。
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG NODE_VERSION
ARG SOURCE_COMMIT

RUN apk add --no-cache git

WORKDIR /build

COPY go.mod go.sum ./
COPY compat/sing-anytls/go.mod ./compat/sing-anytls/go.mod
COPY compat/sing-shadowsocks/go.mod ./compat/sing-shadowsocks/go.mod
RUN go mod download

COPY . .

RUN test -n "$NODE_VERSION" && \
    test "$(git rev-parse HEAD)" = "$SOURCE_COMMIT" && \
    test -z "$(git status --porcelain)" && \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -mod=readonly -trimpath -buildvcs=true -ldflags "-s -w \
    -X main.version=$NODE_VERSION -X main.commit=$SOURCE_COMMIT \
    -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -tags "with_quic with_utls with_wireguard with_acme with_clash_api" \
    -o /out/yz-agent ./cmd/yz-agent && \
    go version -m /out/yz-agent | grep -F 'vcs.modified=false'

# Runtime stage — sing-box & xray-core are embedded as Go libraries
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /out/yz-agent /usr/local/bin/yz-agent

RUN mkdir -p /etc/yz-agent

WORKDIR /etc/yz-agent

# Config can be provided via file mount OR environment variables.
# Env var mode (no config file needed):
#   docker run -d --network=host --stop-timeout=150 \
#     -e apiHost=https://panel.example.com \
#     -e apiKey=YOUR_TOKEN \
#     -e nodeID=1 \
#     ghcr.io/p0me1oo/yz-agent:v1.14.0
#
# Supported env vars:
#   apiHost  / API_HOST    → panel URL
#   apiKey   / API_KEY     → server token
#   nodeID   / NODE_ID     → node ID
#   nodeType / NODE_TYPE   → node type (optional)
#   kernel   / KERNEL_TYPE → 新建建议显式设置 singbox，VLESS 设置 xray；历史省略值仍为 xray
#   domain   / DOMAIN      → TLS domain (enables auto_tls)
#   certFile / CERT_FILE   → TLS cert path
#   keyFile  / KEY_FILE    → TLS key path
#   logLevel / LOG_LEVEL   → log level

ENTRYPOINT ["yz-agent"]
CMD ["run", "-c", "/etc/yz-agent/config.yml"]
