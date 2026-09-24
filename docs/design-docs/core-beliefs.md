# Core beliefs

The non-negotiable invariants of this codebase. Anything that violates
these is a bug, not a tradeoff. Updating one of these requires an
Bead and design note that explain *why* and lands the rule change atomically
with the code change.

---

## 1. The repo is the system of record

Code and design rationale are checked in; work status and dependencies live
in Beads and synchronize through Dolt. No Google Docs of
load-bearing intent. No Slack threads as the source of truth for a
design decision. Discussions promote into docs or they evaporate.

## 2. Boring tech, agent-legible

We pick boring, well-documented, small-surface-area dependencies
(Postgres, chi, huma, pgx, sqlc, Lit, esm.sh, MinIO) because agents
reason better about them and the runtime model is predictable.

Reaching for an exotic dependency is a signal to create a Bead and
justify the choice.

## 3. The wire spec is product-agnostic

LOC and broker clients implement `paid-job/v1` and `paid-session/v1`,
spend authorization, delegated caller proof, and signed settlement evidence.
They do not invent transcode semantics. ABR workload bodies and RTMP/HLS
session descriptors are mapped at the product boundary. Capability protocol,
work unit, estimator, axes, and price denominator come from LOC discovery.

## 4. The SaaS shell is product-agnostic

Auth, waitlist, sessions, admin, API-key minting are the same code as
the openai gateway. We don't fork them per product surface.

## 5. Authorize once; settle measured evidence

Persist a stable workload, idempotency key, and caller identity before network
side effects. LOC owns network funding; the gateway signs delegated caller
proofs. Settle broker-signed measured evidence, never an estimate or elapsed
wall-clock guess. Ambiguous dispatch outcomes stay recoverable and must not
be interpreted as proof that zero work occurred.

## 6. Transcoding belongs to runners

VOD media and HLS outputs move directly between object storage and runners.
The gateway does not run FFmpeg. Gateway-owned live ingress is the explicit
exception: its RTMP listener relays media to the runner endpoint in the
advertised session descriptor, preserving a stable customer ingest URL.

## 7. On-chain only, no static overlays

There is no `LIVEPEER_STATIC_OVERLAY_*` env-var path. There is no
hand-rolled "if no orchestrators advertise, fall back to localhost".
`/api/v1/*` 502s when the registry has no candidates, and that's
correct — the gateway is a thin window onto the Livepeer network's
actual state.

## 8. Capabilities reflect reality

`/api/v1/capabilities` returns what LOC's catalog advertises *right now*,
refreshed every `REGISTRY_REFRESH_INTERVAL_MS`. There is no hand-
curated catalog. If a capability disappears on-chain, it disappears
from the API within one refresh cycle.

## 9. Validate at the boundary

huma validates HTTP bodies via struct-tag → JSON Schema. envconfig
validates env vars at startup. Internal code trusts internal types.
No defensive validation deep in the call stack — that's noise.

## 10. Beads is the work graph

Create and claim a described Bead before edits. Beads owns all task status,
debt, dependencies, acceptance, and handoffs. Design documents preserve
rationale and reference Bead IDs; they do not duplicate the work queue.
Historical execution plans remain archives. Run `bd prime` on session start
and context recovery. See [PLANS.md](../../PLANS.md).

## 11. Throughput over ceremony

PRs are short-lived. Test flakes are usually retries, not blockers.
Corrections are cheap; waiting is expensive. In a low-throughput
environment this would be irresponsible. Here it's correct.

---

## Promoting a new belief

A new entry in this file requires:

1. A note in the corresponding Bead describing the trigger.
2. Cross-reference from at least one design-doc or product-spec that
   depends on it.
3. Reviewer sign-off — this is the file that changes the rules.

## Retiring a belief

Same process in reverse. If a rule has stopped earning its keep,
record the change and rationale in a Bead and design document.
