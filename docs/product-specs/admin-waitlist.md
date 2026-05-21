# Admin waitlist

Operator-facing surface for managing the waitlist queue.

## Routes

| Method | Path | Behavior |
|---|---|---|
| GET | `/api/admin/waitlist?status=&limit=&cursor=` | Paginated list. `status` ∈ `pending`, `approved`, `rejected`, `all`. |
| POST | `/api/admin/waitlist/:id/approve` | Approve → tx-insert `api_keys`, send key email. 409 if email not verified. |
| POST | `/api/admin/waitlist/:id/reject` | Reject. |
| POST | `/api/admin/waitlist/:id/resend-verification` | Re-mint verification token + re-send email. |
| GET | `/api/admin/users` | Aggregate view: waitlist row + active api_keys + recent usage. |
| GET | `/api/admin/usage` | Aggregate usage_reservations view (capability, outcome, latency). |

All routes require `X-Admin-Token`. Missing/invalid token → 401.
Token unset on the server → 503 `admin_disabled`.

## UI

`web/admin/` renders these as panels:

- **Waitlist queue** — `<cc-waitlist-queue>`: filter by status,
  approve/reject inline.
- **Users** — `<cc-users>`: drill into a single waitlist row.
- **Usage** — `<cc-usage>`: time-window aggregate.
- **Capability registry** — `<cc-registry>`: shows the current
  contents of the `capabilities` table (debug view for diagnosing
  registry-fed routes).

## Acceptance

- A pending waitlist row can be approved → user receives API key
  email in <10s.
- Approval refuses to act if email is not verified (returns 409).
- Re-approving an already-approved row is a no-op (idempotent).
- Rejection sets `status='rejected'`; no key is minted.
- Approval is atomic — the api_keys insert + waitlist update happen
  in a single transaction.

## What this surface does NOT do

- Multi-operator role separation (everyone with `ADMIN_TOKEN` has
  full power).
- Audit log retention (logs go to stdout).
- Bulk operations (per-row only in v1).
