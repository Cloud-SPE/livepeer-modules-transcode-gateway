# Route selection (historical — LOC owns this now)

> **Superseded by the LOC migration.** The gateway no longer selects
> routes: the Livepeer Open Clearinghouse binds route selection to
> payment minting inside `POST /v1/jobs` (ABR) and `POST /v1/sessions`
> (live). The in-gateway `RouteSelector` / `Candidate` types, the
> failover dispatcher (`route_dispatch.go`), and the route-health
> cooldown tracker (`route_health.go`) were deleted. See
> [`payment-flow.md`](payment-flow.md) for the current flow.

## What replaced it

| Old concern | Where it lives now |
|---|---|
| Ranked candidate list (`SelectMany`) | LOC's registry pass-through; LOC picks the route at job/session create |
| Per-candidate payment mint | LOC `CreateJob` / `OpenSession` returns broker_url + envelope together |
| Failover loop over candidates | None in the gateway. Recovery from a broker failure = settle(0) + a fresh LOC create (LOC may pick a different broker) |
| Route-health cooldowns | Nothing equivalent yet — LOC has no outcome-aware ranking. Tracked as an upstream LOC feature request |
| Rotation retry (`INVALID_RECIPIENT_RAND`) | Inline in the ABR handler (settle(0) + recreate, once) and the live reconciler (refill retry once, then graceful wind-down) |

## Known regression (upstream issue to file)

The old dispatcher walked a ranked list with health-aware ordering, so
a flapping broker was skipped within one request. With LOC, a retry may
be handed the same broker back — LOC's discovery has no feedback loop
from dispatch outcomes. Proposed upstream fixes: an `exclude_brokers`
field on create, or outcome-aware ranking in the registry daemon.

## Open questions carried forward

- **Live route selection on re-allocation.** `/api/v1/live` still does
  not failover within a session; a "preferred broker" hint for
  re-allocation remains future work (now an LOC-side concern).
- **Quote-aware ABR ladder pricing.** Face value is still estimated
  from input duration (`estimated_units`); runner-reported actuals
  would tighten settlement.
