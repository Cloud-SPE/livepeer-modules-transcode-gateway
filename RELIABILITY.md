# RELIABILITY

Reliability properties this gateway is expected to uphold.

## Hard invariants

- **No customer charge.** v1 has no customer billing — a failed request
  costs the user nothing and is *always safe to retry* from their
  perspective. The gateway's own credit on LOC is what moves.
- **Charge-at-create, settle-actual against LOC.** For VOD: `POST
  /v1/jobs` encumbers the worst-case (funded) credit on LOC and returns
  the envelope + route; after the broker call the gateway settles with
  the actual (estimated) units, and LOC releases `funded − billed`. A
  failed attempt settles `0`. For live: `POST /v1/sessions` encumbers
  up to `LIVE_MAX_TOTAL_UNITS`; the reconciler refills against the same
  session as runway runs low; session close settles a duration estimate.
- **Settle is backstopped.** If the in-line settle doesn't complete
  (crash, transient LOC error), the reservation is left
  `settle_state='pending'` and the settle janitor
  (`SETTLE_JANITOR_INTERVAL_SECS`, default 60s) re-drives it so
  encumbered credit is eventually released. LOC's ledger is
  authoritative.
- **Fail closed when LOC is unreachable.** If LOC can't mint an
  envelope / select a route, `/api/v1/abr` and `/api/v1/live` return
  `503 loc_unavailable` rather than dispatching unpaid work.
- **`/api/v1/*` is API-key-only.** No anonymous access, no
  cookie-session acceptance on `/api/v1/*`. Missing/invalid key returns
  `401` with `WWW-Authenticate: Bearer`.
- **`/api/v1/*` is rate-limited per API key.** Default 60 req/min,
  burst 30. `429` returned with `Retry-After` and the reservation is
  NOT opened.
- **Live streams are payment-bound.** Session lifecycle is tied to the
  LOC session. When the client calls `DELETE /api/v1/live/:id`, or LOC
  signals a refill cap (`will_refuse_next_refill`) / wind-down, or a
  refill rotation can't be recovered, the reconciler ends the stream
  cleanly: synchronously close the customer RTMP socket + upstream relay
  push (~2s OBS disconnect), broker CloseSession, then LOC
  `POST /v1/sessions/{id}/close`. There is no "free idle" mode.
- **VOD ingest is upload-then-job.** `POST /api/v1/abr` accepts an
  `input_url`; if the caller uses our `/api/v1/abr/upload-url` flow,
  the upload happens before the job, against MinIO. The gateway never
  buffers media bytes — for live ingest it only relays RTMP TCP frames
  end-to-end.

## Soft invariants (best-effort, observable)

- **LOC owns routing.** `POST /v1/jobs` returns the broker; the gateway
  does not rank candidates or run per-broker health cooldowns anymore.
  There is no in-gateway multi-candidate failover.
- **Rotation recovery, once.** A broker `401 INVALID_RECIPIENT_RAND`
  (recipient rand rotated) on a VOD job triggers settle(0) + a fresh
  `POST /v1/jobs`, retried once before failing.
- **Capability-catalog refresh is non-blocking.** Background task pulls
  LOC `GET /v1/capabilities` every `REGISTRY_REFRESH_INTERVAL_MS`
  (default 60s). Never blocks the request path.
- **Live status polling is cheap.** `GET /api/v1/live/:id` reads from
  `live_streams` + the reconciler-cached `runner_status_json`; it
  never calls the broker or LOC on the request path.

## /health endpoint

The load-balancer contract:

```json
{
  "status": "ok" | "degraded" | "down",
  "checks": {
    "db":   { "status": "ok" | "error", "latencyMs": N, "error"?: "…" },
    "s3":   { "status": "ok" | "error", "latencyMs": N, "error"?: "…" },
    "loc":  { "status": "ok" | "error" | "skipped", "latencyMs": N, "error"?: "…" },
    "rtmp": { "status": "ok" | "error" | "skipped", "error"?: "…" }
  }
}
```

The `loc` check is a read-only probe of LOC's capability catalog;
`skipped` when `LOC_API_KEY` is unset. The former `payer` / `registry`
checks are gone.

HTTP code semantics:

- `200 + status="ok"` — all subsystems healthy.
- `200 + status="degraded"` — DB is fine, but at least one of s3 / loc /
  rtmp is unreachable. The gateway still serves `/api/portal/*`,
  `/api/admin/*`, and the public surface; affected `/api/v1/*`
  endpoints will 503 at request time (LOC down → `loc_unavailable`).
- `503 + status="down"` — DB is unreachable. Drop the gateway from
  rotation.

## Failure modes

| What can fail | Visible to user | Visible in `/health` |
|---|---|---|
| LOC unreachable / `LOC_API_KEY` unset | `/api/v1/abr` and `/api/v1/live` return `503 loc_unavailable` (fail closed — no envelope, no route). | `loc: error` → `degraded` |
| LOC out of credit | `POST /v1/jobs` / `/v1/sessions` rejected → `503 loc_unavailable`. Top up on LOC. | `loc: ok` (catalog still reachable) |
| MinIO unreachable | `/api/v1/abr/upload-url` returns 503. `/api/v1/abr` with externally-hosted `input_url` still works. Live session-open fails (STS unavailable). | `s3: error` → `degraded` |
| RTMP listener didn't bind | Live ingest unavailable; `POST /api/v1/live` still allocates but customers can't push. | `rtmp: error` → `degraded` |
| In-line settle fails (crash / transient LOC error) | Transparent to user. Reservation sits `settle_state='pending'`; settle janitor re-drives it to release encumbered credit. | n/a |
| Broker `401 INVALID_RECIPIENT_RAND` on VOD | Recipient rand rotated; gateway settles(0) + fresh `POST /v1/jobs`, retried once before failing 502/503. | n/a |
| Broker `401 INVALID_RECIPIENT_RAND` on a live refill | Retried once, then the session ends gracefully (`close_reason=rotation_unrecoverable`). LOC exposes no rotation-recovery primitive yet — known limitation, upstream issue to be filed. | n/a |
| Selected broker 5xx / network error | No in-gateway failover (LOC owns routing). VOD settles(0) and returns 502/503. | n/a |
| Selected broker 4xx | Propagate verbatim — that's the user's problem. | n/a |
| Postgres unreachable | All routes return 500 / 503. | `db: error` → `down` → HTTP 503 |
| Resend unreachable | Signup still succeeds (waitlist row persists). Verification email logged + not retried. Admin can resend via `POST /api/admin/waitlist/:id/resend-verification`. | n/a |
| Rate-limit exhaustion | `429 rate_limit_exceeded` with `Retry-After`. Reservation NOT opened. | n/a |
| Live refill cap reached | LOC returns `will_refuse_next_refill` / `winddown_reason`; reconciler ends the stream cleanly (broker end + LOC CloseSession + RTMP teardown). `GET /api/v1/live/:id` returns the final state. | n/a |

## Observability surface

- **Prometheus** at `/metrics`. Optionally Bearer-gated via
  `METRICS_TOKEN`. Surfaces process metrics, HTTP counters + duration
  histograms, `video_gateway_proxy_reservations_total{capability,outcome}`,
  `video_gateway_live_streams_active`,
  `video_gateway_waitlist_signups_total`.
- **Structured JSON logs** to stdout via `log/slog`. Per-request fields:
  `reqId`, `method`, `path`, `status`, `dur_ms`, plus ad-hoc
  structured fields (e.g. `api_key_id`, `email`, `err`).
- **`usage_reservations`** + **`live_streams`** are the durable per-
  request and per-session logs. Queryable via `/api/admin/usage` and
  `/api/portal/usage`.

## What we explicitly accept

- **No retries on stream-mid-flight failures.** Once RTMP ingest is
  flowing, an upstream relay disconnection terminates the live stream.
- **No idempotency keys in v1.** A duplicate `POST /api/v1/abr`
  creates a duplicate job.
- **In-process rate-limit only.** A multi-replica deploy doesn't
  share buckets.
- **No SLA.** This is beta.
