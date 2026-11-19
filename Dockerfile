# syntax=docker/dockerfile:1
# One image for all three services (vpn, agent, web); see docker-compose.yml.

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
      -ldflags "-s -w -X github.com/shuricksumy/trapdoor/internal/version.Version=$VERSION -X github.com/shuricksumy/trapdoor/internal/version.Commit=$COMMIT -X github.com/shuricksumy/trapdoor/internal/version.Date=$DATE" \
      -o /out/trapdoor ./cmd/trapdoor

# AmneziaWG (WireGuard with obfuscation) is not packaged for Alpine: the
# userspace implementation and its tools are built at pinned releases, so
# the host needs no kernel module.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS awg-go
ARG TARGETOS TARGETARCH
# Renovate opens a PR (built and smoke-tested by CI) for each new release.
# renovate: datasource=github-tags depName=amnezia-vpn/amneziawg-go
ARG AMNEZIAWG_GO_VERSION=v3.1.20261116
RUN apk add --no-cache git \
 && git clone --depth 1 --branch "$AMNEZIAWG_GO_VERSION" https://github.com/amnezia-vpn/amneziawg-go /src
WORKDIR /src
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/amneziawg-go .

FROM alpine:3.24 AS awg-tools
# renovate: datasource=github-tags depName=amnezia-vpn/amneziawg-tools
ARG AMNEZIAWG_TOOLS_VERSION=v3.1.20260812
RUN apk add --no-cache git make gcc musl-dev linux-headers bash \
 && git clone --depth 1 --branch "$AMNEZIAWG_TOOLS_VERSION" https://github.com/amnezia-vpn/amneziawg-tools /src
WORKDIR /src/src
RUN make && make install DESTDIR=/out WITH_WGQUICK=yes WITH_BASHCOMPLETION=no WITH_SYSTEMDUNITS=no

FROM alpine:3.24
# openvpn: the server itself, and `openvpn --genkey` for tls-crypt-v2 client keys (agent)
# iptables: NAT for VPN clients (vpn service only)
# wireguard-tools: wg / wg-quick for WireGuard servers (the kernel module comes from the host;
# also pulls in bash, which awg-quick needs too)
RUN apk add --no-cache openvpn iptables ca-certificates wireguard-tools \
 && addgroup -g 10001 trapdoor \
 && adduser -D -H -u 10001 -G trapdoor trapdoor \
 && mkdir -p /data /run/trapdoor \
 && chown trapdoor:trapdoor /data
COPY --from=go /out/trapdoor /usr/local/bin/trapdoor
COPY --from=awg-go /out/amneziawg-go /usr/local/bin/amneziawg-go
COPY --from=awg-tools /out/usr/bin/awg /out/usr/bin/awg-quick /usr/local/bin/
COPY docker/vpn-entrypoint.sh /usr/local/bin/vpn-entrypoint.sh
# The old name, for compose files from before the "vpn" service.
RUN ln -s vpn-entrypoint.sh /usr/local/bin/openvpn-entrypoint.sh

ENV TD_DB_PATH=/data/trapdoor.db \
    TD_WEB_LISTEN=:8080
USER trapdoor
EXPOSE 8080
ENTRYPOINT ["trapdoor"]
CMD ["web"]

LABEL org.opencontainers.image.source="https://github.com/shuricksumy/trapdoor" \
      org.opencontainers.image.description="Web management for OpenVPN, WireGuard and AmneziaWG servers" \
      org.opencontainers.image.licenses="MIT"
