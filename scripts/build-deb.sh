#!/usr/bin/env bash
# Build a .deb package for linux/amd64.
#
# Runs everything inside Docker, so the only host dependency is Docker itself.
# The binary is compiled in a golang container and the .deb is then produced
# by nfpm (https://nfpm.goreleaser.com/). Output lands in ./dist/.
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"

# nfpm rejects leading "v" and "-dirty" suffixes in version fields.
DEB_VERSION="${VERSION#v}"
DEB_VERSION="${DEB_VERSION%-dirty}"

mkdir -p bin dist

echo ">> compiling pg-rds-proxy for linux/amd64 (version=${VERSION})"
docker run --rm \
  -v "$PWD":/src -w /src \
  -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=amd64 \
  golang:1.22-alpine \
  sh -c "apk add --no-cache git >/dev/null && \
         go build -trimpath \
           -ldflags \"-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}\" \
           -o bin/pg-rds-proxy.linux-amd64 ./cmd/pg-rds-proxy"

echo ">> packaging .deb with nfpm (version=${DEB_VERSION})"
docker run --rm \
  -v "$PWD":/work -w /work \
  -e VERSION="${DEB_VERSION}" \
  goreleaser/nfpm:latest \
  package \
    --config packaging/nfpm.yaml \
    --packager deb \
    --target dist/

echo ">> built:"
ls -1 dist/*.deb
