# Deployment

The gateway serves HTTP and embedded SPAs on port 4000 and public RTMP on
port 1935. Traefik or another reverse proxy terminates HTTPS. Postgres and
MinIO remain private except for the public S3 endpoint required by browsers
and runners. LOC and Modules v2 brokers/runners are external services.

## Upgrading from the legacy gateway

Drain ABR work, live sessions, and unsettled LOC reservations using the old
binary before upgrading. Back up Postgres and both persistent keys before
starting the new image. The v2 gateway refuses startup with legacy active
paid work rather than guessing how to settle it under a different protocol.
Do not erase reservation state to bypass this guard.

Build a new gateway image from this checkout and deploy matching v2 runner
and broker images. Changing the tag to an old published `v1.4.1` image does
not deploy this migration. Database migrations apply on startup, including
durable paid operations and the v2 capability catalog. A schema downgrade
cannot convert v2 active operations back to the legacy wire contract.

The catalog is invalidated during migration and repopulated from LOC. Check
that the advertised offerings include `paid-job/v1` for ABR and
`paid-session/v1` for live, along with their work units, price denominators,
estimator and job/session axes. Capability names alone do not prove protocol
compatibility.

## Secrets and configuration

Use a secret manager or the gitignored `.env` file. Preserve these values
across container replacement and database restore:

| Setting | Value / generation |
|---|---|
| `LOC_BASE_URL`, `LOC_API_KEY` | LOC endpoint and funded operator-issued API key |
| `LOC_CALLER_PRIVATE_KEY` | 32-byte secp256k1 scalar as 64 hex characters; `openssl rand -hex 32` |
| `OPERATION_SECRETS_KEY` | 32-byte AES-GCM key as base64; `openssl rand -base64 32` |
| `ADMIN_TOKEN`, `API_KEY_HASH_PEPPER`, `IP_HASH_PEPPER`, `METRICS_TOKEN` | Independently generated secrets; `openssl rand -hex 32` |
| `POSTGRES_PASSWORD`, `MINIO_ROOT_PASSWORD`, `S3_SECRET_ACCESS_KEY` | Independent strong storage credentials |
| `RESEND_API_KEY`, `FROM_EMAIL` | Email delivery configuration |

The signing and encryption keys are required whenever `LOC_API_KEY` is set.
Losing the encryption key makes persisted recovery credentials unreadable;
changing the signing key during active work changes caller identity. Drain
work before rotating either key and retain the old keys with backups.
Without LOC configured, the SaaS shell remains usable and paid endpoints
return `503 loc_unavailable`.

For a single-domain deployment like the supplied Traefik stack:

```dotenv
BASE_URL=https://video-demo.cloudspe.com
PUBLIC_SITE_URL=https://video-demo.cloudspe.com
PUBLIC_PORTAL_URL=https://video-demo.cloudspe.com/portal/
ALLOWED_ORIGINS=https://video-demo.cloudspe.com
S3_ENDPOINT=http://minio:9000
S3_PUBLIC_ENDPOINT=https://video-s3-demo.cloudspe.com
MINIO_API_CORS_ALLOW_ORIGIN=https://video-demo.cloudspe.com
LIVE_RTMP_PORT=1935
LIVE_RTMP_HOST_PORT=1935
LIVE_EXTERNAL_RTMP_URL=rtmp://video-demo.cloudspe.com:1935/live
```

Pass the new key and policy variables through your production Compose
`gateway.environment` section; setting them only in `.env` is insufficient
when the service does not forward them. The repository Compose file shows
all required pass-throughs. Traefik must join the gateway's Docker network;
its HTTP route targets port 4000, and the S3 route targets MinIO port 9000.
Publish RTMP as TCP or an L4 proxy; an HTTPS router does not carry RTMP.

The Compose mapping uses container port 1935 even when the listener is
disabled with `LIVE_RTMP_PORT=0`. Remove the port mapping to avoid publishing
RTMP at all. If changing the internal listener port, change the mapping too.

## Workload policy and units

| Setting | Default | Meaning |
|---|---|---|
| `ABR_OFFERING` | `abr-default` | Advertised ABR offering |
| `ABR_MAX_TOTAL_UNITS` | `1000000` | Hard authorization ceiling in `video-frame-megapixel` |
| `ABR_JOB_TIMEOUT_SECS` | `3600` | ABR execution deadline |
| `LIVE_GATEWAY_INGEST_OFFERING` | `gateway-ingest` | Advertised live offering |
| `LIVE_MAX_TOTAL_UNITS` | `6000` | Lifetime cap in whole `output_seconds` |
| `LIVE_OUTPUT_PROFILE` | `live-standard` | Runner output profile |
| `LIVE_METERING_RENDITION` | `720p` | Rendition used for finalized output metering |
| `LIVE_RECONCILE_INTERVAL_SECS` | `30` | Positive live reconciliation cadence; zero is invalid |
| `LIVE_TOPUP_RUNWAY_THRESHOLD_SECS` | `60` | Remaining runway at which to request a refill |
| `LIVE_TOPUP_FUND_SECS` | `60` | Additional output seconds requested per refill |

Do not carry forward the old live default of `6000000`: it represented a
millisecond-style estimate and grants a much larger cap in whole seconds.
LOC quotes use `price_per_work_unit_wei / units_per_price`; preserve the
denominator when inspecting prices. Actual settlement comes from signed
broker evidence, not these limits or elapsed time.

The recovery scheduler scans durable operations every two seconds and cannot
be disabled. Live reconciliation requires a positive interval. The former
`SETTLE_JANITOR_INTERVAL_SECS` and `LIVE_IDLE_TIMEOUT_SECS` are removed;
idle-timeout policy belongs to the runner.

Live HLS playback is supplied by the `rtmp-hls/v1` runner descriptor. The
runner's public HLS endpoint and ingest endpoint must be reachable from the
appropriate clients and gateway. `LIVE_PLAYBACK_BASE_URL` and
`LIVE_S3_CREDENTIAL_TTL_HOURS` are legacy settings, not v2 live configuration.
The current runner defaults to ten-minute ingest keys and one-hour issuance
grants. The gateway renews an expired key on encoder reconnect, without
rotating keys during an active publication. A reconnect after the grant has
expired ends the old session with `recovery_failed` and
`stream_key_grant_expired`; create a new session. An already connected
publisher is not interrupted merely because its key expires.

MinIO remains the VOD input and ABR artifact store. Anonymous read in the
sample bootstrap is a deployment policy choice for those artifacts; set
retention and access policies appropriate to your deployment.

## Bring-up and acceptance

Build with `docker compose build gateway`, then start with `make dev` or your
production Compose equivalent. Check `/health`, startup migrations, LOC
catalog refresh, and storage reachability. Exercise the real signup/approval
flow to obtain a customer API key.

Submit a short VOD input, poll the returned job status, and verify that the
completed master playlist and variants play. Confirm the LOC ledger agrees
with signed measured units. Open live, push RTMP to the returned server URL
and key, verify HLS playback, then stop and confirm terminal status and final
settlement. Exercise refill and restart recovery with controlled workloads
before allowing customers onto the deployment. Local unit and contract tests
do not substitute for a funded end-to-end test on the deployed network.

Keep one gateway instance per deployment until distributed session ownership
and rate limiting are validated. Monitor operation recovery/settlement
backlogs and terminal errors as well as HTTP failures; a successful HTTP
submission alone does not prove a finished transcode.

## Backups and operation recovery

Back up Postgres, retained MinIO objects, and the two persistent keys. A
Postgres backup can be created with:

```bash
docker compose exec -T db pg_dump -U video_gateway \
  --format=custom video_gateway > video_gateway.pgdump
```

Test restores in an isolated environment. Stop the gateway during a real
restore, restore matching keys, then let its durable recovery loop reconcile
LOC and broker state. Never turn an ambiguous dispatch into a zero-unit
refund or delete its operation record as a repair shortcut.

Tasks, validation findings and deployment follow-ups belong in Beads; see
[PLANS.md](PLANS.md). No deployment, image publish or paid network test is
implied by editing the local repositories.

## Diagnosing paid operations

Paid-operation warnings contain `operation_id`, `kind`, `state`, `attempt`,
`stop_requested`, and `retryable`. Authorized operations also include
`loc_job_id` or `loc_session_id`, plus `broker_request_id` for correlation
with LOC and broker logs. These diagnostics were added under Bead `vgw-6bq`.

- `paid operation terminal refusal`, `retryable=false`: issuance was refused
  and the gateway finished its local failure handling. This is not a queued retry.
- `loc_http_422` with `loc_code=AUTHORIZATION_REFUSED` and
  `reason_code=authorization_limit_exceeded`: compare
  `requested_max_debit_wei` with `payer_max_authorization_wei`.
- `broker_http_402`: the broker rejected the HTTP request; `broker_endpoint`
  identifies which operation failed. Inspect receiver funding/admission logs.
- `paid operation broker recovery pending`, `broker_outcome=ADMISSION_REJECTED`:
  the broker reports admission rejection, but signed terminal accounting is
  still required. `recovery_action=await_loc_signed_non_admission` identifies
  the LOC recovery path to investigate. This log is not evidence of a refund.
- `upstream_timeout`: a request deadline or network timeout expired.

Recovery warnings retain `retryable=true` while the durable operation is
unfinished, even if the underlying HTTP response is a 4xx. This refers to
operation recovery, not permission to create a replacement job. Raw upstream
messages, request/response bodies, credential-bearing URLs, and unknown error
codes are deliberately excluded. Logs diagnose failures; they do not change
settlement or retry policy. Existing operations acquire the new diagnostics
when their next recovery attempt runs after deployment.
