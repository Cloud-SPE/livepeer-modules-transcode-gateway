# RELIABILITY

Reliability properties this gateway is expected to uphold.

## Hard invariants

- **No charge, no commit.** v1 has no customer billing, so there is
  no ledger to commit or refund. The gateway is *always safe to
  retry* from the user's perspective: a failed request costs nothing.
- **Gateway pays the network exactly once per attempted upstream
  call.** For VOD: a payment envelope is minted per broker attempt; a
  retry to a second broker mints a second envelope. For live: a
  payment envelope is minted at session-open; interim-debits happen
  through the payment-daemon during the session.
- **`/api/v1/*` is API-key-only.** No anonymous access, no
  cookie-session acceptance on `/api/v1/*`. Missing/invalid key returns
  `401` with `WWW-Authenticate: Bearer`.
- **`/api/v1/*` is rate-limited per API key.** Default 60 req/min,
  burst 30. `429` returned with `Retry-After` and the reservation is
  NOT opened.
- **Live streams are payment-bound.** Session lifecycle is tied to
  the payment session. When balance is exhausted or the client calls
  `DELETE /api/v1/live/:id`, the gateway synchronously closes the
  customer RTMP socket + the upstream relay push (~2s OBS disconnect),
  then calls broker CloseSession. There is no "free idle" mode.
- **VOD ingest is upload-then-job.** `POST /api/v1/abr` accepts an
  `input_url`; if the caller uses our `/api/v1/abr/upload-url` flow,
  the upload happens before the job, against MinIO. The gateway never
  buffers media bytes — for live ingest it only relays RTMP TCP frames
  end-to-end.

## Soft invariants (best-effort, observable)

- **`p95` route-selection latency** under nominal load is <50ms
  end-to-end. Above that, something in the route selector or DB is
  wrong.
- **Route failover happens.** Retryable errors (5xx / 429 / timeout)
  from a broker trigger the next ranked candidate before failing.
- **Route health backs off bad brokers.** Default 2 consecutive
  failures → 30s cooldown.
- **Registry refresh is non-blocking.** Background task every
  `REGISTRY_REFRESH_INTERVAL_MS` (default 60s). Never blocks request
  path.
- **Live status polling is cheap.** `GET /api/v1/live/:id` reads from
  `live_streams` + the reconciler-cached `runner_status_json`; it
  never makes a fresh gRPC call to the broker on the request path.

## /health endpoint

The load-balancer contract:

```json
{
  "status": "ok" | "degraded" | "down",
  "checks": {
    "db":       { "status": "ok" | "error", "latencyMs": N, "error"?: "…" },
    "minio":    { "status": "ok" | "error", "latencyMs": N, "error"?: "…" },
    "rtmp":     { "status": "ok" | "error" | "skipped", "error"?: "…" },
    "payer":    { "status": "ok" | "error" | "skipped", "latencyMs": N, "error"?: "…" },
    "registry": { "status": "ok" | "error" | "skipped", "latencyMs": N, "error"?: "…" }
  }
}
```

HTTP code semantics:

- `200 + status="ok"` — all subsystems healthy.
- `200 + status="degraded"` — DB is fine, but at least one of minio /
  rtmp / payer / registry is unreachable. The gateway still serves
  `/api/portal/*`, `/api/admin/*`, and the public surface; affected
  `/api/v1/*` endpoints will 500/503 at request time.
- `503 + status="down"` — DB is unreachable. Drop the gateway from
  rotation.

## Failure modes

| What can fail | Visible to user | Visible in `/health` |
|---|---|---|
| `service-registry-daemon` unreachable | `/api/v1/*` returns 502 because no candidates can be loaded. | `registry: error` → `degraded` |
| `payment-daemon` unreachable | `/api/v1/*` returns 500 — `buildPayment` throws. Reservation refunded. | `payer: error` → `degraded` |
| MinIO unreachable | `/api/v1/abr/upload-url` returns 503. `/api/v1/abr` with externally-hosted `input_url` still works. Live session-open fails (STS unavailable). | `minio: error` → `degraded` |
| RTMP listener didn't bind | Live ingest unavailable; `POST /api/v1/live` still allocates but customers can't push. | `rtmp: error` → `degraded` |
| No brokers advertise the requested capability | `/api/v1/*` returns the last broker's error. | n/a |
| Selected broker 5xx / network error | Try next ranked candidate. Exhaust all → 502. | n/a |
| Selected broker 4xx | Propagate verbatim — that's the user's problem. | n/a |
| Postgres unreachable | All routes return 500 / 503. | `db: error` → `down` → HTTP 503 |
| Resend unreachable | Signup still succeeds (waitlist row persists). Verification email logged + not retried. Admin can resend via `POST /api/admin/waitlist/:id/resend-verification`. | n/a |
| Rate-limit exhaustion | `429 rate_limit_exceeded` with `Retry-After`. Reservation NOT opened. | n/a |
| Live stream balance exhausted | Broker emits `insufficient_balance` close reason; reconciler picks up `status='ended'` on next tick. `GET /api/v1/live/:id` returns the final state. | n/a |

## Observability surface

- **Prometheus** at `/metrics`. Optionally Bearer-gated via
  `METRICS_TOKEN`. Surfaces process metrics, HTTP counters + duration
  histograms, `video_gateway_proxy_reservations_total{capability,outcome}`,
  `video_gateway_live_streams_active`,
  `video_gateway_waitlist_signups_total`,
  `livepeer_gateway_route_health_*`.
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
