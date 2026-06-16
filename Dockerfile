# syntax=docker/dockerfile:1
#
# Production image for the middleman maintainer console.
#
# Multi-stage:
#   1. frontend — build the embedded Svelte SPA + GitHub App UI with Bun/Vite+
#   2. build    — compile the Go server (pure Go, no CGO) with the dist embedded
#   3. runtime  — minimal Debian slim with just the binary + curl for healthcheck
#
# Build:  docker build -t ghcr.io/kenn-io/middleman:latest .
# Run:    docker run -p 8091:8091 -v middleman-data:/data ghcr.io/kenn-io/middleman:latest

# ---- Stage 1: frontends -----------------------------------------------------
FROM node:24-bookworm AS frontend
WORKDIR /app

# Bun for workspace dependency installation (pinned to match frontend tooling),
# plus make to drive the project's own frontend build targets.
ARG BUN_VERSION=1.3.14
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends make unzip curl ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && case "${TARGETARCH:-amd64}" in \
      amd64) BUN_ARCH=x64 ;; \
      arm64) BUN_ARCH=aarch64 ;; \
      *) echo "unsupported arch: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && curl -fsSL -o /tmp/bun.zip "https://github.com/oven-sh/bun/releases/download/bun-v${BUN_VERSION}/bun-linux-${BUN_ARCH}.zip" \
 && unzip -q /tmp/bun.zip -d /tmp/bun \
 && mv /tmp/bun/bun-linux-${BUN_ARCH}/bun /usr/local/bin/bun \
 && chmod +x /usr/local/bin/bun \
 && rm -rf /tmp/bun /tmp/bun.zip

COPY . .
# `make frontend` / `githubapp-frontend` run `bun install`, build with Vite+, and
# copy dist into internal/web/dist and internal/githubapp/ui/dist (the go:embed dirs).
RUN make frontend githubapp-frontend

# ---- Stage 2: Go build ------------------------------------------------------
FROM golang:1.26.3-bookworm AS build
WORKDIR /src

ARG VERSION=docker
ARG COMMIT=unknown
ARG BUILD_DATE=

COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Bring in the built frontends (overwrite any stub/empty dist in the context).
COPY --from=frontend /app/internal/web/dist ./internal/web/dist
COPY --from=frontend /app/internal/githubapp/ui/dist ./internal/githubapp/ui/dist

# Pure Go (modernc.org/sqlite) — no CGO. -buildvcs=false so a missing .git is fine.
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
      -o /out/middleman ./cmd/middleman

# ---- Stage 3: runtime -------------------------------------------------------
FROM debian:bookworm-slim
# socat bridges 0.0.0.0:<port> -> middleman's loopback listener (it binds
# loopback-only by design); curl is for the healthcheck.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl socat \
 && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/middleman /usr/local/bin/middleman
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# Config + SQLite live under MIDDLEMAN_HOME so they persist on the volume.
# MIDDLEMAN_PORT is the external (socat) port; middleman itself binds
# MIDDLEMAN_INTERNAL_PORT on loopback inside the container.
ENV MIDDLEMAN_HOME=/data
ENV MIDDLEMAN_PORT=8091
ENV MIDDLEMAN_INTERNAL_PORT=8092
EXPOSE 8091
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=5 \
  CMD curl -fsS "http://127.0.0.1:${MIDDLEMAN_PORT:-8091}/healthz" || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
