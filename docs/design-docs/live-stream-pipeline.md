# Live stream pipeline

The `/v1/live` flow end-to-end.

## Surface

- `POST /v1/live` — allocate an RTMP ingest + HLS egress session
- `GET /v1/live/:id` — poll session status
- `DELETE /v1/live/:id` — close the session

## Request shape

```json
POST /v1/live
{
  "name": "my-stream",
  "ladder": "default" | {
    "rungs": [
      { "name": "source", "passthrough": true },
      { "name": "720p",   "width": 1280, "height": 720,  "bitrate_kbps": 2500 },
      { "name": "480p",   "width": 854,  "height": 480,  "bitrate_kbps": 1000 },
      { "name": "240p",   "width": 426,  "height": 240,  "bitrate_kbps": 400 }
    ]
  }
}
```

## Response shape

```json
{
  "id": "live_<uuid>",
  "status": "live",
  "ingest": {
    "rtmp_url": "rtmp://broker.example.com:1935/live",
    "stream_key": "lvk_…"
  },
  "playback": {
    "hls_url": "https://broker.example.com/live/<broker-session-id>/master.m3u8"
  },
  "created_at": "…",
  "started_at": null,
  "ended_at": null
}
```

`stream_key` is returned exactly once. The hashed form lives in
`live_streams.stream_key_hash`.

## Step-by-step

1. **Client POSTs `/v1/live`.** Gateway opens a long-lived
   `usage_reservations` row (state=`open`) and an associated
   `live_streams` row (status=`provisioning`).
2. **Route selection.** `Resolver.SelectMany(capability=
   'video:transcode.live', …)` returns ranked
   candidates. Pick the top one (no failover on session-open).
3. **Mint envelope.** `face_value` sized for the session's initial
   estimated work units (configurable via ladder size + an internal
   default).
4. **Broker OpenSession.** Gateway POSTs to broker with mode
   `rtmp-ingress-hls-egress@v0`. Broker returns
   `{session_id, rtmp_url, stream_key, hls_url}`. Gateway updates
   `live_streams` to `status='live'` and stores hashed stream key.
5. **Client pushes RTMP.** OBS / ffmpeg / streamlabs / etc. point
   at `rtmp_url` with `stream_key`. Broker authenticates against the
   provisioned session.
6. **Broker drives interim debits.** As work units accrue,
   `payment-daemon.Debit(session_id, units)` is called. Gateway sees
   updated `live_streams.last_heartbeat_at` via broker pings.
7. **Client polls `GET /v1/live/:id`.** Returns
   `{status, playback, started_at, ended_at}`. Polling cadence is
   client-driven (suggested: 5s while provisioning, 30s while live).
8. **Client DELETEs.** Gateway calls broker `CloseSession`, settles
   via payment-daemon, updates `live_streams.status='ended'` and
   `usage_reservations.state='committed'`.

## States

```
provisioning  → live    (broker accepted session-open + ingest connected)
provisioning  → failed  (broker rejected; refund)
live          → ended   (client DELETE, balance exhausted, or runner crash)
```

## Stream key authentication

The plaintext `stream_key` is returned once. The broker validates it
on RTMP connect. The gateway holds only `stream_key_hash` (peppered
SHA-256 with `IP_HASH_PEPPER`). If a client loses the key, they
create a new live stream — there is no key-rotation surface in v1.

## Failure modes

| What | Behavior |
|---|---|
| Broker rejects session-open | `status='failed'`, reservation refunded, 502 to client. |
| RTMP push fails authentication | Broker-side reject; `live_streams.status` stays `provisioning` until cleanup. |
| Mid-stream broker crash | Stream ends. Client must allocate a new session. |
| Balance exhausted | Broker tears down RTMP; gateway sees status update via heartbeat; `status='ended'`. |
| Client never pushes RTMP | Session stays `provisioning`; janitor task closes it after `LIVE_PROVISIONING_TTL` (TBD; tracked in tech-debt). |
| Client never DELETEs | Session stays `live` until balance exhausts or operator force-closes. |

## What this doc does not cover

- The broker's `rtmp-ingress-hls-egress@v0` mode internals — owned by
  `livepeer-network-modules/capability-broker`.
- LL-HLS chunk timing and player compatibility — broker concern.
- Gateway-side playback proxy — out of scope in v1.
