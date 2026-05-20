# QUALITY_SCORE

Rough grades for each domain. Updated as the codebase evolves.

Grading scale: **A** (load-bearing, well-tested, well-documented) →
**B** (works, gaps known) → **C** (skeleton, expect rough edges) →
**F** (not yet written / broken).

| Domain | Path | Grade | Notes |
|---|---|---|---|
| Harness scaffolding | `AGENTS.md`, `docs/` | C | Phase 0 — present but not exercised. |
| Gateway — proxy core | `gateway/internal/proxy/` | C | Phase 1 + 2. ABR + live handlers wired; not yet exercised against a real broker. |
| Gateway — wire spec | `gateway/internal/proxy/livepeer/` | C | Phase 1. Ported from openai gateway; payment.go requires payer-daemon for real minting. |
| Gateway — route selector | `gateway/internal/proxy/service/` | C | Phase 1. Ported from openai gateway, retargeted at transcode capabilities. |
| Gateway — registry refresh | `gateway/internal/registry/` | C | Phase 1. Refreshes `capabilities` table from the resolver. |
| Gateway — SaaS shell | `gateway/internal/handlers/{waitlist,verify,portal,admin}/` | C | Phase 1. Auth, sessions, admin token, waitlist queue ported from openai gateway in Go. |
| Gateway — schema | `gateway/internal/repo/`, `gateway/migrations/` | C | Phase 1. Initial migration ports openai schema + `live_streams` + renames `models` → `capabilities`. sqlc-generated accessors. |
| Gateway — auth | `gateway/internal/proxy/auth.go`, `internal/handlers/portal/auth.go`, `handlers/admin/auth.go` | C | Phase 1. Bearer + cookie + admin token. |
| Gateway — metrics | `gateway/internal/metrics/` | C | Phase 1. Prometheus exposition. |
| Gateway — S3 / RustFS | `gateway/internal/s3/` | C | Phase 2. Presigned PUT for VOD ingest. |
| Web — site | `web/site/` | C | Phase 3. Zero-build Lit, rebranded to "Livepeer Video Gateway". |
| Web — portal | `web/portal/` | C | Phase 3. Playground rewritten with Live + Transcode tabs, hls.js via esm.sh. |
| Web — admin | `web/admin/` | C | Phase 3. Capability registry view + waitlist + users + usage. |
| Protos | `proto/` | C | Phase 0. Payments + registry trees vendored. Codegen target: `gateway/gen/proto/`. |
| Compose stack | `docker-compose.yml` | C | Phase 0 + 4. db + rustfs + bootstrap + gateway in default stack; daemons behind `livepeer` profile. |
| CI | `.github/workflows/` | C | Phase 0 — Go + Web + docs link check. |
| Tests | `gateway/**/*_test.go` | F | Phase 1 onwards. |
| OpenAPI | `/openapi.json` + `/docs`, via huma struct tags | C | Phase 1. Auto-generated from request/response structs. |

## Promotion criteria

A domain promotes from **F → C** when:
- The files exist and the package builds.

From **C → B** when:
- Happy-path manually exercised end-to-end.
- A short doc lives under `docs/design-docs/` or `docs/product-specs/`.

From **B → A** when:
- Tests cover the happy path + at least three error paths.
- Cross-references are mechanically validated by CI.
- An exec plan in `docs/exec-plans/completed/` records the design history.

Don't downgrade silently. If a domain regresses, open a tech-debt
entry in
[`docs/exec-plans/tech-debt-tracker.md`](./docs/exec-plans/tech-debt-tracker.md).
