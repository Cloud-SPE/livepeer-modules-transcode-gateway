# syntax=docker/dockerfile:1
#
# Build the gateway binary with the three SPAs (web/site, web/portal,
# web/admin) embedded into it via //go:embed. Final image is alpine +
# the static binary + the migrations — production serves everything
# from one port out of this one binary.

ARG GO_VERSION=1.25.7

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /workspace

COPY gateway/ /workspace/gateway/
COPY proto/   /workspace/proto/
COPY web/     /workspace/web/

# Populate the embed source directory before `go build` so the
# //go:embed all:webroot directive in gateway/internal/server/embed.go
# bakes the SPAs into the binary. Mirrors `make embed-webroot`.
RUN mkdir -p /workspace/gateway/internal/server/webroot \
 && rm -rf /workspace/gateway/internal/server/webroot/site \
           /workspace/gateway/internal/server/webroot/portal \
           /workspace/gateway/internal/server/webroot/admin \
 && cp -r /workspace/web/site   /workspace/gateway/internal/server/webroot/site \
 && cp -r /workspace/web/portal /workspace/gateway/internal/server/webroot/portal \
 && cp -r /workspace/web/admin  /workspace/gateway/internal/server/webroot/admin \
 && rm -f /workspace/gateway/internal/server/webroot/site/dev-server.js \
          /workspace/gateway/internal/server/webroot/site/package.json \
          /workspace/gateway/internal/server/webroot/portal/dev-server.js \
          /workspace/gateway/internal/server/webroot/portal/package.json \
          /workspace/gateway/internal/server/webroot/admin/dev-server.js \
          /workspace/gateway/internal/server/webroot/admin/package.json

WORKDIR /workspace/gateway
RUN go mod download
RUN CGO_ENABLED=0 go build -o /out/gateway ./cmd/gateway

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/gateway /usr/local/bin/gateway
COPY gateway/migrations/ /usr/local/share/gateway/migrations/
ENV MIGRATIONS_DIR=/usr/local/share/gateway/migrations
ENTRYPOINT ["/usr/local/bin/gateway"]
