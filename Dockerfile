# syntax=docker/dockerfile:1
#
# Three stages: build the web interface, build the Go binary with that interface
# embedded, then a small Debian runtime. Base images are pinned to versions;
# see docs/operations.md for how to pin digests as well.

ARG NODE_IMAGE=node:22.14.0-bookworm-slim
ARG GO_IMAGE=golang:1.27.0-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim

# ---- Web interface ----
FROM ${NODE_IMAGE} AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- Go binary (the interface is copied in before compiling, because it is embedded) ----
FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY --from=web /src/web/dist ./internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/proxyward ./cmd/proxyward

# ---- Runtime ----
FROM ${RUNTIME_IMAGE}
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --system --gid 10001 proxyward \
 && useradd --system --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin proxyward \
 && mkdir /data \
 && chown 10001:10001 /data \
 && chmod 700 /data
COPY --from=build /out/proxyward /usr/local/bin/proxyward

ENV PROXYWARD_LISTEN=0.0.0.0:8080 \
    PROXYWARD_DATA_DIR=/data
USER 10001:10001
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/proxyward", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/proxyward"]
