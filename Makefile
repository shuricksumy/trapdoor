VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG     := github.com/shuricksumy/trapdoor/internal/version
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)

.PHONY: all ui build test lint fmt clean

all: ui build

# Builds web/ into internal/webui/dist, which `build` embeds.
ui:
	cd web && npm ci && npm run check && npm run build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/trapdoor ./cmd/trapdoor

test:
	go test -race -count=1 ./...

lint:
	golangci-lint run

fmt:
	gofmt -s -w .

clean:
	rm -rf bin dist
	find internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
