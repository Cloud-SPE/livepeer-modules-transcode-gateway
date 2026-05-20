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

A single-host or single-pod deployment of the load-bearing services,
with the three SPAs deployed independently:

```
                ┌────────────────────────────────────────┐
  internet ──►  │  reverse proxy (Traefik / nginx / LB)  │
                └────┬───────────┬───────────┬───────────┘
                     │ api.*     │ site.*    │ ingest.*
                     │           │ portal.*  │ metrics.*
                     │           │ admin.*   │ (basic auth)
                     ▼           ▼           ▼
                ┌────────┐   ┌────────┐   ┌────────┐
                │gateway │   │ CDN /  │   │rustfs  │
                │  :4000 │   │ static │   │ :9000  │
                └───┬────┘   └────────┘   └────────┘
                    │
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

The gateway, postgres, rustfs, and the two daemons share a
`livepeer-run` volume for UDS sockets.

---

## Pre-flight checklist

- [ ] Domain DNS records pointing at the host (api.\*, site.\*,
      portal.\*, admin.\*, ingest.\*, metrics.\*).
- [ ] TLS — Let's Encrypt or your CA of choice.
- [ ] Postgres data volume backed by durable storage.
- [ ] RustFS data volume backed by durable storage **with enough
      headroom for VOD uploads** (size to your traffic).
- [ ] An EVM JSON-RPC endpoint (Arbitrum One default).
- [ ] A funded Ethereum keystore for the payer-daemon.
- [ ] An `AI_SERVICE_REGISTRY_ADDRESS` for the chain you're on.
- [ ] Resend account + API key (or commit to running without email).
- [ ] A copy of `.env.example` with every value filled, including
      `S3_*` for RustFS.
- [ ] Backups configured (Postgres dumps + RustFS bucket sync).

---

## Secrets provisioning

| Var | Why | Suggested generation |
|---|---|---|
| `POSTGRES_PASSWORD` | Postgres role auth. | `openssl rand -base64 32` |
| `ADMIN_TOKEN` | Admin credential. | `openssl rand -hex 32` |
| `API_KEY_HASH_PEPPER` | Pepper for API key SHA-256. | `openssl rand -hex 32` |
| `IP_HASH_PEPPER` | Pepper for IPs / verification / session / stream-key hashes. | `openssl rand -hex 32` |
| `METRICS_TOKEN` | Bearer token on `/metrics`. | `openssl rand -hex 32` |
| `RUSTFS_ROOT_PASSWORD` | RustFS root credential. | `openssl rand -base64 32` |
| `S3_SECRET_ACCESS_KEY` | RustFS-issued access key for the gateway. | `openssl rand -hex 32` |
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

# 3. db + rustfs + bootstrap + gateway
make dev

# 4. plus livepeer daemons
make dev-livepeer
```

After startup the gateway is real. Don't ship to users until you've
done end-to-end validation:

1. **Sign up a test user** through the real flow (waitlist → verify →
   admin approve → API key emailed).
2. **Confirm `/v1/capabilities`** returns a non-empty catalog. Empty →
   registry-daemon hasn't synced; check its logs.
3. **POST `/v1/abr`** with a sample MP4 URL or via the
   `/v1/abr/upload-url` flow against RustFS. Expect a `job_id` +
   `master_playlist_url`. Wait for the runner to finish, then play the
   master playlist in a player.
4. **POST `/v1/live`** → push RTMP to the returned ingest URL using
   OBS or `ffmpeg -re -i input.mp4 -c copy -f flv rtmp://…/<key>`.
   Confirm the returned HLS URL plays back. `DELETE /v1/live/:id`.
5. **Confirm `usage_reservations`** committed for both flows; `live_streams`
   shows `status='ended'` after deletion.

---

## TLS termination

The gateway speaks plain HTTP. Put a reverse proxy in front. Same
shape as `livepeer-modules-openai/DEPLOYMENT.md`; the only delta:
front RustFS at `ingest.*` so clients can PUT directly.

Disable buffering on `/v1/live/*` if you ever add streaming responses
(today: poll-only, fine with default buffering).

---

## SPA hosting

The three SPAs in `web/` are static. Same options as the openai
gateway: same-host reverse proxy, CDN / Cloudflare Pages / Netlify /
Vercel, or object storage + CDN.

Branding: edit `web/site/index.html` + `web/site/index.css`. No
gateway code touches.

---

## RustFS in production

- **Storage class.** RustFS data lives in `video-gateway-rustfs-data`.
  Mount this on durable storage (EBS, GCE PD, ZFS).
- **Object lifecycle.** Set a TTL on `abr/` prefixes — the runner
  reads VOD inputs once; long retention is unnecessary. RustFS
  supports lifecycle policies through the standard S3 API.
- **Backups.** RustFS supports replication via `mc mirror`. Mirror
  the bucket nightly to off-site storage if VOD originals must be
  retained.
- **Public read.** Dev compose sets the bucket anonymous-read so the
  runner can pull. In production, restrict to runner IPs or use
  signed download URLs (a future plan; see tech-debt-tracker).

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

- 5xx rate above 1% sustained 5 min on `/v1/*`
- `proxy_reservations_total{outcome="refunded"}` rising sharply vs `committed`
- `livepeer_gateway_route_health_cooldowns_opened_total` rising
- `live_streams_active` flatlining when ingest should be flowing

---

## Common failure modes

| Symptom | Likely cause | Where to look |
|---|---|---|
| `/health` shows `db: error` | Postgres down or wrong DATABASE_URL | `docker compose logs db` |
| `/health` shows `rustfs: error` | rustfs container down, or bootstrap failed | `docker compose logs rustfs rustfs-bootstrap` |
| `/health` shows `payer: error` | Payer-daemon not running or socket path mismatch | `docker compose logs payer-daemon` |
| `/health` shows `registry: error` | Registry-daemon not running, or chain RPC unreachable | `docker compose logs service-registry-daemon` |
| `/v1/capabilities` returns `data: []` | No transcode capabilities advertised on-chain, or registry-daemon hasn't synced | Registry-daemon logs; wait one refresh cycle |
| `/v1/abr/upload-url` returns 503 | RustFS unreachable or credentials wrong | `docker compose logs rustfs gateway` |
| `/v1/live` returns 502 | No broker advertising `livepeer:transcode/live-rtmp-hls-abr` | Registry-daemon logs |
| RTMP push immediately drops | Broker tore down session (balance exhausted, bad stream key) | Broker logs (operator side) + gateway logs filtered by `live_id` |
| Verification emails not arriving | RESEND_API_KEY missing/invalid | gateway logs — `verification email send failed` |
| `/v1/abr` jobs sit at `processing` forever, never produce `master.m3u8` | Almost always orchestrator-side: runner's CUDA toolkit > host's NVIDIA driver, or unpatched NVENC session cap exhausted. Gateway is blind because the broker doesn't pass-through runner status. | [`docs/troubleshooting/runner-cuda-driver-mismatch.md`](./docs/troubleshooting/runner-cuda-driver-mismatch.md) |

---

## What this runbook does NOT cover (v1)

- **Multi-replica deploys** with shared session state / rate limits.
- **Blue-green / canary** rollouts.
- **Automatic backups.**
- **DDoS protection** (front-edge concern).
- **Multi-region.**
- **Gateway-side playback proxy** (out of v1 scope).
