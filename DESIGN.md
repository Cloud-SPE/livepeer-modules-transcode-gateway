# DESIGN

Architectural overview at a glance. The deep version lives in
[`docs/design-docs/`](./docs/design-docs/).

## The pin

> **A transcode gateway whose backend is the Livepeer decentralized GPU
> network — exposing VOD ABR ladder transcoding and RTMP→HLS live
> streaming with a thin SaaS shell for access control.**

Every architectural choice in this repo flows from that requirement.

## Shape in one sentence

A single Go binary translates client requests into the Livepeer wire
spec, picks a transcode route from `service-registry-daemon`, mints a
payment envelope via `payment-daemon`, and dispatches the work to the
selected `capability-broker` — returning the response (job descriptor
for VOD, ingest+playback descriptor for live) verbatim.

## Six layers

| # | Layer | What it does |
|---|---|---|
| 1 | Transcode surface | `/v1/abr` (VOD ABR ladder), `/v1/abr/upload-url` (RustFS presign), `/v1/live` POST/GET/DELETE (RTMP→HLS sessions), `/v1/capabilities` (registry catalog). |
| 2 | Wire translation | Request → `Livepeer-Capability` header + interaction mode (`http-reqresp@v0` for VOD, `rtmp-ingress-hls-egress@v0` for live). All in `gateway/internal/proxy/livepeer/`. |
| 3 | Route selection | `service-registry-daemon` (gRPC over UDS) gives candidate brokers per capability. `routeSelector` ranks by constraints / extras / price; `routeHealth` tracks per-candidate failure cooldowns. |
| 4 | Payment | `payment-daemon` (gRPC over UDS) mints `Livepeer-Payment` envelopes. The gateway pays the network on behalf of every request — customers pay nothing in v1. Live streams mint on session open and interim-debit during the session. |
| 5 | SaaS shell | Postgres-backed waitlist + email-verify + admin-approval + API-key issuance. Cookie sessions for the portal UI. `ADMIN_TOKEN` env var bootstraps admin access. |
| 6 | Usage + asset tracking | Per-request reservations are opened, then committed or refunded with route-aware settlement metadata. Live streams reference a long-lived reservation. VOD ingest bytes land in RustFS (S3-compatible). |

## What this gateway does NOT do (v1)

- **Charge customers.** No Stripe, no wallet, no rate cards.
- **Host playback.** Returns broker-issued HLS URLs; no gateway proxy.
- **Single-rendition VOD transcode.** ABR ladder only.
- **Realtime push updates.** `/v1/live/:id` is poll-only; no SSE or webhooks.
- **Run media bytes through Go.** All encoding/muxing is on the broker/runner side.
- **Hardcode capability lists.** `/v1/capabilities` reflects what the
  on-chain registry advertises right now.

## Components

```
livepeer-modules-transcode-gateway/
├── gateway/                  # this service (Go)
├── web/{site,portal,admin}/  # 3 zero-build Lit SPAs
└── proto/                    # gRPC protos shared with the daemons
```

External (Docker images pulled at runtime):

- `service-registry-daemon` (`tztcloud/livepeer-service-registry-daemon`)
- `payment-daemon` (`tztcloud/livepeer-payment-daemon`)
- `rustfs/rustfs` (S3-compatible object store for VOD ingest)
- `capability-broker` + `abr-runner` (live on the orchestrator side)

## Stack composition for `make dev`

```
  ┌────────────────┐    /v1/*    ┌────────┐                ┌────────────┐
  │  curl / SDK    │ ──────────► │gateway │ ─── UDS ──► │ registry-  │
  │  (host)        │             │   Go   │            │ daemon     │
  └────────────────┘             └───┬────┘            └────────────┘
                                     │
                                     ├ UDS ─► payer-daemon
                                     │
                                     ├ S3  ─► rustfs
                                     │
                                     ▼
                              ┌──────────────┐
                              │   postgres   │
                              └──────────────┘
```

Capability workers (abr-runner, broker) are not part of this compose.
The gateway runtime is on-chain only and does not support static
registry overlays.

## Open design questions

Tracked in [`docs/exec-plans/tech-debt-tracker.md`](./docs/exec-plans/tech-debt-tracker.md).
