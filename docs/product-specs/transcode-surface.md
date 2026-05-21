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

See [`docs/design-docs/abr-pipeline.md`](../design-docs/abr-pipeline.md).

### `POST /api/v1/live`

See [`docs/design-docs/live-stream-pipeline.md`](../design-docs/live-stream-pipeline.md).

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
| 409 | `live_already_ended` | DELETE on an already-ended live session. |
| 429 | `rate_limit_exceeded` | Per-key token bucket exhausted. |
| 502 | `no_capable_broker` | No candidates returned for the requested capability. |
| 502 | `upstream_broker_error` | All candidates failed; last error attached. |
| 503 | `capabilities_cache_unavailable` | Registry refresh hasn't landed yet. |
| 503 | `payer_unavailable` | `payment-daemon` socket unreachable. |
| 503 | `registry_unavailable` | `service-registry-daemon` socket unreachable. |

## Rate limit

Per `api_key_id` token bucket: 60 / min, burst 30. Configurable via
`V1_RATE_LIMIT_PER_MINUTE` + `V1_RATE_LIMIT_BURST`. 429 returned with
`Retry-After` header.

## Out of scope (v1)

- VOD single-rendition transcode (`/api/v1/transcode`)
- Server-sent events / webhooks
- Gateway-side playback proxy
- Idempotency keys
- Per-key capability scoping
