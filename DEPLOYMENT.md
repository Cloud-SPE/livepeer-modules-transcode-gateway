# Deployment

Operator-facing runbook for deploying the Livepeer Video Gateway to a
real environment. Pairs with [`README.md`](./README.md) (dev
quickstart) and
[`docs/design-docs/boot-sequence.md`](./docs/design-docs/boot-sequence.md)
(what runs at startup).

If you only want to bring up dev: that's `make dev` per the README.
This doc is for production.

---

## Topology

A single-host or single-pod deployment. The gateway binary serves the
API, the three SPAs (embedded), `/health`, and `/metrics` from one port
(default `4000`), and the public RTMP listener on `:1935`:

```
                ┌────────────────────────────────────────┐
  internet ──►  │  reverse proxy (Traefik / nginx / LB)  │
                └────┬───────────────────┬───────────────┘
                     │ HTTP              │ ingest.*
                     │ (api + SPAs +     │ (HLS read)
                     │  /health + /metrics)
                     ▼                   ▼
                ┌────────┐           ┌────────┐
                │gateway │           │ minio  │
                │  :4000 │ ──S3+STS─►│  :9000 │
                │  :1935 │ ◄──RTMP── │ :9001  │
                └───┬────┘           └────────┘
                    │  ▲
                    │  └── OBS / ffmpeg push RTMP to :1935
        ┌───────────┼─────────────┐
        │           │             │
        ▼           ▼             ▼
   ┌─────────┐ ┌─────────┐  ┌──────────┐
   │ postgres│ │ payer-  │  │service-  │
   │  :5432  │ │ daemon  │  │registry- │
   └─────────┘ │  (UDS)  │  │ daemon   │
               └────┬────┘  └────┬─────┘
                    │            │
                    ▼            ▼
                  chain RPC  +  on-chain registry
```

The gateway, postgres, minio, and the two daemons share a
`livepeer-run` volume for UDS sockets.

---

## Pre-flight checklist

- [ ] Domain DNS records pointing at the host (e.g. `app.*` or split
      `api.*`/`portal.*`/`admin.*`, plus `ingest.*` for HLS playback and
      optionally `metrics.*`).
- [ ] TLS — Let's Encrypt or your CA of choice.
- [ ] Postgres data volume backed by durable storage.
- [ ] MinIO data volume backed by durable storage **with enough
      headroom for VOD uploads + live HLS output** (size to your traffic).
- [ ] Public RTMP TCP port 1935 reachable (or set `LIVE_RTMP_PORT=0` to
      disable live ingest).
- [ ] An EVM JSON-RPC endpoint (Arbitrum One default).
- [ ] A funded Ethereum keystore for the payer-daemon.
- [ ] An `AI_SERVICE_REGISTRY_ADDRESS` for the chain you're on.
- [ ] Resend account + API key (or commit to running without email).
- [ ] A copy of `.env.example` with every value filled, including
      `S3_*` and `MINIO_*`.
- [ ] Backups configured (Postgres dumps + MinIO bucket replication via `mc mirror`).

---

## Secrets provisioning

| Var | Why | Suggested generation |
|---|---|---|
| `POSTGRES_PASSWORD` | Postgres role auth. | `openssl rand -base64 32` |
| `ADMIN_TOKEN` | Admin credential. | `openssl rand -hex 32` |
| `API_KEY_HASH_PEPPER` | Pepper for API key SHA-256. | `openssl rand -hex 32` |
| `IP_HASH_PEPPER` | Pepper for IPs / verification / session / stream-key hashes. | `openssl rand -hex 32` |
| `METRICS_TOKEN` | Bearer token on `/metrics`. | `openssl rand -hex 32` |
| `MINIO_ROOT_PASSWORD` | MinIO root credential. | `openssl rand -base64 32` |
| `S3_SECRET_ACCESS_KEY` | MinIO-issued access key for the gateway. | `openssl rand -hex 32` |
| `RESEND_API_KEY` | Email delivery. | from Resend dashboard |

Keep secrets out of git. Use the compose `.env` (git-ignored) or a
secret manager.

---

## Bringing the gateway online

```bash
# 1. clone + env
git clone <repo>
cd livepeer-modules-transcode-gateway
cp .env.example .env
$EDITOR .env  # fill every required value

# 2. build
docker compose build gateway

# 3. db + minio + bootstrap + gateway
make dev

# 4. plus livepeer daemons
make dev-livepeer
```

After startup the gateway is real. Don't ship to users until you've
done end-to-end validation:

1. **Sign up a test user** through the real flow (waitlist → verify →
   admin approve → API key emailed).
2. **Confirm `/api/v1/capabilities`** returns a non-empty catalog. Empty →
   registry-daemon hasn't synced; check its logs.
3. **POST `/api/v1/abr`** with a sample MP4 URL or via the
   `/api/v1/abr/upload-url` flow against MinIO. Expect a `job_id` +
   `master_playlist_url`. Wait for the runner to finish, then play the
   master playlist in a player.
4. **POST `/api/v1/live`** → push RTMP to the returned ingest URL
   (`rtmp://<gateway>:1935/live/<key>`) using OBS or
   `ffmpeg -re -i input.mp4 -c copy -f flv rtmp://…/<key>`. Confirm the
   returned HLS URL plays back. `DELETE /api/v1/live/:id` (OBS should
   see the disconnect within ~2s because the gateway closes the RTMP
   socket synchronously).
5. **Confirm `usage_reservations`** committed for both flows; `live_streams`
   shows `status='ended'` after deletion.

---

## TLS termination

The gateway speaks plain HTTP. Put a reverse proxy in front. Same
shape as `livepeer-modules-openai/DEPLOYMENT.md`; the only delta:
front MinIO at `ingest.*` so HLS viewers can fetch directly, and pass
RTMP traffic on TCP `:1935` either directly or via an L4 proxy (RTMP
is not HTTP — most reverse proxies need a stream/TCP rule for it).

Disable buffering on `/api/v1/live/*` if you ever add streaming responses
(today: poll-only, fine with default buffering).

---

## SPA hosting

The three SPAs are **embedded into the gateway binary** via `//go:embed`
under `gateway/internal/server/webroot/`. `make embed-webroot` (also run
by the Dockerfile) copies `web/{site,portal,admin}` into that path before
`go build`, so there's no separate static-host or CDN step. Production
serves `/`, `/portal/`, and `/admin/` from the same port as the API.

If you want to front the SPAs with a CDN, point the CDN at the gateway
host; cache `*.js`/`*.css` aggressively and the HTML lightly.

Branding: edit `web/site/index.html` + `web/site/index.css` and rebuild.
No gateway code touches.

---

## MinIO in production

- **Storage class.** MinIO data lives in `video-gateway-minio-data`.
  Mount this on durable storage (EBS, GCE PD, ZFS).
- **Object lifecycle.** Set a TTL on `abr/` and `live-out/` prefixes
  — the runner reads VOD inputs once, and HLS segments are throwaway
  once the session ends. Configure lifecycle via `mc ilm import` or
  the standard S3 API.
- **Backups.** Replicate the bucket via `mc mirror` (or MinIO's
  built-in bucket replication) to off-site storage if VOD originals
  must be retained.
- **CORS.** The MinIO compose service is configured via the
  `MINIO_API_CORS_ALLOW_ORIGIN` env var; no separate CORS-bootstrap
  container.
- **STS.** Per-live-session credentials are minted via STS
  `AssumeRole` with an inline policy scoped to
  `live-out/<api_key>/<live_id>/*`. The runner only gets write access
  to its own session's prefix. Confirm STS is reachable from the
  gateway with `mc admin info` and that the bucket policy permits
  the gateway's service-account access key to call `AssumeRole`.
- **Public read.** Dev compose leaves the bucket anonymous-read so
  the runner and viewers can pull. In production, restrict to runner
  IPs or use signed download URLs (tracked in tech-debt-tracker).

---

## Postgres backup + restore

Logical backups daily, shipped off-host:

```bash
docker compose exec -T db pg_dump -U video_gateway \
  --format=custom \
  video_gateway > video_gateway-$(date +%F).pgdump
```

Restore:

```bash
docker compose stop gateway
docker compose exec db psql -U video_gateway -c \
  "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"
docker compose exec -T db pg_restore -U video_gateway -d video_gateway \
  < video_gateway-2026-05-19.pgdump
docker compose start gateway
```

Migrations live in `gateway/migrations/`. Applied at boot by
`golang-migrate`, recorded in `schema_migrations`, idempotent.

---

## Metrics

Prometheus scrapes `/metrics`. Surfaces:

- Go runtime metrics under prefix `video_gateway_*`
- `video_gateway_http_requests_total{method,route,status}`
- `video_gateway_http_request_duration_seconds`
- `video_gateway_proxy_reservations_total{capability,outcome}`
- `video_gateway_live_streams_active`
- `video_gateway_waitlist_signups_total`
- `livepeer_gateway_route_health_*`

Recommended starter alerts:

- 5xx rate above 1% sustained 5 min on `/api/v1/*`
- `proxy_reservations_total{outcome="refunded"}` rising sharply vs `committed`
- `livepeer_gateway_route_health_cooldowns_opened_total` rising
- `live_streams_active` flatlining when ingest should be flowing

---

## Common failure modes

| Symptom | Likely cause | Where to look |
|---|---|---|
| `/health` shows `db: error` | Postgres down or wrong DATABASE_URL | `docker compose logs db` |
| `/health` shows `minio: error` | minio container down, or bootstrap failed | `docker compose logs minio minio-bootstrap` |
| `/health` shows `rtmp: error` | RTMP listener didn't bind to `LIVE_RTMP_PORT` (port in use, perms) | `docker compose logs gateway` |
| `/health` shows `payer: error` | Payer-daemon not running or socket path mismatch | `docker compose logs payer-daemon` |
| `/health` shows `registry: error` | Registry-daemon not running, or chain RPC unreachable | `docker compose logs service-registry-daemon` |
| `/api/v1/capabilities` returns `data: []` | No transcode capabilities advertised on-chain, or registry-daemon hasn't synced | Registry-daemon logs; wait one refresh cycle |
| `/api/v1/abr/upload-url` returns 503 | MinIO unreachable or credentials wrong | `docker compose logs minio gateway` |
| `/api/v1/live` returns 502 | No broker advertising `video:transcode.live` with offering `gateway-ingest` | Registry-daemon logs |
| RTMP push immediately drops | Stream key didn't match `live_streams.stream_key_hash`, or upstream broker tore down session | Broker logs (operator side) + gateway logs filtered by `live_id` |
| Verification emails not arriving | RESEND_API_KEY missing/invalid | gateway logs — `verification email send failed` |
| `/api/v1/abr` jobs sit at `processing` forever, never produce `master.m3u8` | Almost always orchestrator-side: runner's CUDA toolkit > host's NVIDIA driver, or unpatched NVENC session cap exhausted. Gateway is blind because the broker doesn't pass-through runner status. | [`docs/troubleshooting/runner-cuda-driver-mismatch.md`](./docs/troubleshooting/runner-cuda-driver-mismatch.md) |

---

## What this runbook does NOT cover (v1)

- **Multi-replica deploys** with shared session state / rate limits.
- **Blue-green / canary** rollouts.
- **Automatic backups.**
- **DDoS protection** (front-edge concern).
- **Multi-region.**
- **Gateway-side playback proxy** (out of v1 scope).
