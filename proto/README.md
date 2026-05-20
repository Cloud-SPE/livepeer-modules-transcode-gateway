# proto

gRPC service definitions used by the gateway to talk to the Livepeer
daemons.

## Layout

```
proto/
└── livepeer/
    ├── payments/v1/
    │   ├── payer_daemon.proto       # PayerDaemon service (UDS, talks to payment-daemon)
    │   └── types.proto              # shared payment types
    └── registry/v1/
        ├── resolver.proto           # Resolver service (UDS, talks to service-registry-daemon)
        └── types.proto              # shared registry types
```

The protos reference `google/protobuf/empty.proto` and
`google/protobuf/timestamp.proto` which ship with protoc's
well-known-types.

## How the gateway uses these

Generated Go stubs are checked in at `gateway/gen/proto/` so the
gateway builds standalone (no sibling repo required). The proto files
here are the source of truth for those stubs.

If you change a `.proto`, regenerate:

```bash
make proto
```

That script invokes `protoc-gen-go` + `protoc-gen-go-grpc` and writes
into `gateway/gen/proto/`. The output directory is intentionally
**checked in** — agents and CI can build without running protoc.

## Provenance

| Proto | Origin |
|---|---|
| `livepeer/payments/v1/*` | `livepeer-network-modules/proto-contracts/livepeer/payments/v1/` |
| `livepeer/registry/v1/*` | `livepeer-network-modules/proto-contracts/livepeer/registry/v1/` |

These are vendored copies. The daemon binaries (`payment-daemon`,
`service-registry-daemon`) are pulled as Docker images at runtime
and were generated from these same `.proto` files upstream.

## Updating

Re-sync from upstream:

```bash
cp /path/to/livepeer-network-modules/proto-contracts/livepeer/payments/v1/*.proto \
   proto/livepeer/payments/v1/
cp /path/to/livepeer-network-modules/proto-contracts/livepeer/registry/v1/*.proto \
   proto/livepeer/registry/v1/
make proto
go test ./...
```
