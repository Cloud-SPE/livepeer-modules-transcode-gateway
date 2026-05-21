# Portal playground

The `<cc-playground>` component in `web/portal/`. Two tabs.

## Live tab

UI:
```
[ Create stream ]   (button — primary)

Once created:
  Stream ID:     live_…
  Ingest URL:    rtmp://<gateway>:1935/live
  Stream key:    lvk_…    (one-time, copyable)
  OBS hint:      → settings copy-pasta block

  Playback (<video>):  HLS player via hls.js
  [ Stop stream ] (button — destructive)
```

Behavior:
- `Create stream` calls `POST /api/v1/live` with `{name: "playground"}`.
- The stream key is shown plain only on this initial render; refresh
  redacts it.
- `<video>` is bound to the returned `hls_url`. hls.js (loaded from
  `https://esm.sh/hls.js@1`) handles the HLS playback. On Safari,
  fall back to native `<video src>` since Safari plays HLS natively.
- `Stop stream` calls `DELETE /api/v1/live/:id`, then GETs once more
  to show final status. OBS sees the disconnect within ~2s because
  the gateway closes the customer RTMP socket synchronously.

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
  Master playlist:  https://...../master.m3u8
  [ Copy URL ]    [ ▸ N variants ]
  Rendition:        [ select: 240p | 480p | 720p | 1080p ]
  Playback (<video>):  HLS player via hls.js

  Expanded variants (▸ → ▾):
    240p   2.5 Mbps    [ Play this one ] [ Copy URL ]
    480p   ...
    720p   ...

When failed:
  Job ID:     job_…
  Status:     failed
  ↳ error_code: <runner code, e.g. "preset_not_found">
    error:    <runner error string verbatim>
```

Behavior:
- Drop zone accepts a single video file (`accept="video/*"`).
- On drop: `POST /api/v1/abr/upload-url` → PUT bytes to S3 → on PUT
  200, `POST /api/v1/abr {input_url: object_url}` → poll
  `GET /api/v1/abr/:id` every 3s.
- Rendition selector lets the user override the auto-quality default
  by switching to a specific rendition playlist.
- **Copy URL** copies the master playlist URL to the clipboard.
  **▸ N variants** expands a per-rendition list with **Play this one**
  + **Copy URL** for each variant. Bitrates are formatted by the
  `formatBitrate` helper.
- **Errors are surfaced verbatim.** When a job fails, an inline
  sub-row shows the runner's raw `error_code` and `error` strings.
  There is no translation layer; the portal does not advise users to
  "re-encode locally" or interpret runner failures. The runner is the
  authoritative voice on what went wrong.

## Cross-cutting

- Uses the user's API key (passed at login) for all `/api/v1/*` calls.
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
