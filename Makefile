VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG     := github.com/shuricksumy/tunnelward/internal/version
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)

.PHONY: build test lint fmt clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/tunnelward ./cmd/tunnelward

test:
	go test -race -count=1 ./...

lint:
	golangci-lint run

fmt:
	gofmt -s -w .

clean:
	rm -rf bin dist
