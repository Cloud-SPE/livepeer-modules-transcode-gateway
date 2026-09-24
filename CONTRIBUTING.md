# Contributing

This repo follows an **agent-first harness pattern**
([reference](./docs/references/openai-harness-engineer.md)) — the
conventions matter more than the code style.

Read these three files first:

1. [`AGENTS.md`](./AGENTS.md) — the map.
2. [`docs/design-docs/core-beliefs.md`](./docs/design-docs/core-beliefs.md) — invariants.
3. [`ARCHITECTURE.md`](./ARCHITECTURE.md) — system shape + data flows.

Together they're under 600 lines.

---

## Dev environment

```bash
# Go toolchain (1.25+) and Node 24 + pnpm 10 required.
pnpm install
make dev               # gateway + db + minio + bootstrap

# Run the three SPAs (each in its own terminal, or one command)
make web

# Verify end-to-end
make smoke
```

You don't need a Resend account for local dev. When `RESEND_API_KEY`
is unset, verification + API-key emails are logged to stdout.

For a fully working `/api/v1/*` stack you need a funded LOC API key and
v2-compatible network brokers and runners. This repo is on-chain only; there is no
local fallback broker mode.

---

## Local contract and recovery tests

CI runs Go tests with the race detector and a disposable PostgreSQL 16 service.
The paid-operation integration tests apply the real migrations to a unique
schema per test, use local HTTP fixtures for LOC/brokers, and remove their
schemas afterward. They do not spend funds or connect to production.

To run them locally, point `TEST_DATABASE_URL` at a disposable database whose
user can create schemas and extensions, then run:

```bash
make embed-webroot
cd gateway
TEST_DATABASE_URL='postgres://postgres@127.0.0.1:5432/gateway_test?sslmode=disable' \
  go test -race ./...
```

Without `TEST_DATABASE_URL`, database integration tests explicitly skip;
unit tests still run. This is separate from `make smoke`, which exercises a
running deployment. The integration suite covers persisted request identity,
concurrent retries, tenant boundaries, encrypted recovery material, signed
evidence rejection, lost responses, live refill/stop, and worker isolation.

## Running end-to-end checks against local services

`scripts/e2e.py` exercises actual HTTP endpoints and uses logged development
emails to complete signup, verification and approval. Set `RESEND_API_KEY` empty;
the harness refuses real email delivery. It requires Python 3, Docker and FFmpeg.
The environment file must contain the local administrator and metrics tokens.
Secrets and resumable test identities are saved under gitignored `.dev/e2e/`.

```bash
python3 scripts/e2e.py --phase shell
python3 scripts/e2e.py --phase abr
python3 scripts/e2e.py --phase live
```

Defaults target `http://localhost:14000`, `.env.e2e.local`, and Docker container
`vgw-e2e-gateway`; override `--url`, `--env-file`, `--container`, or `--state-dir`
for another local deployment. The shell phase checks the apps, authentication,
key revocation, namespace isolation, metrics and an S3 upload/download roundtrip.
It reports dependency health separately; a passing shell phase does not certify
LOC availability.

The explicit ABR/live phases use real LOC credit and real network runners.
ABR uses a three-second generated input and verifies returned HLS artifacts
and signed accounting. Live publication is bounded to 35 seconds, verifies HLS
segments, then requests stop in a `finally` block. Repeated ABR execution reuses
the same idempotency key; it does not intentionally start another paid job.
A live admission failure can leave an unknown LOC issuance outcome: retain its
journal and stop request until authoritative recovery finishes. Never replace
that identity or clear database rows to make a test appear successful.

## How work lands

Create and claim a Bead before every change. Use `bd prime` and the
[project Beads skill](.agents/skills/beads/SKILL.md), with the workflow in
[PLANS.md](PLANS.md). Non-trivial work uses an epic and dependency edges;
keep design rationale in `docs/design-docs/` linked to the bead. Do not
maintain Markdown task lists or execution-plan status alongside Beads.

---

## What good code looks like here

- **Boring technology.** Postgres. Go stdlib + chi + huma. pgx. sqlc.
  Lit. esm.sh. If you reach for an exotic dep, record the rationale in the bead and design docs.
- **Strict types.** Go's compiler is the lint gate. `go vet ./...`
  must pass. Avoid `interface{}` / `any` outside narrow boundaries.
- **Light DOM.** See [`FRONTEND.md`](./FRONTEND.md): no shadow DOM,
  no inline styles, no bundler. CSS lives in checked-in `.css` files.
- **Validate at the boundary.** huma's struct-tag validation handles
  HTTP boundaries; env-var parsing validates at startup.
- **Tests at the load-bearing seams.** `gateway/...` tests cover pure
  helpers (`internal/crypto`, route selection, payment client) and
  integration paths through the smoke flow.
- **No comments that just restate the code.** Comments earn their
  keep by explaining *why* something is non-obvious.

---

## Things to leave alone

- **`gateway/internal/proxy/livepeer/`** and **`gateway/internal/proxy/service/`**.
  These are load-bearing wire mechanics ported from the upstream
  `livepeer-network-modules` ecosystem. Divergence is expensive. If
  you need to change them, create a bead and document the wire-contract rationale first.
- **The Livepeer wire spec.** Owned by `livepeer-network-protocol`
  upstream, not here.

---

## Commit style

- Subject line: imperative, ≤72 chars. Examples:
  - `fix: refund usage_reservation on /api/v1/abr broker timeout`
  - `gateway: add /api/admin/live-streams CSV export`
  - `docs: tighten core-beliefs §3 wording`
- Body wraps ~72 chars; explains *why*.
- Reference Bead IDs and link design documents as relative paths from repo root.
- We do **not** use Co-Authored-By trailers in this repo.

---

## Reporting bugs

Open an issue with:

1. What you expected
2. What happened
3. How to reproduce (curl commands or repro repo welcome)
4. Gateway version (`git rev-parse --short HEAD`) + relevant env
   (Go version, Docker version, OS)

Security issues: email the maintainer directly. See
[`SECURITY.md`](./SECURITY.md).

---

## Code of conduct

Be kind. Disagreements about technical direction are welcome.
Personal attacks aren't.
