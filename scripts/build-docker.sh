#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

IMAGE="${IMAGE:-pg-rds-proxy}"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
PLATFORM="${PLATFORM:-linux/amd64}"

echo ">> building OCI image ${IMAGE}:${VERSION} for ${PLATFORM}"
docker buildx build \
  --platform "${PLATFORM}" \
  --build-arg VERSION="${VERSION}" \
  --build-arg COMMIT="${COMMIT}" \
  -t "${IMAGE}:${VERSION}" \
  -t "${IMAGE}:latest" \
  --load \
  .

echo ">> built ${IMAGE}:${VERSION}"
