# Transcode surface

The `/api/v1/*` API. Custom shape (not Livepeer Studio-compatible).

## Routes

| Method | Path | Auth | Behavior |
|---|---|---|---|
| POST | `/api/v1/abr/upload-url` | Bearer | Returns a presigned MinIO PUT URL for VOD ingest. |
| POST | `/api/v1/abr` | Bearer | Submit an ABR ladder transcode job. |
| GET | `/api/v1/abr/:id` | Bearer | Poll job status + master playlist URL. |
| POST | `/api/v1/live` | Bearer | Allocate an RTMP ingest + HLS egress session. |
| GET | `/api/v1/live/:id` | Bearer | Poll live session status. |
| DELETE | `/api/v1/live/:id` | Bearer | Close a live session (synchronous RTMP teardown). |
| GET | `/api/v1/capabilities` | Bearer | List active transcode capabilities advertised by the network. |

OpenAPI: `GET /openapi.json` + `GET /docs` (huma-generated).

## Bodies

### `POST /api/v1/abr/upload-url`

```json
Request:  { "filename": "input.mp4", "content_type": "video/mp4" }
Response:
{
  "upload_url": "https://minio.example.com/lvp-video-ingest/abr/<key_id>/<uuid>/input.mp4?X-Amz-Signature=…",
  "object_url": "https://minio.example.com/lvp-video-ingest/abr/<key_id>/<uuid>/input.mp4",
  "expires_at": "2026-05-19T21:00:00Z"
}
```

### `POST /api/v1/abr`

See the generated `/openapi.json` for request fields and [Modules v2](../design-docs/modules-v2.md) for the paid-job workload boundary.

New ABR jobs authorize the duration-based estimate plus 25% headroom, rounded
up and capped by `ABR_MAX_TOTAL_UNITS`. The estimate sums the preset's video
rendition pixels × 60 fps × `estimated_input_seconds`, divides by one million,
and rounds up once. Omitted/zero duration uses 60 seconds. Audio adds no units.
Provide an accurate duration rounded up; for higher frame rates or uncertain
inputs, specify `max_total_units` explicitly. An explicit cap must cover the
estimate and stay within the server ceiling. These are authorization bounds,
not measured charges or a guarantee that an underestimated input can complete.
Existing jobs retain their persisted bounds, including on idempotent retries.

### `GET /api/v1/abr/:id`

`status=admission_rejected`, `phase=settlement_pending`, and
`error_code=broker_admission_rejected` mean the broker refused admission and
LOC has not yet finalized accounting. Keep polling; this is not a successful
transcode, a confirmed refund, or permission to submit a replacement automatically.
The gateway polls recovery without replaying a definitively rejected workload.
After LOC confirms terminal `NOT_ADMITTED`, status becomes `failed` with
`error_code=not_admitted`; `accounting_state` remains the authoritative LOC
accounting outcome. Playback links appear only for verified success.
The admin listing exposes `status`, `accounting_state`, and `error_code`
separately from the reservation's legacy `state`.

### `POST /api/v1/live`

See the generated `/openapi.json` for request fields and [Modules v2](../design-docs/modules-v2.md) for session descriptors and grants.

### `GET /api/v1/capabilities`

See [`docs/design-docs/capability-catalog.md`](../design-docs/capability-catalog.md).

## Errors

All errors follow huma's RFC 9457 problem+json shape:

```json
{
  "type": "https://livepeer-modules-transcode-gateway/errors/<code>",
  "title": "human-readable",
  "status": 4xx | 5xx,
  "detail": "what went wrong",
  "instance": "/api/v1/abr"
}
```

| Status | Code | When |
|---|---|---|
| 401 | `invalid_api_key` | Missing or revoked Bearer key. |
| 403 | `key_not_approved` | Key exists but `waitlist.status != 'approved'`. |
| 404 | `not_found` | `/api/v1/abr/:id` / `/api/v1/live/:id` doesn't exist. |
| 429 | `rate_limit_exceeded` | Per-key token bucket exhausted. |
| 503 | `loc_unavailable` | LOC is not configured or cannot authorize work. |

Upstream authorization, protocol and recovery errors are reported by the
current handlers. The generated OpenAPI schema is the definitive request
and response contract; legacy daemon errors and v0 interaction modes no
longer describe this API.

## Rate limit

Per `api_key_id` token bucket: 60 / min, burst 30. Configurable via
`V1_RATE_LIMIT_PER_MINUTE` + `V1_RATE_LIMIT_BURST`. 429 returned with
`Retry-After` header.

## Out of scope (v1)

- VOD single-rendition transcode (`/api/v1/transcode`)
- Server-sent events / webhooks
- Gateway-side playback proxy
- Per-key capability scoping

### Live termination and settlement

Live responses expose `settlement_pending`. Once broker termination or terminal
evidence confirms media has ended, status becomes `ended` or `failed`, `ended_at`
is recorded, ingest is closed, and stream-key recovery is disabled. Accounting
may still be pending: the durable operation remains queued until LOC validates
final evidence. Usage is neither committed nor refunded merely because media
ended. Portal history and the selected stream continue polling pending settlement.
A definitive broker `refill_refused` stops replaying that revision and requests
termination; ambiguous failures retain idempotent recovery. See bead `vgw-e2o`.

Live session detail and portal history also expose `status_message` (empty when
there is no startup diagnostic). While provisioning retries an upstream failure,
the portal displays a curated explanation instead of the ordinary preparation
message. Broker authorization refusals are distinguished from general upstream
delays. Raw upstream bodies and private recovery data are never exposed, and a
generic authorization rejection does not imply a specific credit shortfall.
Messages clear on successful recovery or Stop; retry and accounting semantics
remain unchanged. Tracked in `vgw-cq9`.

Stop during provisioning (`vgw-15z`) prevents fresh broker opens. The gateway
queries the original request's exchange: a confirmed rejected admission ends
media without finalizing accounting; a confirmed existing session permits
idempotent credential recovery followed by End. Missing or ambiguous evidence
keeps cancellation pending with an explanatory message. Before any authorization
attempt, a queued intent can be canceled locally. Once authority may have been
issued, reservations remain until LOC confirms final accounting.
