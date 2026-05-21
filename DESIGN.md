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
| 1 | Transcode surface | `/api/v1/abr` (VOD ABR ladder), `/api/v1/abr/upload-url` (MinIO presign), `/api/v1/live` POST/GET/DELETE (RTMP→HLS sessions), `/api/v1/capabilities` (registry catalog). |
| 2 | Wire translation | Request → `Livepeer-Capability` header + interaction mode (`http-reqresp@v0` for VOD, `live-session-gateway-ingest@v0` for live). All in `gateway/internal/proxy/livepeer/`. |
| 3 | Route selection | `service-registry-daemon` (gRPC over UDS) gives candidate brokers per capability. `routeSelector` ranks by constraints / extras / price; `routeHealth` tracks per-candidate failure cooldowns. |
| 4 | Payment | `payment-daemon` (gRPC over UDS) mints `Livepeer-Payment` envelopes. The gateway pays the network on behalf of every request — customers pay nothing in v1. Live streams mint on session open and interim-debit during the session. |
| 5 | SaaS shell | Postgres-backed waitlist + email-verify + admin-approval + API-key issuance. Cookie sessions for the portal UI. `ADMIN_TOKEN` env var bootstraps admin access. |
| 6 | Usage + asset tracking | Per-request reservations are opened, then committed or refunded with route-aware settlement metadata. Live streams reference a long-lived reservation. VOD ingest bytes land in MinIO (S3-compatible); live HLS output lands in the same MinIO under a session-scoped prefix. |

## What this gateway does NOT do (v1)

- **Charge customers.** No Stripe, no wallet, no rate cards.
- **Host playback.** Returns HLS URLs that point at MinIO (and a CDN
  later). No gateway-side HTTP playback proxy.
- **Single-rendition VOD transcode.** ABR ladder only.
- **Realtime push updates.** `/api/v1/live/:id` is poll-only; no SSE or webhooks.
- **Run media bytes through Go.** All encoding/muxing is on the runner
  side. The gateway only relays RTMP TCP frames for live ingest.
- **Hardcode capability lists.** `/api/v1/capabilities` reflects what the
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
- `minio/minio` (S3-compatible object store for VOD ingest + live HLS output, with STS for per-session scoped creds)
- `capability-broker` + `abr-runner` + live-runner (live on the orchestrator side)

## Stack composition for `make dev`

```
  ┌────────────────┐  /api/v1/* ┌────────┐                ┌────────────┐
  │  curl / SDK    │ ──────────►│gateway │ ─── UDS ───► │ registry-  │
  │  (host)        │            │   Go   │             │ daemon     │
  └────────────────┘            │ +SPAs  │             └────────────┘
  ┌────────────────┐  RTMP 1935 │ +RTMP  │
  │  OBS / ffmpeg  │ ──────────►│        │
  └────────────────┘            └───┬────┘
                                     │
                                     ├ UDS ─► payer-daemon
                                     │
                                     ├ S3+STS ─► minio
                                     │
                                     ▼
                              ┌──────────────┐
                              │   postgres   │
                              └──────────────┘
```

Capability workers (abr-runner, live-runner, broker) are not part of
this compose. The gateway runtime is on-chain only and does not support
static registry overlays.

## Open design questions

Tracked in [`docs/exec-plans/tech-debt-tracker.md`](./docs/exec-plans/tech-debt-tracker.md).
