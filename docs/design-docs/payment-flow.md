# Payment flow

How `Livepeer-Payment` envelopes get minted for `/api/v1/*` requests.

## VOD (single-attempt)

```
client → POST /api/v1/abr
  gateway opens usage_reservations
  gateway calls Resolver.SelectMany(capability=livepeer:transcode/abr-ladder)
  for each candidate (loop):
    gateway calls PayerDaemon.CreatePayment(face_value, recipient, capability, offering)
    gateway POSTs to broker with Livepeer-Payment header
    if 2xx → commit reservation, return job descriptor
    if 5xx/timeout → mark broker unhealthy, try next candidate, mint a NEW payment
```

A single client request can mint multiple envelopes if failover
happens. Each envelope is a real on-chain commitment — the payer
daemon has signed and recorded it.

`face_value` is derived from the candidate's `price_per_work_unit_wei`
× `estimated_work_units`. For ABR ladder, we estimate work units from
input duration × output ladder size (rough; refined when the runner
reports actual work).

## Live (session-bound)

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
| `gateway/internal/proxy/livepeer/payment.go` | gRPC client to `payment-daemon`; `MintEnvelope(...)` is the only callsite. |
| `gateway/internal/proxy/abr.go` | Calls `MintEnvelope` per attempt; loops failover candidates. |
| `gateway/internal/proxy/live.go` | Calls `MintEnvelope` once on session-open; relies on broker-side interim debits. |
| `gateway/internal/proxy/livepeer/headers.go` | Attaches the envelope to outbound broker requests as `Livepeer-Payment`. |

## Failure semantics

- **Payer daemon unreachable** → 500, reservation refunded, no broker
  call.
- **`face_value` too small** → broker rejects with 402; gateway
  surfaces the error and refunds.
- **Mid-attempt RPC failure** → log the failure with the work_id;
  the reservation stays in `open` until the next sweep refunds it.
- **Live session balance exhaustion** → broker closes the RTMP;
  `live_streams.status='ended'`, `usage_reservations.state='committed'`
  with whatever work units accrued.

## What this doc does not cover

- The ticket math itself — see go-livepeer's `pm` package and the
  `payee_daemon.proto` types.
- Operator-side keystore funding — see [`../../DEPLOYMENT.md`](../../DEPLOYMENT.md).
