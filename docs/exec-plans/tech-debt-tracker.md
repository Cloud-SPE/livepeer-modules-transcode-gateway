# Tech debt tracker

Known debt and deferred follow-ups. When a plan can't be done now
but is the right thing to do later, it lands here. When the debt
is paid, the row gets crossed out + the plan goes into
`completed/`.

| Item | Priority | Trigger | Notes |
|---|---|---|---|
| `cloudflared` tunnel for the gateway so the webhook receiver works against external runners. | high | now | The webhook receiver is live at `POST /api/abr/callback` and the gateway passes `webhook_url`+`webhook_secret` to the runner whenever `GATEWAY_PUBLIC_URL` is set. For remote runners (us-central-worker.xodeapp.xyz et al.) that URL must be publicly reachable. Wire `cloudflared` into compose pointing at `gateway:4000`, set `GATEWAY_PUBLIC_URL` to its hostname on each `make dev`, and runner errors surface as `runner_status=error` automatically. |
| ~~Bump `tztcloud/livepeer-payment-daemon` image tag once the upstream session-persistence release is published.~~ **DONE 2026-05-20.** | — | — | Bumped to `v1.3.1` (`.env`: `LIVEPEER_PAYER_DAEMON_TAG=v1.3.1`, `docker-compose.yml` default also `v1.3.1`). Confirms with `version=v1.3.1-bdeffb372c50-dirty mode=sender` on boot. Vendored proto-go bindings already match. Still pending: confirm `xodeapp` upgrades their capability-broker + receiver-side payment-daemon to v1.3.1 — the new error-flow + restart-stable sessions only work end-to-end if BOTH sides are on v1.3.1. Validation test (kill-receiver mid-session, confirm credit survives) pending xodeapp upgrade. |
| ~~Migrate `/v1/live` to `live-session-remote-runner@v0`.~~ **DONE 2026-05-20.** | — | — | Six phases shipped: migration 0005, new broker client, POST/GET/DELETE rewrite, reconciler + auto-topup. See [`completed/0002-live-session-remote-runner.md`](./completed/0002-live-session-remote-runner.md). End-to-end smoke pending xodeapp publishing the `video:transcode.live` capability — until then `/v1/live` returns `registry_select_failed`, which is the upstream-side condition. |
| ~~Add gateway-ingest mode (live-session-gateway-ingest@v0).~~ **DONE 2026-05-20.** | — | — | Eight phases shipped: migration 0006, RTMP server, S3 credential minter, broker client, handler dispatch, RTMP relay, lifecycle/health/metrics, SPA. Mode discriminated by offering (`default` vs `gateway-ingest`) under the shared `video:transcode.live` capability. See [`active/0003-gateway-rtmp-ingest.md`](./active/0003-gateway-rtmp-ingest.md). |
| First real `/v1/live` smoke against a live orchestrator. | high | xodeapp publishes `video:transcode.live` (both offerings) | Acceptance: open session, ffmpeg pushes RTMP, HLS playback works, idle timeout triggers session.end (broker emits `idle_timeout` close_reason), runway exhaustion triggers auto-topup (counter `livepeer_gateway_live_topup_attempts_total{outcome="succeeded"}` increments). |
| Bump daemon images to `v1.3.2` when published. | med | `tztcloud/livepeer-{payment,service-registry}-daemon:v1.3.2` available on Docker Hub | Update `.env` (`LIVEPEER_PAYER_DAEMON_TAG`, `LIVEPEER_REGISTRY_DAEMON_TAG`) + `.env.example` + `docker-compose.yml` defaults; `docker compose up -d --force-recreate payer-daemon service-registry-daemon`; verify versions in boot log. |
| Live-stream webhook integration. | med | post-tunnel | The capability-broker's `rtmp-ingress-hls-egress` mode doesn't have an obvious per-session webhook surface yet. Today `live_streams.status` only updates on `/v1/live` POST/DELETE — broker-side teardowns (balance exhausted, runner crash) leave rows stuck `live`. Mirror the ABR webhook design once the broker exposes the hook. |
| Real-broker validation of `/v1/abr` end-to-end. | high | Phase 4 done | Need at least one orchestrator advertising `video:transcode.abr` with a funded payer keystore *and* working ABR preset config. As of 2026-05-20 us-central-worker.xodeapp.xyz returns `unknown preset: abr-standard` — operator-side regression unrelated to our gateway. |
| Real-broker validation of `/v1/live` end-to-end. | high | Phase 4 done | Same, for `video:transcode.live` (both offerings: `default` for broker_ingest, `gateway-ingest` for the new path). |
| Janitor for stuck `live_streams.status='provisioning'` rows. | med | post-scaffold | Close + refund after `LIVE_PROVISIONING_TTL` (suggest 5m default). |
| Signed download URLs at job dispatch instead of anonymous-read bucket. | med | production | Replace `mc anonymous set download` with per-job presigned GETs the runner consumes. |
| Quote-aware ABR ladder pricing. | med | post-scaffold | Today `face_value` is duration × default rate; runners advertise `units_per_price` we should honor. |
| Webhook / SSE channel for live status. | low | user demand | Out of v1; clients poll. |
| Idempotency keys on `/v1/abr` + `/v1/live`. | low | user demand | Today duplicate POSTs create duplicate jobs / sessions. |
| Distributed rate limiting (multi-replica). | low | scale | In-process token buckets only. |
| Pepper rotation without invalidating existing keys. | low | rotation cycle | Add dual-lookup. |
| Multi-operator admin role separation. | low | team growth | Currently one `ADMIN_TOKEN` for everyone. |
| Gateway-side playback proxy (`/playback/:id/*`). | low | user demand | v2. Lets us add signed URLs + per-key auth. |
| Sweeper for `usage_reservations.state='open'` orphans. | low | observability | If a request crashed before commit/refund, the row sits open. |
| Capability hot-reload without restart. | low | operator UX | Today the refresh ticker handles it; admin "force refresh" button would help. |
| Replace polling `GET /v1/abr/:id` with HEAD + cached status. | low | scale | Reduce DB churn under heavy polling. |
| Mechanical import-graph linter for the layered architecture. | low | code growth | Currently enforced by reviewer attention. |
