---
plan: 0003
title: Gateway-owned RTMP ingest for live (live-session-gateway-ingest@v0)
status: shipped
phase: end-to-end-test
opened: 2026-05-20
shipped: 2026-05-20
owner: gateway-team
related:
  - "0002-live-session-remote-runner — adopting the prior live mode (completed)"
  - "runner-status-hardening.md — captures runner-team's expanded status/control surface"
---

# 0003 — Gateway-owned RTMP ingest

## What

The gateway is the public RTMP endpoint for live transcode. Customers push
RTMP to `rtmp://<gateway>:1935/live/<stream_key>`. The gateway relays to a
selected orchestrator's private RTMP endpoint. The orchestrator writes HLS
output directly to gateway-owned S3 using short-lived credentials.

Interaction mode `live-session-gateway-ingest@v0` lives upstream at
`livepeer-network-modules/livepeer-network-protocol/modes/live-session-gateway-ingest.md`.

## Why

- **Customer-facing URL stability** — single public ingest endpoint
  regardless of which orch backs the session.
- **Cross-orch failover** — gateway controls the customer's RTMP connection;
  re-binds to a new orch on failure without OBS reconfiguration (soft
  failover semantics for v1).
- **CDN-friendly HLS** — output lives in our object store, served via our
  domain (and a CDN later). Orch-host bandwidth no longer scales with
  viewer count.
- **No per-orch public RTMP port exposure** — orchs only need a private
  endpoint reachable from the gateway.

## Architecture

```
OBS  ──→  gateway:1935  ──→  orch's private RTMP endpoint (rtmp://host:port/live/<gws_key>)
                ↓
        S3 credential
        (scoped to live-out/<api_key>/<live_id>/)
                ↓
        orch live-runner writes HLS  ──→  RustFS / S3  ──→  CDN  ──→  viewers
```

## Capability + offering shape (upstream-finalized)

```
capability id:  video:transcode.live          ← shared across both live modes

  ├─ offering "default"         + mode live-session-remote-runner@v0   (legacy broker_ingest)
  └─ offering "gateway-ingest"  + mode live-session-gateway-ingest@v0  (this plan's new path)
```

The capability id is the same for both live modes; the **offering label**
discriminates which mode an orchestrator is offering. Operators can publish
either, both, or neither per orch.

## Phases shipped (all in this repo)

1. ✅ DB migration `0006_live_gateway_ingest` + LiveStream repo (`ingest_mode`,
   `s3_output_prefix`, `private_ingest_url`, `stream_key_hint`;
   `FindActiveByStreamKeyHash`; `ActivateGatewayIngest`).
2. ✅ RTMP server skeleton in `gateway/internal/rtmp/` (yutopp/go-rtmp,
   bound to `LIVE_RTMP_PORT`, peppered-hash auth via `live_streams.stream_key_hash`).
3. ✅ S3 credential minter at `s3.MintLiveSessionCredentials(prefix, ttl)`.
4. ✅ Broker client for the new mode (`livepeer.OpenLiveSessionGatewayIngest`)
   + wire types `LiveOpenGatewayIngestRequest`/`Response`,
   `LiveOutputCredential`, `LiveIngestAccept`.
5. ✅ `/v1/live` POST handler dispatches by `LIVE_INGEST_MODE_DEFAULT`. The
   `openLiveGatewayIngest` helper resolves on capability `video:transcode.live`
   with offering `gateway-ingest` (per `LIVE_GATEWAY_INGEST_OFFERING`).
6. ✅ RTMP relay (`gateway/internal/rtmp/relay.go`) — yutopp/go-rtmp client
   for upstream push; forwards FLV tags from customer to upstream; closes
   cleanly on customer disconnect.
7. ✅ `/health` adds `rtmp` check. Metrics: `livepeer_gateway_rtmp_active_publishes`
   (gauge) + `livepeer_gateway_rtmp_publishes_total{outcome}` (counter).
8. ✅ Admin `cc-live-streams.js` adds `ingest_mode` column. `AdminLiveStreamView`
   carries the new field.

## Customer-facing semantics

| Today (broker_ingest) | New (gateway_ingest) |
|---|---|
| `rtmp_url` from orch's broker | `rtmp://<gateway>:1935/live/<key>` |
| `stream_key` from orch's broker | issued by gateway, peppered-hashed in DB |
| `hls_url` from orch | `https://<our-s3-or-cdn>/.../master.m3u8` |
| Orch failure ends stream (customer reconfigures OBS) | Orch failure: customer's OBS reconnects; gateway binds to a new orch behind the same URL (soft failover) |

## Configuration knobs

```
LIVE_RTMP_PORT=1935                       # 0 disables the RTMP listener entirely
LIVE_INGEST_MODE_DEFAULT=broker_ingest    # or gateway_ingest
LIVE_PLAYBACK_BASE_URL=                   # optional CDN base; falls back to bucket URL
LIVE_S3_CREDENTIAL_TTL_HOURS=4
LIVE_CAPABILITY=video:transcode.live
LIVE_GATEWAY_INGEST_OFFERING=gateway-ingest
LIVE_IDLE_TIMEOUT_SECS=120
LIVE_RECONCILE_INTERVAL_SECS=30
LIVE_TOPUP_RUNWAY_THRESHOLD_SECS=60
LIVE_TOPUP_FUND_SECS=60
```

## What's blocking end-to-end smoke

| Blocker | Owner |
|---|---|
| Orchestrators re-register capabilities under `video:transcode.live` (both offerings) | orch operators / xodeapp |
| Upstream daemon image `v1.3.2` published to Docker Hub | tztcloud |
| Runner multi-rung ladder support (still passthrough + 1 rung in v1) | runner team |

Our gateway is ready: build clean, both modes coexist behind the
`LIVE_INGEST_MODE_DEFAULT` flag, RTMP listener bound on 1935, S3 credential
minter live, broker client + retry-once rotation handling all in place.

## Open follow-ups (small)

- Surface the runner's richer status fields (`ConnectedPublisher`,
  `LastPacketAt`, `PutFailureCount`) in the admin live-streams view.
- Real STS-style credentials for the runner once RustFS exposes assume-role
  (today: shared bucket credential with key_prefix scoping; documented
  trust boundary).
- CDN integration: when ready, a CDN sits in front of our object store
  with no gateway-side code change.

## Cross-references

- Upstream wire spec: `livepeer-network-modules/livepeer-network-protocol/modes/live-session-gateway-ingest.md`
- Runner status surface: see `runner-status-hardening.md` in this directory
- Prior plan: `completed/0002-live-session-remote-runner.md`
