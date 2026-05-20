# 0001 — scaffold

## One-liner

Stand up `livepeer-modules-transcode-gateway` as a Go + Lit clone of
`livepeer-modules-openai`, retargeted at VOD ABR + RTMP→HLS live
transcode capabilities, with RustFS for VOD ingest.

## Context

We have a working OpenAI gateway over the Livepeer network
(`livepeer-modules-openai`). Two things change in this repo:

1. **The product surface.** Replace OpenAI `/v1/chat/completions` etc.
   with transcode-shaped `/v1/abr`, `/v1/live`, `/v1/capabilities`.
2. **The implementation language.** Move the gateway from
   TypeScript/Fastify to Go/huma/chi/pgx/sqlc. The supply-side
   ecosystem (capability-broker, payment-daemon,
   service-registry-daemon, video-runners) is already Go, so Go
   matches the network's idiom.

The SaaS shell (waitlist → verify → admin approve → API key, cookie
sessions, admin token, per-key rate limit, metrics) ports unchanged
in spirit. The three Lit SPAs port verbatim with rebranding +
Playground rewritten to demo transcode.

## Scope

### In

- Repo skeleton: package.json (root, web/* workspace only), Makefile,
  docker-compose.yml (db + rustfs + bootstrap + gateway, livepeer
  daemons behind profile), .env.example, .gitignore, LICENSE, CI.
- Harness docs (AGENTS / ARCHITECTURE / DESIGN / FRONTEND / PLANS /
  PRODUCT_SENSE / QUALITY_SCORE / RELIABILITY / SECURITY / DEPLOYMENT
  / CONTRIBUTING / README), docs/{design-docs, product-specs,
  exec-plans, references, generated} tree.
- Vendored proto/ tree (payments v1 + registry v1) + provenance README.
- gateway/ Go module: cmd/gateway/main.go, internal/{config, db,
  repo, handlers/{waitlist, verify, portal, admin, v1}, proxy/{abr,
  live, capabilities, livepeer, service}, registry, crypto, email,
  s3, metrics, schema}, gen/{proto, db}, migrations/0001_initial.sql,
  sqlc.yaml, Dockerfile.
- huma + chi HTTP surface, OpenAPI auto-generated.
- pgx + sqlc data layer; golang-migrate at boot.
- gRPC clients to payment-daemon + service-registry-daemon over UDS,
  using protoc-gen-go-generated stubs.
- RustFS S3 presign client for `/v1/abr/upload-url`.
- Three Lit SPAs cloned from openai gateway, rebranded, with
  Playground rewritten (Live + Transcode tabs, hls.js via importmap).
- RustFS in docker-compose with a one-shot `rustfs-bootstrap`
  container that creates the bucket + access key automatically.
- Smoke test script.

### Out (deferred)

- VOD single-rendition transcode (`/v1/transcode`).
- Gateway-side playback proxy.
- Server-sent events or webhooks for live status.
- Idempotency keys.
- Multi-replica / distributed rate limiting.
- Multi-operator admin role separation.
- Pepper rotation without invalidating existing keys.
- Hardware wallet keystore.
- Production-grade observability stack (Loki, Vector, Grafana
  configs).

## Approach

Five phases (this plan is the deliverable for **Phase 0**; the rest
ship as part of this same plan because the scaffold has no value
half-built):

| Phase | Output |
|---|---|
| 0 — Skeleton + docs | All root files, harness docs, docs/ tree, vendored proto/, this plan. |
| 1 — Go gateway shell | go.mod, cmd/gateway, internal/{config, db, repo, handlers, proxy/livepeer, proxy/service, registry, crypto, email, metrics, schema}, migrations/0001_initial.sql, sqlc.yaml. |
| 2 — Transcode handlers | `/v1/abr`, `/v1/abr/upload-url`, `/v1/live` (POST/GET/DELETE), `/v1/capabilities`. RustFS S3 client. |
| 3 — Lit SPAs | web/site, web/portal, web/admin clones with rebrand + Playground rewrite. |
| 4 — Compose + RustFS bootstrap | docker-compose.yml with rustfs + rustfs-bootstrap, smoke.sh. |
| 5 — Design + product docs | docs/design-docs/* and docs/product-specs/* written to match the implemented surface. |

### Decisions locked

| Date | Decision | Why |
|---|---|---|
| 2026-05-19 | Go (not TypeScript) for the gateway. | Supply-side ecosystem is all Go; protoc + sqlc codegen is more legible than runtime-loaded TS. |
| 2026-05-19 | huma + chi (not gin / echo / fiber). | huma auto-generates `/openapi.json` + `/docs` from struct tags, fits agent-first harness. |
| 2026-05-19 | sqlc (not ent / GORM). | "Write SQL, generate accessors" is the most agent-legible; SQL stays the source of truth. |
| 2026-05-19 | Custom `/v1/abr`, `/v1/live` (not Studio-compatible). | We're not trying to be Studio; greenfield REST is cleaner. |
| 2026-05-19 | RustFS (not MinIO). | User-specified. S3-compatible; works with `mc` CLI for bootstrap. |
| 2026-05-19 | Pre-signed URL + caller-URL both supported for VOD input. | User-specified. Lets users push small files via RustFS + reuse existing public assets without a copy. |
| 2026-05-19 | Hand back broker HLS URLs (no playback proxy). | v1 simplification. Gateway-side playback is a v2 plan. |
| 2026-05-19 | Live = poll-only status. | No SSE / WebSocket / webhook surface in v1. |
| 2026-05-19 | Live = payment-bound lifecycle. | The broker's rtmp-ingress-hls-egress mode requires it; no alternative. |
| 2026-05-19 | `live_streams` as a separate table. | RTMP sessions need their own client-facing ID, ingest URL, stream-key hash; collapsing into `usage_reservations` would muddy the per-attempt billing semantics. |
| 2026-05-19 | Capability identifiers — `livepeer:transcode/abr-ladder` + `livepeer:transcode/live-rtmp-hls-abr`. | Mirrors livepeer-network-modules naming. Overridable via env. |

## Acceptance

- `go build ./...` succeeds in `gateway/`.
- `make dev` brings up `db + rustfs + rustfs-bootstrap + gateway`
  with no manual steps; `curl localhost:4000/health` returns 200.
- `curl localhost:4000/openapi.json` returns a valid huma-generated
  spec listing all `/api/*`, `/portal/*`, `/admin/*`, `/v1/*` routes.
- `make web` starts site/portal/admin dev servers on 3000/3001/3002,
  all visually clean.
- The signup → verify → approve → key flow round-trips against the
  dev DB without errors.
- `POST /v1/abr/upload-url` returns a valid presigned URL against
  RustFS; PUTting bytes to it returns 200.
- `POST /v1/abr` and `POST /v1/live` return 502 cleanly (no
  candidates) when daemons aren't up — proving the failover loop
  reaches its terminal state.

## Outcome

Scaffold landed 2026-05-19. The repo is now ready for real-broker
validation: bring up the `livepeer` profile, fund the payer
keystore, and exercise `/v1/abr` and `/v1/live` end-to-end. Open
follow-ups are tracked in
[`../tech-debt-tracker.md`](../tech-debt-tracker.md).
