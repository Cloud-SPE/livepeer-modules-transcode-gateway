# syntax=docker/dockerfile:1
#
# Build the gateway binary in a minimal stage, then ship a tiny
# runtime image.

ARG GO_VERSION=1.25.7

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /workspace

COPY gateway/ /workspace/gateway/
COPY proto/ /workspace/proto/

WORKDIR /workspace/gateway
RUN go mod download
RUN CGO_ENABLED=0 go build -o /out/gateway ./cmd/gateway

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/gateway /usr/local/bin/gateway
COPY gateway/migrations/ /usr/local/share/gateway/migrations/
ENV MIGRATIONS_DIR=/usr/local/share/gateway/migrations
ENTRYPOINT ["/usr/local/bin/gateway"]
