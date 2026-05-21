# Live stream pipeline

The `/api/v1/live` flow end-to-end. Mode is
`live-session-gateway-ingest@v0`: the gateway owns the public RTMP
endpoint at `:1935`, authenticates the customer's stream key against
`live_streams.stream_key_hash`, and relays RTMP to the orchestrator's
private ingest URL. The runner writes HLS directly to gateway-owned
MinIO using short-lived STS credentials.

## Surface

- `POST /api/v1/live` — allocate an RTMP ingest + HLS egress session
- `GET /api/v1/live/:id` — poll session status
- `DELETE /api/v1/live/:id` — close the session (synchronous RTMP teardown)

## Request shape

```json
POST /api/v1/live
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
    "rtmp_url": "rtmp://<gateway>:1935/live",
    "stream_key": "lvk_…"
  },
  "playback": {
    "hls_url": "https://<our-s3-or-cdn>/lvp-video-ingest/live-out/<api_key>/<live_id>/master.m3u8"
  },
  "created_at": "…",
  "started_at": null,
  "ended_at": null
}
```

`stream_key` is returned exactly once. The hashed form lives in
`live_streams.stream_key_hash`.

## Step-by-step

1. **Client POSTs `/api/v1/live`.** Gateway generates the stream key,
   stores its peppered hash in `live_streams.stream_key_hash`, opens a
   long-lived `usage_reservations` row (state=`open`), and creates the
   `live_streams` row (status=`provisioning`).
2. **Route selection.** `Resolver.SelectMany(capability=
   'video:transcode.live', offering='gateway-ingest')` returns ranked
   candidates. Pick the top one (no failover on session-open).
3. **Mint STS credentials.** `s3.MintLiveSessionCredentials(...)` calls
   MinIO STS `AssumeRole` with an inline policy scoped to
   `live-out/<api_key>/<live_id>/*` (PutObject / DeleteObject /
   AbortMultipartUpload only). Lifetime = `LIVE_S3_CREDENTIAL_TTL_HOURS`.
4. **Mint envelope.** `face_value` sized for the session's initial
   estimated work units (configurable via ladder size + an internal
   default).
5. **Broker OpenSession.** Gateway POSTs to broker `/v1/cap` with
   mode `live-session-gateway-ingest@v0`. Body carries the STS
   credentials (so the runner can write HLS) and the customer's
   stream key (so the runner can authenticate the gateway's upstream
   RTMP push). Broker returns
   `{broker_session_id, private_ingest_url}`. Gateway updates
   `live_streams` to `status='live'`, persists
   `private_ingest_url` + `s3_output_prefix`, and computes the
   public-facing `playback.hls_url`.
6. **Client pushes RTMP.** OBS / ffmpeg / streamlabs / etc. point
   at `rtmp://<gateway>:1935/live/<stream_key>`. The gateway's RTMP
   server authenticates the key against `stream_key_hash` and opens
   an upstream RTMP push to `private_ingest_url`. FLV tags are
   relayed verbatim.
7. **Runner writes HLS.** Runner re-encodes and PUTs HLS segments +
   manifests directly to MinIO under the scoped prefix. MinIO
   enforces the STS policy server-side.
8. **Broker drives interim debits.** As work units accrue,
   `payment-daemon.Debit(session_id, units)` is called. The
   reconciler (`live_reconciler.go`, cadence
   `LIVE_RECONCILE_INTERVAL_SECS`) polls the broker for runner status
   each tick and caches the result in `live_streams.runner_status_json`
   for the admin UI. Auto-topup mints a new envelope when reported
   runway falls below `LIVE_TOPUP_RUNWAY_THRESHOLD_SECS`.
9. **Client polls `GET /api/v1/live/:id`.** Returns
   `{status, playback, started_at, ended_at, ...}`. Polling cadence
   is client-driven (suggested: 5s while provisioning, 30s while live).
10. **Client DELETEs.** Gateway synchronously closes the customer's
    RTMP socket + the upstream relay push (`RTMPProbe.CloseSession`)
    so OBS sees disconnect in ~2s, then calls broker `CloseSession`,
    settles via payment-daemon, updates `live_streams.status='ended'`
    and `usage_reservations.state='committed'`.

## States

```
provisioning  → live    (broker accepted session-open + ingest connected)
provisioning  → failed  (broker rejected; refund)
live          → ended   (client DELETE, balance exhausted, or runner crash)
```

## Stream key authentication

The plaintext `stream_key` is returned once. The gateway's RTMP
server validates it on customer-side RTMP connect by peppered-hashing
the published key and looking up
`live_streams.stream_key_hash`. The gateway holds only the hash
(peppered SHA-256 with `IP_HASH_PEPPER`). If a client loses the key,
they create a new live stream — there is no key-rotation surface in
v1.

## Failure modes

| What | Behavior |
|---|---|
| Broker rejects session-open | `status='failed'`, reservation refunded, 502 to client. |
| RTMP push fails authentication | Gateway closes the customer's TCP socket; `live_streams.status` stays `provisioning` until cleanup. |
| Mid-stream upstream RTMP failure | Gateway closes the customer's TCP socket too; stream ends. Client must allocate a new session. (v1 has no soft failover.) |
| Balance exhausted | Broker emits `insufficient_balance` close reason; reconciler transitions `status='ended'`. |
| Client never pushes RTMP | Session stays `provisioning`; janitor task closes it after `LIVE_PROVISIONING_TTL` (TBD; tracked in tech-debt). |
| Client never DELETEs | Session stays `live` until balance exhausts or operator force-closes. |

## What this doc does not cover

- The `live-session-gateway-ingest@v0` wire spec — owned by
  `livepeer-network-modules/livepeer-network-protocol/modes/live-session-gateway-ingest.md`.
- LL-HLS chunk timing and player compatibility — runner concern.
- Gateway-side playback proxy — out of scope in v1.
