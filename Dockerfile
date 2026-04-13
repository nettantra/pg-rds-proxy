# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build
WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/pg-rds-proxy ./cmd/pg-rds-proxy

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pg-rds-proxy /usr/local/bin/pg-rds-proxy
USER nonroot:nonroot
EXPOSE 5532
ENTRYPOINT ["/usr/local/bin/pg-rds-proxy"]
