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
