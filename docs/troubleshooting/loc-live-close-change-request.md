# Change request: reconcile live close after an issued but refused refill

Owner: LOC team (paid-session refill, authorization grant ledger, signed settlement validation). Gateway tracking: `vgw-e2o`, investigation `vgw-j2o`.

A broker-ended live session remains open in LOC: closing with the broker's predecessor settlement returns HTTP 400 after LOC issued a successor that the broker definitively refused. Please determine the exact validation failure and make this lifecycle close safely and idempotently.

## Confirmed evidence and uncertainty

LOC issued cumulative cap 180, refill sequence 1, at 11:48:23 UTC. Broker refused that successor at 11:48:23.823 and retained the predecessor (cap 120). At 11:48:55.380 the broker ended with `authorization_exhausted`, claimed 124 but debited 120, with payment closed and runner terminated. Gateway settlement retries received LOC HTTP 400; LOC payment session remained open and both authorization-grant revisions were listed as issued.

The exact HTTP 400 validation reason is not established. A mismatch between latest-issued authority and actually-admitted authority is a hypothesis to investigate, not a proven LOC defect. Please correlate the identifiers below with validation logs and return the concrete error code and failing invariant.

## Requested changes

1. Verify close can validate signed terminal evidence from the last actually admitted predecessor when a newer grant was issued but definitively refused/cancelled. Validate the complete session, issuer, audience, authorization lineage, signature, and usage scope; do not simply relax latest-revision validation.
2. Reconcile the unused successor through authenticated broker/receiver admission and cancellation evidence. An issued grant alone does not prove admission, and a gateway assertion alone must not authorize releasing funds. Preserve ambiguous outcomes until evidence resolves them.
3. Close and account exactly once, release only verified unused reservations, and make duplicate close and response-loss recovery return consistent final accounting through the session read API and janitor.
4. Expose a stable machine-readable validation reason with correlated session and authorization identifiers. Avoid generic HTTP 400 as the only operational signal.

## Acceptance

Cover predecessor admitted / successor refused, successor admitted, admission unknown, duplicate close, lost close response, stale evidence, and mismatched signatures/session IDs. For this incident, reconcile billed usage against signed debit evidence (120), not merely claimed consumption (124). Verify the payment session reaches a final state without duplicate billing, fabricated zero-usage settlement, or releasing funds on an unknown outcome. Add a joint modules/LOC paid live test spanning several refills and final stop.

The gateway now shows ended/failed media with `settlement_pending=true`, closes ingest, and retries accounting without committing or refunding usage prematurely. This corrects the product state but does not make LOC accept rejected evidence.

## Incident identifiers

Observed 2026-09-25 UTC on EU production; broker and receiver revision `747a085d8164`, runner revision `c3aa899`.

| Identity | Value |
|---|---|
| Gateway live session | `5f64c6b8-afc3-4eca-8655-8b63690267b5` |
| LOC payment session | `d21fde63-5803-43a9-81b4-ddd6b2b479a9` |
| Broker session | `sess_26fe55e0-1f09-480a-bbc2-91549368dc27` |
| Runner session | `runner_a36507721a56b180fcceb421b6dbc43a` |
| Initial request | `0894ae51-0144-4bcc-84d6-eabb494b0645` |
| Predecessor authorization | `loc-auth:0894ae51-0144-4bcc-84d6-eabb494b0645` |
| Refill request | `b35b4f41-44ef-4e21-b5a3-e4d901effd1e` |
| Successor authorization | `loc-auth:b35b4f41-44ef-4e21-b5a3-e4d901effd1e` |

