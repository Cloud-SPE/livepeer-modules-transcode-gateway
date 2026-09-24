> Historical pre-v2 design. For current behavior see [Modules v2](modules-v2.md) and [deployment](../../DEPLOYMENT.md).

# Payment flow

How `Livepeer-Payment` envelopes get minted for `/api/v1/*` requests.

> Both surfaces mint via the **LOC** (Livepeer Open Clearinghouse):
> ABR through the jobs API, live through the sessions API. No local
> daemons or operator keystore are involved.

## VOD (LOC jobs)

LOC owns route selection and payment minting. The gateway asks LOC for
a job, makes the broker call itself with the returned envelope, then
settles actual work units back to LOC. LOC encumbers the worst-case
credit at create and refunds `funded − billed` at settle.

```
client → POST /api/v1/abr
  gateway opens usage_reservations (settle_state='none')
  gateway calls LOC POST /v1/jobs {capability, offering, estimated_units}
    → {job_id, work_id, broker_url, payment_envelope, funded/expected wei}
  gateway records loc_job_id on the reservation (settle_state='pending')
  gateway POSTs to broker_url/v1/cap with Livepeer-Payment header
  if 2xx → commit reservation,
           LOC POST /v1/jobs/{id}/settle {actual_units=estimate}  (settle_state='settled')
           return job descriptor
  if INVALID_RECIPIENT_RAND → settle(0) to release credit, create a
           fresh LOC job (fresh envelope, possibly fresh broker), retry once
  if other failure → settle(0) (settle_state='refunded'), refund reservation, 502
```

Notes:

- **Settles use the estimate** (`estimated_input_seconds × 600`): the
  runner webhook reports completion, not unit counts, so waiting
  wouldn't improve accuracy — and settling promptly releases LOC's
  worst-case encumbrance.
- **Every LOC job must be settled.** The settle janitor
  (`internal/server/settle_janitor.go`, `SETTLE_JANITOR_INTERVAL_SECS`)
  re-drives rows stuck in `settle_state='pending'` (settle-call
  failures, crashes between dispatch and settle). A 409
  `job_already_settled` is treated as success.
- **No multi-candidate failover in the gateway.** LOC picks the route;
  on broker failure the recovery is settle(0) + a fresh create. (The
  old candidate-walk dispatcher and its route-health cooldowns were
  deleted in PR-1.)
- **No idempotency keys** on LOC creates — the gateway never blindly
  retries a create after an ambiguous timeout (double-encumbrance risk).

## Live (LOC sessions)

```
client → POST /api/v1/live
  gateway opens long-lived usage_reservations + live_streams (status='provisioning')
  gateway mints stream keys + per-session S3 STS credentials
  gateway calls LOC POST /v1/sessions {capability, offering,
    estimated_runway_units=60000, max_total_units=LIVE_MAX_TOTAL_UNITS}
    → {session_id, work_id, broker_url, payment_envelope}
  gateway POSTs broker /v1/cap (gateway-ingest mode) with Livepeer-Payment
  on INVALID_RECIPIENT_RAND → close LOC session (0 units), open a fresh
    one, retry once
  broker returns {broker_session_id, private_ingest_url}
  gateway activates live_streams (status='live', loc_session_id persisted)
  gateway → client {id, ingest, playback}

during the session (reconciler tick):
  runway below threshold → LOC POST /v1/sessions/{id}/refill
    → fresh envelope (pinned to the SAME broker; same work_id)
  gateway POSTs broker /v1/cap/{bsess}/topup with the envelope
  cap_status.will_refuse_next_refill=true → warn; stream ends when this
    funding drains (or wind down on the cap refusal)
  refill refused (cap) / rotation unrecoverable → graceful wind-down:
    broker end + LOC close + RTMP teardown

client → DELETE /api/v1/live/:id (or broker ends the session)
  gateway calls broker /v1/cap/{bsess}/end
  gateway calls LOC POST /v1/sessions/{id}/close
    {actual_units = min(elapsed_secs × 1000, LIVE_MAX_TOTAL_UNITS)}
    — a duration ESTIMATE: the broker reports only runway, never
    consumed units; LOC's daemon-ledger reconciliation is authoritative
  ClaimLOCClose (loc_closed_at) makes DELETE vs reconciler race-safe
```

One reservation row per live session — refills bump
`live_streams.loc_refill_count` instead of opening per-envelope rows
(LOC's session ledger is the authoritative money log).

Live streams do **not** failover on broker failure mid-session.
Restarting requires a fresh `POST /api/v1/live` — that's a client
responsibility.

## What lives where

| File | Role |
|---|---|
| `gateway/internal/proxy/loc/` | HTTP client to LOC: jobs + sessions + capabilities; error matchers; settle retry policy. |
| `gateway/internal/server/handlers_v1.go` | ABR create→dispatch→settle loop incl. rotation retry; `settleLOCJob` helper. |
| `gateway/internal/server/settle_janitor.go` | Re-drives stuck `settle_state='pending'` rows. |
| `gateway/internal/server/live_loc.go` | `closeLiveLOCSession` — race-safe LOC session settlement (DELETE vs reconciler). |
| `gateway/internal/server/live_reconciler.go` | Refill + winddown orchestration for live sessions. |
| `gateway/internal/proxy/livepeer/headers.go` | Attaches the envelope to outbound broker requests as `Livepeer-Payment`. |

## Failure semantics (ABR / LOC)

- **LOC unreachable or LOC_API_KEY unset** → 503 `loc_unavailable`, no
  reservation opened / refunded.
- **`NO_ROUTE_AVAILABLE`** → 502 `no_capable_broker`, reservation refunded.
- **`INSUFFICIENT_CREDIT` / spend-cap** → 402 `insufficient_credit`;
  the operator-granted LOC credit pool needs a topup.
- **Broker 402 (face value too small)** → settle(0), refund, 502.
- **Crash between create and settle** → reservation stays
  `settle_state='pending'`; janitor settles (estimate if committed,
  0 + refund if still open) on its next tick.

## What this doc does not cover

- LOC's internal ledger / EV-charging model — see the basic-pymnthouse
  repo's DESIGN.md.
- The ticket math itself — see go-livepeer's `pm` package and the
  `payee_daemon.proto` types.
- LOC-account onboarding and credit funding — see
  [`../../DEPLOYMENT.md`](../../DEPLOYMENT.md).
