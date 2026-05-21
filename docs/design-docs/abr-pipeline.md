# ABR pipeline

The `/api/v1/abr` flow end-to-end.

## Surface

- `POST /api/v1/abr/upload-url` — request a presigned MinIO PUT for VOD ingest
- `POST /api/v1/abr` — submit a transcode job
- `GET /api/v1/abr/:job_id` — poll job status + master playlist URL

## Request shape

```json
POST /api/v1/abr
{
  "input_url": "https://.../source.mp4",
  "ladder": "default" | {
    "rungs": [
      { "name": "240p",  "width": 426,  "height": 240,  "bitrate_kbps": 250 },
      { "name": "480p",  "width": 854,  "height": 480,  "bitrate_kbps": 500 },
      { "name": "720p",  "width": 1280, "height": 720,  "bitrate_kbps": 1000 },
      { "name": "1080p", "width": 1920, "height": 1080, "bitrate_kbps": 2500 }
    ]
  },
  "callback_url": null
}
```

`ladder = "default"` resolves to whatever the abr-runner advertises
as its default in the registry manifest. v1 ignores `callback_url`
(poll-only).

## Response shape

```json
{
  "id": "job_<uuid>",
  "status": "running",
  "input_url": "https://.../source.mp4",
  "output": {
    "master_playlist_url": null,
    "renditions": []
  },
  "created_at": "2026-05-19T20:00:00Z",
  "broker_url": "https://broker.example.com",
  "eth_address": "0x…"
}
```

When the runner finishes:

```json
{
  "id": "job_<uuid>",
  "status": "succeeded",
  "output": {
    "master_playlist_url": "https://runner.example.com/abr/<id>/master.m3u8",
    "renditions": [
      { "name": "240p",  "playlist_url": "…/240p.m3u8",  "bandwidth": 250000 },
      …
    ]
  },
  "started_at": "…",
  "completed_at": "…"
}
```

## Step-by-step

1. **Client requests upload URL.** Optional. The gateway presigns
   `PUT /lvp-video-ingest/abr/<api_key_id>/<uuid>/<filename>` against
   MinIO for `S3_PRESIGN_TTL_SECONDS` (default 1h). Returns
   `{upload_url, object_url}`.
2. **Client uploads bytes to MinIO.** Direct PUT, no gateway in path.
3. **Client submits job.** `POST /api/v1/abr` with `input_url` (either
   the MinIO object URL or any HTTPS URL the runner can fetch).
4. **Gateway opens `usage_reservations`.** State=`open`,
   `capability='livepeer:transcode/abr-ladder'`,
   `offering='default'` (or the custom ladder id).
5. **Route selection.** `RouteSelector.SelectMany(...)` returns
   ranked candidates.
6. **Failover loop.** For each candidate: mint envelope → POST to
   broker with `Livepeer-Capability`, `Livepeer-Payment`, mode
   `http-reqresp@v0`, body = the job request.
7. **Broker dispatches to abr-runner.** Runner returns `{job_id,
   master_playlist_url}` (async runners may return `{job_id,
   status_url}` first and populate `master_playlist_url` once
   transcoding finishes).
8. **Gateway commits `usage_reservations`.** State=`committed`,
   `committed_work_units` populated.
9. **Client polls `GET /api/v1/abr/:id`** until `status='succeeded'`,
   then loads `master_playlist_url` in a player.

## Failure modes

| What | Behavior |
|---|---|
| `input_url` unreachable from runner | Runner returns 4xx; gateway propagates 4xx; reservation refunded. |
| Runner crashes mid-transcode | Broker returns 5xx after timeout; failover triggers; new envelope minted. |
| All candidates exhaust | Gateway returns 502; last error_text recorded in reservation. |
| Presigned URL expired | Client receives S3 error on PUT; gets a fresh URL via `/api/v1/abr/upload-url`. |

## What this doc does not cover

- The runner-side ABR ladder execution — owned by `livepeer-network-modules/video-runners/abr-runner`.
- The broker's `http-reqresp@v0` mode spec — owned by `livepeer-network-protocol/modes/http-reqresp.md`.
