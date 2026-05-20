# AGENTS.md

This is `livepeer-modules-transcode-gateway` — the **Livepeer Video
Gateway**: a transcode-focused gateway exposing ABR ladder transcoding
and RTMP→HLS live streaming, backed by the Livepeer network.

## Operating principles

This repo follows the agent-first harness pattern in
[`docs/references/openai-harness-engineer.md`](./docs/references/openai-harness-engineer.md).
Short version:

- **You steer; the agent executes.** Humans set intent; tools and feedback
  loops do the rest.
- **The repo is the system of record.** If it isn't checked in, it doesn't
  exist.
- **Progressive disclosure.** This file is a *map*, not a manual.
- **Enforce invariants, not implementations.** Constraints in lints/CI;
  choices in code.
- **Throughput over ceremony.** Short-lived PRs; fix-forward over block.

Read [`docs/design-docs/core-beliefs.md`](./docs/design-docs/core-beliefs.md)
before making load-bearing decisions.

## Where to look

| Question | File |
|---|---|
| What is this repo? | [`README.md`](./README.md) |
| Architectural overview at a glance | [`DESIGN.md`](./DESIGN.md) |
| Top-level architecture map | [`ARCHITECTURE.md`](./ARCHITECTURE.md) |
| What invariants must any change uphold? | [`docs/design-docs/core-beliefs.md`](./docs/design-docs/core-beliefs.md) |
| What design docs exist? | [`docs/design-docs/index.md`](./docs/design-docs/index.md) |
| What product surface ships? | [`docs/product-specs/index.md`](./docs/product-specs/index.md) |
| What plans are active / done? | [`PLANS.md`](./PLANS.md) |
| What tech debt are we tracking? | [`docs/exec-plans/tech-debt-tracker.md`](./docs/exec-plans/tech-debt-tracker.md) |
| What product principles guide tradeoffs? | [`PRODUCT_SENSE.md`](./PRODUCT_SENSE.md) |
| What's the quality bar per layer? | [`QUALITY_SCORE.md`](./QUALITY_SCORE.md) |
| What reliability properties hold? | [`RELIABILITY.md`](./RELIABILITY.md) |
| What's the threat model + auth surface? | [`SECURITY.md`](./SECURITY.md) |
| What frontend DOM/CSS rules apply? | [`FRONTEND.md`](./FRONTEND.md) |
| How do I deploy this to production? | [`DEPLOYMENT.md`](./DEPLOYMENT.md) |
| Operator-side runbooks for failures the gateway can't fix itself | [`docs/troubleshooting/index.md`](./docs/troubleshooting/index.md) |
| Where is the API spec? | `GET /openapi.json` (live, served by huma) or [`docs/product-specs/transcode-surface.md`](./docs/product-specs/transcode-surface.md) (prose). |
| Reference material (papers, transcripts) | [`docs/references/`](./docs/references/) |

## Repo shape

| Path | What it is |
|---|---|
| [`gateway/`](./gateway/) | Go backend — single binary: transcode `/v1/*` API (`/v1/abr`, `/v1/live`, …), waitlist + auth + admin SaaS shell, gRPC clients to `service-registry-daemon` + `payment-daemon`, S3 presign client to RustFS. |
| [`web/site/`](./web/site/) | Zero-build Lit marketing site + waitlist signup. |
| [`web/portal/`](./web/portal/) | Zero-build Lit user dashboard (account, API keys, playground with Live + Transcode tabs). |
| [`web/admin/`](./web/admin/) | Zero-build Lit admin (waitlist queue, users, usage, transcode-capability registry). |
| [`proto/`](./proto/) | gRPC protos shared between the gateway and the registry / payer daemons. Codegen target: `gateway/gen/proto/`. |

## Doing work in this repo

- **Go**, not TypeScript. The gateway is a single Go binary at
  `gateway/cmd/gateway`. Reasons in
  [`docs/design-docs/core-beliefs.md`](./docs/design-docs/core-beliefs.md).
- **huma + chi** for HTTP. Struct-tag validation → JSON Schema →
  `/openapi.json` + `/docs` viewer for free.
- **pgx + sqlc** for Postgres. Migrations under `gateway/migrations/`
  applied at boot by golang-migrate.
- **Zero-build SPAs.** `web/` apps use Lit + `esm.sh` importmaps + a
  per-app `dev-server.js`. No Vite, no bundler. See
  [`FRONTEND.md`](./FRONTEND.md) for DOM/CSS invariants.
- **Gateway pays the network.** Every `/v1/*` request mints a
  `Livepeer-Payment` envelope via `payment-daemon`. Live streams mint
  on session open and interim-debit during the session.
- **Capabilities come from the service registry.** No hardcoded
  capability list. `/v1/capabilities` reflects what the on-chain
  registry advertises.
- **No Stripe, no billing, no rate cards in v1.** Auth shape is
  waitlist → email verify → admin approval → API key by email.
- **VOD ingest lands in RustFS.** The compose stack stands up an
  S3-compatible RustFS, a one-shot bootstrap container creates the
  bucket + access key, the gateway presigns PUTs for VOD upload.
- **Capability workers (abr-runner, capability-broker) are external.**
  This repo talks to the Livepeer network; it does not carry runner
  implementations.
- **Single root `Makefile`.** Local dev entrypoints live at the repo root.

## Plan-as-code

Non-trivial work lands as an exec plan under
[`docs/exec-plans/active/`](./docs/exec-plans/active/). Completed plans
move to [`docs/exec-plans/completed/`](./docs/exec-plans/completed/).
Lightweight changes go straight to PR.
