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
make dev               # gateway + db + rustfs + bootstrap
make dev-livepeer      # adds payer + resolver daemons

# Run the three SPAs (each in its own terminal, or one command)
make web

# Verify end-to-end
make smoke
```

You don't need a Resend account for local dev. When `RESEND_API_KEY`
is unset, verification + API-key emails are logged to stdout.

For a fully working `/v1/*` stack you do need: chain RPC, registry
address, payer keystore. This repo is on-chain only; there is no
local fallback broker mode.

---

## How work lands

### Small changes — go straight to a PR

Bug fixes, doc tweaks, single-file refactors, anything <50 lines.

### Non-trivial changes — write an exec plan first

Open the plan at `docs/exec-plans/active/NNNN-slug.md`, following
the template in [`PLANS.md`](./PLANS.md). Land the plan first (small
PR, mostly markdown), then implement. On completion, append a
`## Outcome` section and `git mv` to `docs/exec-plans/completed/`.

"Non-trivial" includes: new HTTP endpoints, schema changes,
cross-component refactors, new dependencies, anything you're not sure
how to scope.

---

## What good code looks like here

- **Boring technology.** Postgres. Go stdlib + chi + huma. pgx. sqlc.
  Lit. esm.sh. If you reach for an exotic dep, write a plan.
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
  you need to change them, write an exec plan first.
- **The Livepeer wire spec.** Owned by `livepeer-network-protocol`
  upstream, not here.

---

## Commit style

- Subject line: imperative, ≤72 chars. Examples:
  - `fix: refund usage_reservation on /v1/abr broker timeout`
  - `gateway: add /admin/live-streams CSV export`
  - `docs: tighten core-beliefs §3 wording`
- Body wraps ~72 chars; explains *why*.
- Link issues / plan files as relative paths from repo root.
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
