# syntax=docker/dockerfile:1
# One image for all three services (openvpn, agent, web); see docker-compose.yml.

# UI and Go are built on the build machine's platform and cross-compiled,
# so multi-arch builds do not run Node or Go under emulation.
FROM --platform=$BUILDPLATFORM node:22-alpine AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN mkdir -p ../internal/webui/dist && npm run check && npm run build

FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/internal/webui/dist ./internal/webui/dist
ARG TARGETOS TARGETARCH VERSION=dev COMMIT=none DATE=unknown
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/shuricksumy/tunnelward/internal/version.Version=$VERSION -X github.com/shuricksumy/tunnelward/internal/version.Commit=$COMMIT -X github.com/shuricksumy/tunnelward/internal/version.Date=$DATE" \
      -o /out/tunnelward ./cmd/tunnelward

FROM alpine:3.22
# openvpn: the server itself, and `openvpn --genkey` for tls-crypt-v2 client keys (agent)
# iptables: NAT for VPN clients (openvpn service only)
RUN apk add --no-cache openvpn iptables ca-certificates \
 && addgroup -g 10001 tunnelward \
 && adduser -D -H -u 10001 -G tunnelward tunnelward \
 && mkdir -p /data /run/tunnelward \
 && chown tunnelward:tunnelward /data
COPY --from=go /out/tunnelward /usr/local/bin/tunnelward
COPY docker/openvpn-entrypoint.sh /usr/local/bin/openvpn-entrypoint.sh

ENV TW_DB_PATH=/data/tunnelward.db \
    TW_WEB_LISTEN=:8080
USER tunnelward
EXPOSE 8080
ENTRYPOINT ["tunnelward"]
CMD ["web"]

LABEL org.opencontainers.image.source="https://github.com/shuricksumy/tunnelward" \
      org.opencontainers.image.description="Web management for an OpenVPN server" \
      org.opencontainers.image.licenses="MIT"
