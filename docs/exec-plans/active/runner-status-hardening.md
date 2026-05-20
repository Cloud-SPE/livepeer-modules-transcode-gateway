---
title: live-runner status + control-plane hardening (upstream change)
status: tracking
opened: 2026-05-20
owner: runner-team (livepeer-modules-transcode-runners)
related:
  - "0003-gateway-rtmp-ingest.md"
---

# live-runner status / control-plane hardening

Tracking the runner team's expansion of the original change request. This
work is **upstream**; the gateway only needs to consume the richer surface.
Captured here so the integration plan stays in one place.

## Scope (runner team will deliver)

### 1. Explicit session state machine

State enum:

- `provisioning` — session accepted, resources allocated, ffmpeg not yet ready
- `ready` — RTMP listener bound and accepting publish
- `publishing` — valid publish authenticated, media flowing
- `uploading` — uploader actively syncing outputs to S3 (may overlap with publishing)
- `stalled` — was publishing, media flow stopped past a threshold
- `ending` — shutdown requested
- `ended` — clean shutdown complete
- `failed` — unrecoverable error

Notes:

- `ready` MUST only emit after the RTMP listener is actually bound (fixes
  the existing runner-bind bug we hit earlier — silent state transition
  from "session created" to "ready" without verifying the bind succeeded).
- `publishing` MUST be driven by confirmed ingest activity, not process
  liveness alone.

### 2. Richer status endpoint shape

`GET /v1/video/live/sessions/{rsess_id}` response adds:

```json
{
  "ingest": {
    "listener_bound": true,
    "authenticated": true,
    "stream_key_suffix": "3ZBv",
    "connected_publisher": true,
    "last_packet_at": "..."
  },
  "output": {
    "mode": "s3_push",
    "target_prefix": "live-out/<api_key>/<live_id>/",
    "last_manifest_put_at": "...",
    "last_segment_put_at": "...",
    "put_success_count": 42,
    "put_failure_count": 0,
    "last_put_error": null
  },
  "usage_total": 123,
  "usage_unit": "output_seconds"
}
```

Required new fields at minimum:

- `ingest.listener_bound`
- `ingest.connected_publisher`
- `ingest.last_packet_at`
- `output.last_manifest_put_at`
- `output.last_segment_put_at`
- `output.put_failure_count`
- `output.last_put_error`

### 3. Idempotent broker-callback events

- `session.ready` — listener bound, awaiting publish
- `session.publish_started` — authenticated publish began
- `session.publish_stopped` — publisher disconnected or stalled
- `session.upload.healthy` — S3 puts succeeding
- `session.upload.failed` — repeated S3 failures past threshold
- `session.ended`
- `session.failed`

Do not overload `session.started` to mean both "listener ready" and "media
flowing." They are distinct operational states.

### 4. Deterministic stop semantics

`DELETE /v1/video/live/sessions/{id}` MUST guarantee:

- ingest listener closed
- ffmpeg subprocess terminated
- uploader goroutines drained or cancelled
- final manifest flushed if possible
- terminal status becomes `ended` or `failed`
- repeated DELETE is safe + idempotent

### 5. Two-stage stall detection

- `SESSION_NO_PUBLISH_TTL` — no valid publish after `ready`
- `SESSION_IDLE_TTL` — publish started, media stopped

Transient blips should transition `publishing` → `stalled` first, then
optionally to `failed`/`ended`. Don't terminate on the first missed packet.

### 6. State derived from multiple signals (not stderr parsing)

- RTMP listener bind confirmation
- publisher connection/auth result
- packet-flow timestamps
- ffmpeg process liveness
- uploader success/failure timestamps
- optional manifest-mutation timestamps

ffmpeg stderr can remain a signal, but not the only one.

### 7. Redaction + metrics

Redact in logs and status:

- full `stream_key`
- `access_key_id` (suffix only)
- `secret_access_key`
- `session_token`

New runner-side metrics:

- `live_runner_sessions_ready`
- `live_runner_sessions_publishing`
- `live_runner_sessions_stalled`
- `live_runner_ingest_auth_rejections_total`
- `live_runner_s3_put_success_total`
- `live_runner_s3_put_failure_total`
- `live_runner_ffmpeg_exit_total`

## What the gateway needs to do to consume this

Minimal:

- Our existing `LiveGetResponse` (in `gateway/internal/proxy/livepeer/live_session.go`)
  uses JSON loose-unmarshal; extra fields are tolerated. No code change to
  read them, just nothing-yet-consumed.
- Reconciler's `mapBrokerState()` in `handlers_v1.go` already maps the
  current four states. When the new `stalled`, `uploading`, `ready`,
  `ending` arrive on the wire, add cases:
  - `ready` → `provisioning` (no media yet)
  - `publishing` → `live`
  - `uploading` → `live` (overlaps with publishing)
  - `stalled` → `live` with surfaced `close_reason="stalled"`-style warning
  - `ending` → `live` (transient terminal)
  - `ended` → `ended`
  - `failed` → `failed`

Future (when worthwhile):

- Surface `ingest.connected_publisher` + `last_packet_at` to the portal
  Playground so customers can see "publishing" vs "ready but no signal".
- Surface `output.put_failure_count` to the admin live-streams view so
  operators can see S3 upload health.

## Recommended sequencing (runner team's proposal)

1. status/control hardening
2. single-port multi-stream ingest
3. S3 uploader path
4. compatibility wiring for old mode

The gateway-side phases (1–8 in `0003-gateway-rtmp-ingest.md`) can land
in parallel; integration tests are gated on the runner team completing #1
+ #3 at minimum.

## Why this matters for our plan

The new gateway-ingest mode adds a hard dependency on the runner being
honest about its own state. The old mode could ship with the gateway
pessimistically assuming "broker said ready, but verify via HEAD probe of
master.m3u8." With gateway-ingest, the gateway is _driving_ the RTMP
session lifecycle and needs to know precisely when the runner is ready
to accept bytes (before we open the upstream RTMP push) and when it
stops (so we can fail the customer's session cleanly). Without the
state-machine hardening, our gateway's relay would be racy and our
status surface would lie to customers.

So: the gateway phases can proceed against the existing wire (with
graceful degradation), but ABRP-grade operational quality requires the
runner team's hardening to land before we flip `LIVE_INGEST_MODE_DEFAULT`
to `gateway_ingest` in production.
