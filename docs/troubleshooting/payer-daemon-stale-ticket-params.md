# Payer-daemon: stale `TicketParams` after receiver-side rotation

Operator-side recipe for a recurring class of failure where the
receiver-side payment-daemon rejects our minted tickets mid-session
with a cryptographic mismatch — because our payer-daemon is signing
against `TicketParams` it cached before the receiver rotated state.

> **Fixed in `tztcloud/livepeer-payment-daemon:v1.3.1` (2026-05-20).**
> Receiver now persists session state across restart, `GetTicketParams`
> is idempotent for the lifetime of an open session, and the
> `ReportPaymentResult` RPC evicts the sender-side cached session +
> returns `Aborted` on `INVALID_RECIPIENT_RAND`. Our gateway is on
> v1.3.1; this runbook applies only when one of these is true:
>
> - You're on a payer-daemon < v1.3.1 (downgrade, fork, or stale pin)
> - The receiver side (xodeapp's `payee-daemon`) hasn't yet upgraded —
>   the fix is end-to-end and needs **both** sides on v1.3.1
>
> If you're on v1.3.1 both sides and STILL see this WARN, that's a
> regression worth reporting upstream. The retired-recipe content below
> is kept for historical reference / for stacks pinned to older images.

---

## Symptoms

In the receiver's log (orchestrator-side, you'll see this if you have
access to it):

```
level=WARN msg="invalid ticket; skipping"
  work_id=<32-byte hex>  nonce=<N>
  err="validator: invalid recipientRand for recipientRandHash"
level=INFO msg="payment processed"
  tickets=1  credited_ev_wei=0  winners_queued=0  balance_wei=0
```

Tell-tale signs:

- `nonce` is **greater than 1** — earlier tickets in the same session
  validated cleanly, then suddenly stopped
- `credited_ev_wei=0` — receiver saw the payment but credited nothing
- Pattern repeats for every subsequent `/v1/abr` until you intervene

From the gateway's side everything looks fine: the broker returns 2xx,
reservations commit, the playground keeps showing `processing`. The
runner never starts work because it has zero credit.

---

## Diagnosis

The v1.3.0 `payment-daemon` (sender mode) caches `TicketParams` per
`(recipient, capability)` after its first `GetTicketParams` call.
Subsequent `CreatePayment` calls reuse those params and increment the
ticket `nonce` (1, 2, 3, …). What it does **not** do is invalidate the
cache when a payment is rejected as "invalid recipientRand" — there's
no out-of-band signal back to the sender saying "rotate your params."

Triggers on the receiver side that cause rotation:

| Receiver event | What changes |
|---|---|
| Their `payment-daemon` container restarts (deployment, host reboot, runner reinstall) | Fresh `recipientRand` generated for our sender address; old hash invalidated |
| Livepeer protocol round advances (Arbitrum, ~22h cycle) | Their TicketBroker session can rotate, depending on their config |
| Operator-side state reset (manual `mc admin policy reload`, etc.) | Same |

Our daemon doesn't know any of these happened. It keeps minting
against the pre-rotation `seed` / `recipient_rand_hash`. The receiver
hashes its current `recipientRand` and gets a different value than
what our ticket claims, so it rejects.

---

## Fix

Bounce our payer-daemon to force a fresh `GetTicketParams` on the next
`CreatePayment`:

```bash
docker compose stop payer-daemon
docker compose rm -f payer-daemon
docker compose --profile livepeer up -d payer-daemon
```

`docker compose restart payer-daemon` is **not** enough — that keeps
the container's writable layer (including the cache state in
`/var/lib/livepeer/payment-daemon/sessions.db`). The `rm -f` between
stop and up wipes the layer.

After the restart, the next `/v1/abr` will see `nonce=1` in a fresh
session, the receiver validates cleanly, EV credits resume.

### Verify the fix took

```bash
# Submit any /v1/abr through the portal Playground or:
curl -X POST http://localhost:4000/v1/abr \
  -H "Authorization: Bearer sk-…" \
  -d '{"input_url":"…","preset":"abr-standard","estimated_input_seconds":10}'

# Payer-daemon log should show nonce=1 on a brand-new work_id:
docker compose logs --since=20s payer-daemon | grep "payment created"
# Expected:
#   payment created … work_id=<NEW>… capability=video:transcode.abr nonce=1
```

If you have access to the receiver-side log, you should see:

```
INFO msg="payment processed" tickets=1 credited_ev_wei=<non-zero>
```

instead of `credited_ev_wei=0`.

---

## Why this is operator-side, not a gateway bug

The gateway is doing exactly what the v1.3.0 daemon API expects: hand
in `(recipient, capability, offering, accepted_price, funding)` and
trust the daemon to manage ticket params + nonces. The cache-
invalidation gap was inside the daemon binary
(`tztcloud/livepeer-payment-daemon:v1.3.0`).

The proper long-term fix landed upstream — see "Upstream fix"
admonition at the top. The runbook stays here until the new tagged
image is pulled into our compose stack.

---

## When you'd routinely see this

- Iterating with an orchestrator that's actively under construction
  (CUDA driver swaps, preset config rebuilds, daemon reinstalls)
- After any planned receiver-side maintenance window
- ~Daily, if the receiver's daemon happens to bounce on protocol round
  events

For a stable production orch, this shouldn't fire more than a couple
of times per week.

---

## Cross-references

- [`runner-cuda-driver-mismatch.md`](./runner-cuda-driver-mismatch.md) — operator-side rebuilds (CUDA / Keylase) that often trigger this rotation
- [`../design-docs/payment-flow.md`](../design-docs/payment-flow.md) — the gateway-side mint path this guide complements
