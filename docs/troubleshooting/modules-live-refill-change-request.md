# Change request: paid live refill admission and refusal diagnostics

Owner: Livepeer network modules team (`livepeer-network-modules`, capability broker and receiver/payment admission). Gateway tracking: `vgw-e2o`, investigation `vgw-j2o`.

OBS connected and published successfully, then was disconnected because the broker exhausted its original authorization after refusing the first refill. Please trace and repair admission of valid successor authorizations, and expose the precise receiver refusal reason.

## Confirmed evidence

- 11:46:15: initial authorization issued for 120 units. OBS authenticated at 11:46:40.
- 11:48:23: LOC issued revision 1, cumulative cap 180 units.
- 11:48:23.823: broker persisted HTTP 409 `refill_refused`: “authorization revision was not admitted; existing authority retained”.
- 11:48:55.380: broker ended with `authorization_exhausted`, claimed 124 units, debited 120 units; `payment_closed=true`, `runner_terminated=true`. Original authorization remained authoritative.

The underlying receiver rejection reason was not available in inspected logs. Funding shortage and the earlier reservation-sizing bug are not established causes of this incident. Current source already bounds remaining revision reservations; do not assume deploying that earlier fix resolves this case.

## Requested changes

1. Trace `capability-broker/internal/sessionengine/revisions.go`, especially `resolveRevisionLocked`, `remainingRevisionReservation`, and the receiver `AdmitAuthorization` / `CancelAuthorizationAdmission` result. Determine why this valid-looking successor was refused; repair the demonstrated cause.
2. Preserve a structured, non-secret refusal reason and correlated request/session/revision identifiers in broker and receiver diagnostics. Keep definitive refusal distinguishable from pending admission, transport failure, and unknown outcome.
3. Preserve predecessor authority until successor admission is confirmed. Make retry, cancellation, restart, and terminal settlement deterministic and idempotent. Never bypass admission or silently extend paid authority.
4. Ensure terminal signed evidence identifies the actually admitted authorization and billed usage, even when a successor was issued but refused. Coordinate that evidence contract with LOC.

## Acceptance

Run a real paid OBS/RTMP session through several refill boundaries, longer than the initial 120-unit allowance. Confirm uninterrupted media and increasing admitted allowance. Exercise receiver refusal, lost admission response, lost top-up response, broker restart, cancellation, and stop during refill. Verify no duplicate debit, no authority from a refused grant, no permanent unresolved revision, and signed final evidence accepted by LOC. Retain bounded shutdown when allowance truly exhausts.

The gateway now distinguishes definitive `refill_refused` from ambiguous errors, stops replaying confirmed refusal, requests graceful termination, and separates ended media from pending accounting. This limits misleading reconnect/status behavior; it cannot repair receiver admission.

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

