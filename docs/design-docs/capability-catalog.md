# Capability catalog

The background refresher reads LOC `GET /v1/capabilities`, filters to the
configured ABR/live capability IDs, and atomically replaces the active
Postgres snapshot. Disappearing offerings become inactive. Refresh failures
retain the previous snapshot and record their error in
`capability_refresh_meta` for diagnostics.

Each offering retains its advertised `protocol`, `work_unit`,
`units_per_price`, `price_per_work_unit_wei`, `work_unit_estimator`, `job`,
`session`, and opaque `extra` metadata. Decimal price and denominator values
are stored without floating-point conversion. A displayed price must keep
the denominator: the wei amount applies to `units_per_price` work units.

The protocol is never guessed from a capability name or offering label.
The legacy interaction-mode column is left empty. Broker identity is absent
from the aggregate catalog because LOC owns route selection. Actual route
bindings returned during authorization provide execution identity.

Migration 0012 invalidates legacy active snapshots because they omit these
fields; a successful LOC refresh repopulates them. Unknown future JSON axes
and estimator fields survive as JSON rather than being silently dropped.

The public catalog and admin catalog expose this metadata. See
[Modules v2](modules-v2.md) for the broader contract and recovery boundary.
