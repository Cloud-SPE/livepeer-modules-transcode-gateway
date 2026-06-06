# LOC refill vs broker payment-session rotation

Live streams can end with close reason `rotation_unrecoverable`. This
runbook explains why, what the gateway already did about it, and what
would actually fix it.

## Symptom

- A live stream ends mid-broadcast; `live_streams.close_reason =
  'rotation_unrecoverable'`.
- Gateway logs show the reconciler's topup path getting broker
  `401` + `INVALID_RECIPIENT_RAND` on `POST /v1/cap/{bsess}/topup`,
  twice in a row.
- Metric: `livepeer_gateway_session_rotation_retries_total{outcome="retry_failed"}`
  and `livepeer_gateway_live_topup_attempts_total{outcome="rotation_unrecoverable"}`.

## Why

The broker (receiver side) rotated its payment session, invalidating
the `recipient_rand` baked into envelopes minted before the rotation.
LOC's `POST /v1/sessions/{id}/refill` is pinned to the session's
original recipient state (that pinning is what makes refills land on
the same broker), and LOC exposes **no rotation-recovery primitive** —
nothing equivalent to the old payer-daemon `ReportPaymentResult` that
evicted the cached session and re-fetched fresh `TicketParams`.

## What the gateway does

1. Retries the refill once (in case LOC's daemon refreshed its cache
   between calls).
2. If the second envelope is also rejected: winds the stream down
   gracefully — broker `/end`, LOC `CloseSession` (duration-estimate
   units), `live_streams.status='ended'`, RTMP relay teardown. The
   customer sees a clean disconnect instead of a starved stream.

There is no operator action that saves the running stream; the
customer restarts with a fresh `POST /api/v1/live`.

## The actual fix (upstream, to file against LOC)

LOC needs one of:
- a `force_rotate: true` flag on `POST /v1/sessions/{id}/refill` that
  makes the payment daemon re-fetch `TicketParams` from the broker
  before minting, or
- automatic eviction + re-mint when a client reports a refill envelope
  was rejected with `INVALID_RECIPIENT_RAND` (a
  `ReportPaymentResult`-shaped endpoint).

Tracked in docs/exec-plans/tech-debt-tracker.md.
