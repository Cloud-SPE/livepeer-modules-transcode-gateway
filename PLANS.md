# PLANS

Plans are first-class artifacts. Non-trivial work lands as an execution
plan under `docs/exec-plans/active/`; completed plans move to
`docs/exec-plans/completed/`. Lightweight changes go straight to PR.

## Active

| Plan | Status |
|---|---|

(none.)

## Completed

| Plan | Date | Summary |
|---|---|---|
| [`0001-scaffold`](./docs/exec-plans/completed/0001-scaffold.md) | 2026-05-19 | Initial scaffold: Go gateway shell, three zero-build Lit SPAs, RustFS S3 ingest, docker-compose with payment + registry daemons, harness docs. Defines `/v1/abr`, `/v1/live`, `/v1/capabilities` surface and the `live_streams` table on top of the SaaS shell ported from `livepeer-modules-openai`. |
| Route rename + MinIO/STS + SPA embed | 2026-05-21 | Three converging batches landed together: (1) all HTTP routes moved under `/api/*` (`/v1/*`→`/api/v1/*`, `/admin/*`→`/api/admin/*`, `/portal/*`→`/api/portal/*`, `/api/waitlist`→`/api/public/waitlist`, `/api/verify`→`/api/public/verify`, `/api/abr/callback`→`/api/webhooks/abr`); `/health` and `/metrics` stay at root. (2) Storage backend swapped from RustFS to MinIO; live sessions now use MinIO STS `AssumeRole` to mint per-session credentials scoped to `live-out/<api>/<sess>/*` (env vars renamed `RUSTFS_*`→`MINIO_*`, compose services `rustfs`/`rustfs-bootstrap`/`rustfs-cors` replaced by `minio` + `minio-bootstrap` with CORS via `MINIO_API_CORS_ALLOW_ORIGIN`). (3) The three SPAs are now embedded into the gateway binary via `//go:embed`; production serves everything on one port; `make web` dev mode still works for hot reload. Plus: `live-session-remote-runner@v0` removed (only `live-session-gateway-ingest@v0` survives), `DELETE /api/v1/live` synchronously tears down the RTMP relay (~2s OBS disconnect), admin live-streams view shows runner status (migration 0008 `runner_status_json`), portal playground surfaces raw runner errors + adds Copy URL / variants UI, and `tztcloud/livepeer-video-gateway:v1.3.0` published to Docker Hub. |

## How to write a plan

Each plan is a markdown file at `docs/exec-plans/active/NNNN-slug.md` with:

1. **One-liner.** What is this plan about? Answerable in one sentence.
2. **Context.** Why does this exist? What's the trigger?
3. **Scope.** What's in. What's out.
4. **Approach.** Phases, files touched, decisions to lock.
5. **Acceptance.** How do we know this is done?
6. **Decision log.** Each non-obvious choice + why, dated.

When the plan completes, append a `## Outcome` section, then `git mv` it
into `docs/exec-plans/completed/`.

## Tech debt

Ongoing debt tracked in
[`docs/exec-plans/tech-debt-tracker.md`](./docs/exec-plans/tech-debt-tracker.md).
