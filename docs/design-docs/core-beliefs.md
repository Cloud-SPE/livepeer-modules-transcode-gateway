# Core beliefs

The non-negotiable invariants of this codebase. Anything that violates
these is a bug, not a tradeoff. Updating one of these requires an
exec plan that explains *why* and lands the rule change atomically
with the code change.

---

## 1. The repo is the system of record

If it isn't checked in, it doesn't exist. No Google Docs of
load-bearing intent. No Slack threads as the source of truth for a
design decision. Discussions promote into docs or they evaporate.

## 2. Boring tech, agent-legible

We pick boring, well-documented, small-surface-area dependencies
(Postgres, chi, huma, pgx, sqlc, Lit, esm.sh, RustFS) because agents
reason better about them and the runtime model is predictable.

Reaching for an exotic dependency is a signal to write a plan and
justify the choice.

## 3. The wire spec is product-agnostic

Anything in `gateway/internal/proxy/livepeer/` and
`gateway/internal/proxy/service/` only knows about
`Livepeer-Capability`, `Livepeer-Payment`, broker interaction modes,
and route candidates. It never knows about transcode-specific
concepts. Mapping "user wants an ABR ladder" → "capability =
`livepeer:transcode/abr-ladder`, mode = http-reqresp" lives in
`gateway/internal/proxy/{abr,live}.go`.

The wire layer is the shared piece across this gateway and
`livepeer-modules-openai`. Diverging it bites both repos.

## 4. The SaaS shell is product-agnostic

Auth, waitlist, sessions, admin, API-key minting are the same code as
the openai gateway. We don't fork them per product surface.

## 5. The gateway pays the network exactly once per attempted upstream call

- For VOD: a payment envelope is minted per broker attempt. Failover
  to a second broker mints a second envelope.
- For live: a payment envelope is minted at session-open;
  interim-debits happen through the payment-daemon over the session
  lifetime. There is no "free idle" — when balance hits zero, the
  broker tears down ingest.

There is exactly one payment-minting code path
(`gateway/internal/proxy/livepeer/payment.go`). Surfaces that want
payments call into it.

## 6. Media bytes never traverse the gateway

The gateway signs URLs and tracks reservations. It does not buffer
video. It does not run FFmpeg. It does not parse HLS playlists at
request time. All of that is broker + runner side. Violating this is
a signal that scope has crept and we should reconsider.

## 7. On-chain only, no static overlays

There is no `LIVEPEER_STATIC_OVERLAY_*` env-var path. There is no
hand-rolled "if no orchestrators advertise, fall back to localhost".
`/v1/*` 502s when the registry has no candidates, and that's correct
— the gateway is a thin window onto the Livepeer network's actual
state.

## 8. Capabilities reflect reality

`/v1/capabilities` returns what the resolver advertises *right now*,
refreshed every `REGISTRY_REFRESH_INTERVAL_MS`. There is no hand-
curated catalog. If a capability disappears on-chain, it disappears
from the API within one refresh cycle.

## 9. Validate at the boundary

huma validates HTTP bodies via struct-tag → JSON Schema. envconfig
validates env vars at startup. Internal code trusts internal types.
No defensive validation deep in the call stack — that's noise.

## 10. Plans are first-class

Non-trivial work writes a plan first under
`docs/exec-plans/active/NNNN-slug.md`. The plan is mostly markdown,
lands quickly, and gets implemented against. Completion = `git mv`
to `completed/`.

The plan template is in [`PLANS.md`](../../PLANS.md).

## 11. Throughput over ceremony

PRs are short-lived. Test flakes are usually retries, not blockers.
Corrections are cheap; waiting is expensive. In a low-throughput
environment this would be irresponsible. Here it's correct.

---

## Promoting a new belief

A new entry in this file requires:

1. A note in the corresponding exec plan describing the trigger.
2. Cross-reference from at least one design-doc or product-spec that
   depends on it.
3. Reviewer sign-off — this is the file that changes the rules.

## Retiring a belief

Same process in reverse. If a rule has stopped earning its keep,
write a plan that removes it and explains why.
