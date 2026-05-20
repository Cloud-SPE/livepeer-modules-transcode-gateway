#!/usr/bin/env bash
# Regenerate Go gRPC stubs from proto/ into gateway/gen/proto/.
#
# Requires:
#   - protoc       (https://grpc.io/docs/protoc-installation/)
#   - protoc-gen-go        (go install google.golang.org/protobuf/cmd/protoc-gen-go@latest)
#   - protoc-gen-go-grpc   (go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest)

set -euo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
PROTO_DIR="$REPO_ROOT/proto"
OUT_DIR="$REPO_ROOT/gateway/gen/proto"

mkdir -p "$OUT_DIR"

cd "$PROTO_DIR"

find . -name '*.proto' -print0 | while IFS= read -r -d '' f; do
  protoc \
    --go_out="$OUT_DIR" \
    --go_opt=paths=source_relative \
    --go-grpc_out="$OUT_DIR" \
    --go-grpc_opt=paths=source_relative \
    -I "$PROTO_DIR" \
    "$f"
done

echo "proto: regenerated into gateway/gen/proto/"
