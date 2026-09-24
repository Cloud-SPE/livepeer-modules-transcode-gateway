# Livepeer Video Gateway

A Go gateway for VOD ABR transcoding and live RTMP→HLS streaming on the
Livepeer network. One binary serves the API, marketing site, user portal,
and admin UI. Agents start at [AGENTS.md](AGENTS.md); all work is tracked
in [Beads](PLANS.md).

The gateway uses the Modules v2 contracts: LOC authorizes and routes work;
a capability broker executes `paid-job/v1` or `paid-session/v1`; external
runners perform transcoding. The public API remains under `/api/v1/*`.
See [the compatibility design](docs/design-docs/modules-v2.md) for source
revisions and migration constraints.

## Runtime dependencies

| Component | Responsibility |
|---|---|
| Postgres | Accounts, API keys, durable operations, usage and recovery |
| MinIO / S3 | Browser VOD uploads and ABR artifacts |
| Livepeer Open Clearinghouse | Capability catalog, route selection, spend authorizations and settlement |
| Modules v2 capability broker | Caller proof verification, runner dispatch, signed settlement evidence |
| `livepeer-modules-transcode-runners` | v2 ABR workloads and RTMP/HLS live session runtime |
| Resend (optional) | Verification and account emails |

There is no local payment daemon or funded operator wallet. A persistent
caller signing key proves authority delegated by LOC, and an encryption key
protects stored recovery credentials. The gateway owns public RTMP ingress
and relays media to the runner; live HLS URLs come from the runner runtime
descriptor. The gateway does not run FFmpeg.

## API and web surfaces

| Route | Purpose |
|---|---|
| `POST /api/v1/abr/upload-url` | Presigned VOD upload |
| `POST /api/v1/abr` | Submit an ABR workload |
| `GET /api/v1/abr/:id` | Read durable job status and completed artifacts |
| `POST /api/v1/live` | Allocate gateway RTMP ingress and runner HLS session |
| `GET /api/v1/live/:id` | Read session status and playback |
| `DELETE /api/v1/live/:id` | End live ingest and reconcile final usage |
| `GET /api/v1/capabilities` | LOC catalog, including protocol, unit and price denominator |
| `/openapi.json`, `/docs` | Generated API schema and viewer |
| `/`, `/portal/`, `/admin/` | Embedded Lit applications |
| `/api/public/*`, `/api/portal/*`, `/api/admin/*` | SaaS shell |
| `/health`, `/metrics` | Operations |

ABR uses `video-transcode-abr/v2` over broker SSE, processed by durable
background work so customers can poll the gateway. Live uses
`rtmp-hls-session/v1` requests and the `rtmp-hls/v1` runtime descriptor.
Measured, broker-signed evidence settles LOC usage; an unknown network
outcome is retained for recovery instead of being refunded as zero work.

## Local development

```bash
cp .env.example .env
# Configure URLs, auth secrets and S3 credentials.
# For network work, add a funded LOC_API_KEY, LOC_CALLER_PRIVATE_KEY,
# and OPERATION_SECRETS_KEY. See DEPLOYMENT.md for generation and backup.
make dev
curl http://localhost:4000/health
```

Set local `BASE_URL=http://localhost:4000`, `PUBLIC_SITE_URL` to that origin,
`PUBLIC_PORTAL_URL=http://localhost:4000/portal/`, and appropriate
`ALLOWED_ORIGINS`. MinIO's public endpoint must be reachable by the browser
and remote runners; `localhost:9000` works only for local consumers.
`LIVE_EXTERNAL_RTMP_URL` must be the externally reachable RTMP server root.

Production serves all SPAs on port 4000; optional `make web` runs development
servers at 3000/3001/3002. Default RTMP port is 1935. MinIO uses 9000/9001.
See [.env.example](.env.example) for all deployment settings.

```bash
make build
make go-test
docker compose build gateway
```

`make embed-webroot` copies the zero-build Lit SPAs into the embedded webroot
before Go compilation. The Dockerfile performs the same step. The published
image name is `tztcloud/livepeer-video-gateway:<TAG>`; build and publish a new
release for v2. Older `v1.4.1` images contain the legacy wire implementation.

## Repository map

| Path | Purpose |
|---|---|
| `gateway/` | Go API, auth, LOC/broker clients, durable operations, RTMP relay |
| `web/site/`, `web/portal/`, `web/admin/` | Zero-build Lit applications |
| `gateway/migrations/` | Boot-applied Postgres migrations |
| `docs/design-docs/` | Architectural rationale |
| `.agents/skills/beads/`, `.beads/`, `.codex/` | Work tracking and session hooks |

Deployment and upgrade instructions are in [DEPLOYMENT.md](DEPLOYMENT.md).
