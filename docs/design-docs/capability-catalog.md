# Capability catalog

How the `capabilities` table stays fresh and what `/api/v1/capabilities`
returns.

## Why a cache

Querying the resolver on every `/api/v1/capabilities` request would
couple catalog reads to chain availability. The cache decouples them
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
  eth_address              text,
  price_per_work_unit_wei  numeric,
  broker_url               text,
  extra_json               jsonb,
  constraints_json         jsonb,
  active                   boolean NOT NULL DEFAULT true,
  snapshot_at              timestamptz NOT NULL DEFAULT now()
)
```

`capability_id` is the composite identity the registry uses:
`<capability>:<offering>` (e.g.
`livepeer:transcode/abr-ladder:default`). For v1 we treat this as
opaque PK.

## Refresh loop

`gateway/internal/registry/refresh.go` runs a `time.Ticker` at
`REGISTRY_REFRESH_INTERVAL_MS`:

```
loop:
  candidates = resolver.ListKnown() + ResolveByAddress per addr
  rows = candidatesToCapabilityRows(candidates)
  begin tx:
    UPSERT each row
    UPDATE active=false WHERE capability_id NOT IN (rows.ids)
  commit tx
```

A failed refresh logs + retries on the next tick. The request path
never blocks on this; it reads `WHERE active=true` from the table.

## `GET /api/v1/capabilities` response

```json
{
  "object": "list",
  "data": [
    {
      "id": "livepeer:transcode/abr-ladder:default",
      "capability": "livepeer:transcode/abr-ladder",
      "offering": "default",
      "interaction_mode": "http-reqresp@v0",
      "name": "ABR ladder transcode (default)",
      "category": "transcode",
      "price_per_work_unit_wei": "1000000000",
      "extra": { "max_input_seconds": 7200 },
      "constraints": { … }
    },
    {
      "id": "video:transcode.live:gateway-ingest",
      "capability": "video:transcode.live",
      "offering": "gateway-ingest",
      "interaction_mode": "live-session-gateway-ingest@v0",
      "name": "Live RTMP→HLS ABR",
      "category": "live",
      "price_per_work_unit_wei": "2000000000",
      "extra": { … }
    }
  ],
  "snapshot_at": "…"
}
```

## Failure modes

| What | `/api/v1/capabilities` response |
|---|---|
| First refresh hasn't landed | `503 capabilities_cache_unavailable` |
| Last refresh older than `MAX_STALE` | `503 capabilities_cache_stale` |
| Refresh fine, zero capabilities | `200` with empty `data: []` (correct if no orchestrators advertise transcode capabilities right now). |

## What this doc does not cover

- How orchestrators publish capabilities — owned by the
  capability-broker + secure-orch-console.
- The resolver's manifest fetch path — owned by
  `service-registry-daemon`.
