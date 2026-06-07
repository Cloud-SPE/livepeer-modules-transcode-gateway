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
spec, asks **LOC** (the Livepeer Open Clearinghouse) to mint a payment
envelope and pick a route in one call, dispatches the work to the
broker LOC selected, then settles the job back to LOC — returning the
response (job descriptor for VOD, ingest+playback descriptor for live)
verbatim.

## Six layers

| # | Layer | What it does |
|---|---|---|
| 1 | Transcode surface | `/api/v1/abr` (VOD ABR ladder), `/api/v1/abr/upload-url` (MinIO presign), `/api/v1/live` POST/GET/DELETE (RTMP→HLS sessions), `/api/v1/capabilities` (LOC catalog snapshot). |
| 2 | Wire translation | Request → `Livepeer-Capability` header + interaction mode (`http-reqresp@v0` for VOD, `live-session-gateway-ingest@v0` for live). All in `gateway/internal/proxy/livepeer/`. |
| 3 | Routing + payment (LOC) | `gateway/internal/proxy/loc/` calls LOC over HTTPS (`LOC_BASE_URL`, `LOC_API_KEY`). `POST /v1/jobs` mints the `Livepeer-Payment` envelope **and** selects the broker in one call — LOC owns routing, so there's no in-gateway route selector/health or multi-candidate failover. The gateway pays the network on behalf of every request; customers pay nothing in v1. |
| 4 | Settlement | After the broker call, the gateway settles to LOC: VOD `POST /v1/jobs/{id}/settle {actual_units}`; live `POST /v1/sessions/{id}/close {duration estimate}`. Both are estimates — the broker reports only runway, not consumed units — so LOC's ledger reconciliation is authoritative. A settle janitor (`SETTLE_JANITOR_INTERVAL_SECS`) re-drives any reservation stuck in `settle_state='pending'`. |
| 5 | SaaS shell | Postgres-backed waitlist + email-verify + admin-approval + API-key issuance. Cookie sessions for the portal UI. `ADMIN_TOKEN` env var bootstraps admin access. |
| 6 | Usage + asset tracking | Per-request reservations are opened, then committed or refunded with LOC job/settle metadata (`loc_job_id`, `funded`/`expected`/`billed` wei, `settle_state`). Live streams carry one long-lived reservation plus `loc_session_id`/`loc_refill_count`. VOD ingest bytes land in MinIO (S3-compatible); live HLS output lands in the same MinIO under a session-scoped prefix. |

## What this gateway does NOT do (v1)

- **Charge customers.** No Stripe, no wallet, no rate cards.
- **Host playback.** Returns HLS URLs that point at MinIO (and a CDN
  later). No gateway-side HTTP playback proxy.
- **Single-rendition VOD transcode.** ABR ladder only.
- **Realtime push updates.** `/api/v1/live/:id` is poll-only; no SSE or webhooks.
- **Run media bytes through Go.** All encoding/muxing is on the runner
  side. The gateway only relays RTMP TCP frames for live ingest.
- **Hardcode capability lists.** `/api/v1/capabilities` reflects LOC's
  catalog (`GET /v1/capabilities`) as of the last refresh.

## Components

```
livepeer-modules-transcode-gateway/
├── gateway/                  # this service (Go)
└── web/{site,portal,admin}/  # 3 zero-build Lit SPAs
```

(`proto/` lingers from the retired payer/resolver daemons; the live
wire path is now plain HTTPS to LOC.)

External services:

- **LOC — Livepeer Open Clearinghouse** (`loc.cloudspe.com`, HTTPS): payment minting, route selection, settlement, capability catalog
- `minio/minio` (S3-compatible object store for VOD ingest + live HLS output, with STS for per-session scoped creds)
- `capability-broker` + `abr-runner` + live-runner (live on the orchestrator side)

## Stack composition for `make dev`

```
  ┌────────────────┐  /api/v1/* ┌────────┐   HTTPS    ┌──────────────┐
  │  curl / SDK    │ ──────────►│gateway │ ─────────► │ LOC          │
  │  (host)        │            │   Go   │  pymth_key │ clearinghouse│
  └────────────────┘            │ +SPAs  │            └──────────────┘
  ┌────────────────┐  RTMP 1935 │ +RTMP  │
  │  OBS / ffmpeg  │ ──────────►│        │
  └────────────────┘            └───┬────┘
                                     │
                                     ├ S3+STS ─► minio
                                     │
                                     ▼
                              ┌──────────────┐
                              │   postgres   │
                              └──────────────┘
```

`make dev` brings up gateway + postgres + minio (+ bootstrap) only.
LOC is a hosted service reached over HTTPS — no local daemons, keystore,
or chain RPC. Capability workers (abr-runner, live-runner, broker) live
on the orchestrator side and are not part of this compose.

## Open design questions

Tracked in [`docs/exec-plans/tech-debt-tracker.md`](./docs/exec-plans/tech-debt-tracker.md).
