# syntax=docker/dockerfile:1.7

# ---- Stage 1: build ----
FROM golang:1.27.1-alpine AS builder

RUN apk add --no-cache ca-certificates git

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/relayer \
        ./cmd/relayer

# ---- Stage 2: runtime ----
FROM alpine:3.24

RUN apk add --no-cache ca-certificates tzdata curl \
    && addgroup -S relayer \
    && adduser -S -G relayer -u 65532 relayer

WORKDIR /app

COPY --from=builder /out/relayer /app/relayer
# Ship the example config as the default so `docker run` has something to load.
# Real deployments override this via a volume mount (see infra/docker/docker-compose.yml).
# The live configs/config.yaml is gitignored and not available at build time in CI.
COPY configs/config.example.yaml /app/configs/config.yaml

USER 65532:65532

EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8080/health || exit 1

ENV CONFIG_PATH=/app/configs/config.yaml

ENTRYPOINT ["/app/relayer"]
