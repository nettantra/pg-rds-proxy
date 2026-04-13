BINARY    := pg-rds-proxy
PKG       := ./cmd/pg-rds-proxy
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
IMAGE     ?= pg-rds-proxy
IMAGE_TAG ?= $(VERSION)
LDFLAGS   := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: all build build-linux test tidy docker deb clean

all: build

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) $(PKG)

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY).linux-amd64 $(PKG)

test:
	go test ./...

tidy:
	go mod tidy

docker:
	VERSION=$(VERSION) COMMIT=$(COMMIT) IMAGE=$(IMAGE) scripts/build-docker.sh

deb:
	VERSION=$(VERSION) COMMIT=$(COMMIT) scripts/build-deb.sh

clean:
	rm -rf bin dist
