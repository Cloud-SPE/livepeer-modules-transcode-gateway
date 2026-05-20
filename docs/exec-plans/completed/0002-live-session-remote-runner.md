---
plan: 0002
title: Adopt live-session-remote-runner@v0 for /v1/live
status: completed
phase: shipped
opened: 2026-05-20
closed: 2026-05-20
owner: gateway-team
---

# 0002 — Adopt `live-session-remote-runner@v0` for `/v1/live`

Internal control-plane rewrite. The customer-facing `/v1/live` API
shape did not change. The internal gateway↔broker contract migrated
from the legacy `/v1/session` adapter onto the new
`live-session-remote-runner@v0` mode landed upstream on 2026-05-20.

## Why

The Livepeer network-modules team split the live media architecture:
the broker keeps payment + session authority; a separate live-runner
owns the RTMP/HLS media runtime. Gateway integration with the new
mode is the consumer-side half of that split. Wire spec:
[`livepeer-network-protocol/modes/live-session-remote-runner.md`](https://github.com/Cloud-SPE/livepeer-network-modules/blob/main/livepeer-network-protocol/modes/live-session-remote-runner.md).

## What shipped

### Schema — migration 0005

Adds five identifier / reconciliation columns to `live_streams` and a
foreign key on `usage_reservations` so per-envelope funding rows can be
enumerated per live session:

```
live_streams +
  broker_session_id   TEXT
  runner_session_id   TEXT
  broker_work_id      UUID
  close_reason        TEXT
  last_broker_sync_at TIMESTAMPTZ

usage_reservations +
  live_stream_id      UUID  → live_streams(id)
```

Indexes:
- `idx_live_streams_broker_session_id` (lookup by bsess)
- `idx_live_streams_active_reconcile` (covers the reconciler scan)
- `idx_usage_reservations_live_stream_id`

### New broker client

`gateway/internal/proxy/livepeer/live_session.go` replaces the deleted
`rtmp_session.go`. Four endpoints on `*HTTPClient`:

| Method | Wire |
|---|---|
| `OpenLiveSession` | `POST /v1/cap` with `Livepeer-Mode: live-session-remote-runner@v0` |
| `TopUpLiveSession` | `POST /v1/cap/{broker_session_id}/topup` |
| `GetLiveSession` | `GET /v1/cap/{broker_session_id}` |
| `EndLiveSession` | `POST /v1/cap/{broker_session_id}/end` |

All four go through one `doJSON` helper so 4xx/5xx returns `*BrokerError`
consistently — `IsInvalidRecipientRandError` matches session rotations
on every live endpoint, not just open.

### Handlers — `/v1/live` POST / GET / DELETE

Customer surface unchanged (same `LiveCreateOut`, same `LiveIngest` /
`LivePlayback` body shapes). Internally:

- **POST** — opens local row + reservation, mints initial payment, calls
  `OpenLiveSession` with `gateway_session_id = live.id`, persists all
  five new IDs, commits the reservation at acceptance (no refund on
  later DELETE per agreed semantics). Session-rotation retry-once is
  preserved on this path.
- **GET** — calls `reconcileLiveSession()` on rows still in
  `provisioning`/`live`, mapping broker state via `mapBrokerState()`.
  Stream key never returned. Adds `close_reason` to the response. Best
  effort: broker errors don't fail the customer GET.
- **DELETE** — calls `EndLiveSession(reason="gateway_close")`, persists
  the broker-returned `close_reason`, idempotent on already-ended rows.

### Background reconciler + auto-topup

`gateway/internal/server/live_reconciler.go`. Goroutine started from
`cmd/gateway/main.go`. Single scan per tick, two internally-separate
concerns:

1. **State reconciliation** — `reconcileLiveSession()` shared with the
   on-GET path; persists broker state + close_reason via
   `Live.RecordBrokerSync`.
2. **Auto-topup** — when broker reports
   `balance.runway_seconds_estimate < LIVE_TOPUP_RUNWAY_THRESHOLD_SECS`
   AND state is `publishing`, mints a new envelope, opens a new
   per-envelope reservation linked via `live_stream_id`, calls broker
   topup. Session-rotation retry-once also wired here.

Per-row 5s context budget so one slow broker can't starve the rest of
the scan.

### Config (`config.go` + `docker-compose.yml` + `.env.example`)

```
LIVE_IDLE_TIMEOUT_SECS=120              # broker idle timeout on session open
LIVE_RECONCILE_INTERVAL_SECS=30         # 0 disables the background loop
LIVE_TOPUP_RUNWAY_THRESHOLD_SECS=60     # mint topup below this
LIVE_TOPUP_FUND_SECS=60                 # seconds of runway each topup buys
```

All four are operator-overridable via `.env`; the gateway falls back to
the documented defaults when unset.

### Metrics

```
livepeer_gateway_live_topup_attempts_total{capability, outcome}
  outcome ∈ {succeeded, mint_failed, broker_failed,
             resolver_failed, broker_drift, reservation_failed}
```

Distinct from `session_rotation_retries_total` — top-up triggered by
runway exhaustion is a separate signal from a session-rand rotation.

### Daemon versions

Both daemons pinned to v1.3.1 (the spec landed in the same release):

```
.env.example:
  LIVEPEER_REGISTRY_DAEMON_TAG=v1.3.1
  LIVEPEER_PAYER_DAEMON_TAG=v1.3.1

docker-compose.yml defaults:
  tztcloud/livepeer-service-registry-daemon:v1.3.1
  tztcloud/livepeer-payment-daemon:v1.3.1
```

## Verified on this stack

| Check | Result |
|---|---|
| migration applied | `schema_migrations`: `version=5, dirty=f` |
| reconciler started | `live reconciler started interval_secs=30 topup_threshold_secs=60 topup_fund_secs=60` |
| payer-daemon | `version=v1.3.1-fea8e6192a66 mode=sender` |
| service-registry-daemon | `version=v1.3.1-fea8e6192a66 mode=resolver cache_size=53` |
| `/health` | `ok` (db / payer / registry / rustfs) |
| `/v1/abr` regression | 200 (untouched by this change) |
| `/v1/live` | 502 `registry_select_failed` — pure resolver `not_found`; xodeapp doesn't yet advertise `video:live.rtmp`. Gateway is ready; supply side hasn't published the offering yet. |
| dispatcher unit tests | `ok internal/proxy/service 1.011s` |

## What blocks first real `/v1/live` smoke

Operator-side: `xodeapp` (or any orchestrator) needs to advertise a
`video:live.rtmp` capability with `live-session-remote-runner@v0` mode +
a remote live-runner wired up. Once that lands on chain + manifest, the
gateway's first `/v1/live` POST will:

1. Resolve the live capability
2. Mint payment envelope
3. POST `/v1/cap` with `Livepeer-Mode: live-session-remote-runner@v0` and the agreed body
4. Persist `broker_session_id` / `runner_session_id` / `broker_work_id` from the response
5. Return RTMP ingest URL + plaintext stream key (once) + HLS playback URL to the customer

No further gateway-side code change needed.

## Out of scope, deliberately

- Customer-facing manual top-up route — auto-only per design decision A.
- Customer-facing refund flow on DELETE or terminal failure — payment
  layer reflects credited value per E/F. Compensation is a separate
  policy concern.
- WebSocket / push delivery of broker session events — polling is the
  current and near-term answer per the runner team's guidance.
- Live-runner direct calls — the gateway never talks to the runner;
  the broker is the only authority on the wire.

## Cross-references

- Wire spec: `livepeer-network-protocol/modes/live-session-remote-runner.md` (upstream)
- Broker driver: `capability-broker/internal/modes/sessioncontrolexternalmedia/` (directory misleadingly named; `const Mode = "live-session-remote-runner@v0"`)
- Live runner: `livepeer-modules-transcode-runners/live-runner/`
- Predecessor plan: [`0001-scaffold.md`](./0001-scaffold.md)
- Related tech-debt: [`../tech-debt-tracker.md`](../tech-debt-tracker.md) — log session-state validation against a real broker once xodeapp publishes the live capability.
