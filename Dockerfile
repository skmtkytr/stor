# --- Build stage ---
FROM oven/bun:1 AS ui-builder
WORKDIR /src/ui/frontend
COPY ui/frontend/package.json ui/frontend/bun.lock ./
RUN bun install --frozen-lockfile
COPY ui/frontend/ ./
RUN bun run build

FROM golang:1.26-bookworm AS go-builder
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
COPY --from=ui-builder /src/ui/dist ./ui/dist
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /stor ./cmd/stor

# --- Runtime stage ---
FROM gcr.io/distroless/static-debian12

LABEL org.opencontainers.image.source="https://github.com/skmtkytr/stor"
LABEL org.opencontainers.image.description="BitTorrent client written from scratch in Go"
LABEL org.opencontainers.image.licenses="MIT"

COPY --from=go-builder /stor /usr/local/bin/stor

# Web UI + API
EXPOSE 9090
# BitTorrent peer port
EXPOSE 6881
# DHT (UDP)
EXPOSE 6881/udp

VOLUME ["/data", "/config"]

ENV STOR_DOWNLOAD_DIR=/data
ENV STOR_CONFIG_DIR=/config

ENTRYPOINT ["stor", "daemon", "--dir", "/data", "--config", "/config/config.toml"]
