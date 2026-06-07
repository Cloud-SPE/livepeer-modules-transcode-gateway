# Capability catalog

How the `capabilities` table stays fresh and what `/api/v1/capabilities`
returns.

> **Migration status:** as of PR-2 of the LOC migration the catalog is
> sourced from LOC's discovery API (`GET /v1/capabilities` on the
> clearinghouse) instead of the resolver daemon's
> `ListKnown`/`ResolveByAddress` pair.

## Why a cache

Querying LOC on every `/api/v1/capabilities` request would couple
catalog reads to clearinghouse availability. The cache decouples them
and keeps catalog reads ~10ms.

## Schema (recap)

```
capabilities (
  capability_id            text PK,
  offering_id              text NOT NULL,
  interaction_mode         text NOT NULL,
  name                     text,
  description              text,
  provider                 text,
  category                 text,
  eth_address              text,      -- NULL for LOC-era rows
  price_per_work_unit_wei  numeric,
  broker_url               text,      -- NULL for LOC-era rows
  extra_json               jsonb,     -- NULL for LOC-era rows
  constraints_json         jsonb,     -- NULL for LOC-era rows
  active                   boolean NOT NULL DEFAULT true,
  snapshot_at              timestamptz NOT NULL DEFAULT now()
)
```

`capability_id` is the composite identity `<capability>:<offering>`
(e.g. `video:transcode.live:gateway-ingest`). For v1 we treat this as
opaque PK.

**LOC catalog is narrower than the old resolver feed.** It carries
capability + offering + price + work unit only — no `eth_address`,
`broker_url`, `constraints`, or `extra`. That's deliberate: LOC owns
route selection, so the gateway advertising a specific broker would be
misleading. The columns stay in the schema (NULL) so pre-migration rows
remain readable; `interaction_mode` is derived locally from the
capability name (`guessInteractionMode`).

## Refresh loop

`gateway/internal/registry/refresh.go` runs a `time.Ticker` at
`REGISTRY_REFRESH_INTERVAL_MS`:

```
loop:
  caps = LOC GET /v1/capabilities
  rows = buildRows(caps, filter=[ABR_CAPABILITY, LIVE_CAPABILITY])
  begin tx:
    UPSERT each row
    UPDATE active=false WHERE capability_id NOT IN (rows.ids)
  commit tx
```

A failed refresh logs + retries on the next tick. The request path
never blocks on this; it reads `WHERE active=true` from the table.
`capability_refresh_meta` records every tick (outcome, row count,
filter) for the admin Registry view.

## `GET /api/v1/capabilities` response

```json
{
  "object": "list",
  "data": [
    {
      "id": "video:transcode.live:gateway-ingest",
      "capability": "video:transcode.live",
      "offering": "gateway-ingest",
      "interaction_mode": "live-session-gateway-ingest@v0",
      "name": "video:transcode.live",
      "category": "transcode",
      "price_per_work_unit_wei": "1000000000000"
    }
  ],
  "snapshot_at": "…"
}
```

`extra` / `constraints` / broker identity fields are omitted for
LOC-era rows (the JSON uses `omitempty`).

## Failure modes

| What | `/api/v1/capabilities` response |
|---|---|
| First refresh hasn't landed | `503 capabilities_cache_unavailable` |
| Last refresh older than `MAX_STALE` | `503 capabilities_cache_stale` |
| Refresh fine, zero capabilities | `200` with empty `data: []` (correct if the network advertises no matching capabilities right now). |
| LOC unreachable | refresh tick fails (visible in `capability_refresh_meta`); endpoint keeps serving the last snapshot. |

## What this doc does not cover

- How orchestrators publish capabilities — owned by the
  capability-broker + secure-orch-console.
- LOC's own discovery pass-through to the registry daemon — owned by
  the basic-pymnthouse repo.
