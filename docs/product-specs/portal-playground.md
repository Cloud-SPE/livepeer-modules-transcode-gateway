# Portal playground

The `<cc-playground>` component in `web/portal/`. Two tabs.

## Live tab

UI:
```
[ Create stream ]   (button — primary)

Once created:
  Stream ID:     live_…
  Ingest URL:    rtmp://broker.example.com:1935/live
  Stream key:    lvk_…    (one-time, copyable)
  OBS hint:      → settings copy-pasta block

  Playback (<video>):  HLS player via hls.js
  [ Stop stream ] (button — destructive)
```

Behavior:
- `Create stream` calls `POST /v1/live` with `{name: "playground"}`.
- The stream key is shown plain only on this initial render; refresh
  redacts it.
- `<video>` is bound to the returned `hls_url`. hls.js (loaded from
  `https://esm.sh/hls.js@1`) handles the LL-HLS playback. On Safari,
  fall back to native `<video src>` since Safari plays HLS natively.
- `Stop stream` calls `DELETE /v1/live/:id`, then GETs once more to
  show final status.

## Transcode tab

UI:
```
[ Drop file or click to upload ]   (drop zone)

While uploading:
  filename                                progress bar  XX%

After upload, while running:
  Job ID:     job_…
  Status:     queued | running
  (poll)

When succeeded:
  Master playlist:  https://runner.example.com/abr/.../master.m3u8
  Rendition:        [ select: 240p | 480p | 720p | 1080p ]
  Playback (<video>):  HLS player via hls.js
```

Behavior:
- Drop zone accepts a single video file (`accept="video/*"`).
- On drop: `POST /v1/abr/upload-url` → PUT bytes to S3 → on PUT
  200, `POST /v1/abr {input_url: object_url}` → poll
  `GET /v1/abr/:id` every 3s.
- Rendition selector lets the user override the auto-quality default
  by switching to a specific rendition playlist.

## Cross-cutting

- Uses the user's API key (passed at login) for all `/v1/*` calls.
  The portal stores the bearer key in memory after login — not in
  localStorage — for the lifetime of the session.
- hls.js loaded via importmap: `"hls.js": "https://esm.sh/hls.js@1"`.
- All HLS playback gracefully handles 404 / network errors with an
  inline error state.

## Acceptance

- Live tab: from `Create stream` to first video frame ≤ 30s assuming
  a downstream broker accepts the session and the user starts pushing
  RTMP within 15s.
- Transcode tab: from drop to first playback frame is bounded by the
  runner. The UI shows progress at each phase (upload / queue /
  transcode).
- Both tabs work in Chrome + Safari + Firefox. Edge cases like
  Safari's native HLS player are tested manually.
