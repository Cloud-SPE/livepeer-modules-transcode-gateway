# Payment flow

How `Livepeer-Payment` envelopes get minted for `/api/v1/*` requests.

> **Migration status:** ABR (VOD) runs on the **LOC** (Livepeer Open
> Clearinghouse) jobs API as of PR-1. Live still mints via the local
> payer-daemon + resolver pair and moves to LOC sessions in PR-3.

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

## Live (session-bound, legacy daemons until PR-3)

```
client → POST /api/v1/live
  gateway opens long-lived usage_reservations + live_streams (status='provisioning')
  gateway calls Resolver.SelectMany(capability=video:transcode.live)
  pick top candidate (no failover on session-open — live can't retry mid-handshake)
  gateway calls PayerDaemon.CreatePayment(face_value, …) with face_value
    sized for the session's estimated initial budget
  gateway POSTs broker.OpenSession with Livepeer-Payment header
  broker returns {session_id, rtmp_url, stream_key, hls_url}
  gateway updates live_streams (status='live', urls populated)
  gateway → client {id, ingest, playback}

during the session:
  broker debits the payment session via payment-daemon as work-units accrue
  if balance is exhausted → broker tears down RTMP → live_streams.status='ended'

client → DELETE /api/v1/live/:id
  gateway calls broker.CloseSession
  gateway settles via payment-daemon
  gateway updates live_streams.status='ended', usage_reservations.state='committed'
```

Live streams do **not** failover on broker failure mid-session.
Restarting requires a fresh `POST /api/v1/live` — that's a client
responsibility.

## What lives where

| File | Role |
|---|---|
| `gateway/internal/proxy/loc/` | HTTP client to LOC: `CreateJob` / `SettleJob` (+ sessions for PR-3); error matchers; settle retry policy. |
| `gateway/internal/server/handlers_v1.go` | ABR create→dispatch→settle loop incl. rotation retry; `settleLOCJob` helper. |
| `gateway/internal/server/settle_janitor.go` | Re-drives stuck `settle_state='pending'` rows. |
| `gateway/internal/proxy/livepeer/payment.go` | (live only, until PR-3) gRPC client to `payment-daemon`. |
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
- Operator-side keystore funding (live path only until PR-3) — see
  [`../../DEPLOYMENT.md`](../../DEPLOYMENT.md).
