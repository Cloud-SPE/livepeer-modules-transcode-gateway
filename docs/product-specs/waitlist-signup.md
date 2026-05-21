# Waitlist signup

Public surface that takes a name + email and gets the user onto the
gated path to an API key.

## Routes

| Method | Path | Auth | Behavior |
|---|---|---|---|
| POST | `/api/public/waitlist` | none | Idempotent insert into `waitlist`; returns `{ok: true}` whether new or duplicate (no enumeration). Sends a verification email. |
| GET | `/api/public/verify?token=…` | token | Sets `email_verified_at`; clears `verification_token_hash`. Returns `{ok: true, message: …}`. |

## Request bodies

```json
POST /api/public/waitlist
{ "name": "Alice", "email": "alice@example.com" }
```

`name` 1–200 chars, `email` validated by huma (`format: email`).

## Rate limits

- `/api/public/waitlist`: 5 / hour per `ip_hash` (peppered SHA-256). Excess
  returns 429.

## Email shape

- Subject: "Verify your Livepeer Video Gateway signup"
- Body: "Hi {name}, click to verify: {PUBLIC_SITE_URL}/verify.html?token={token}"

Without `RESEND_API_KEY`, the email is logged to stdout with
`would have sent`.

## UI

`web/site/index.html` shows a single signup form (`<cc-signup-form>`)
and a `/verify.html` landing page (`<cc-verify-card>`). Branding:
"Livepeer Video Gateway" — rebrand by editing the HTML/CSS only.

## State transitions

```
(no row) ──POST /api/public/waitlist──► pending, email_verified_at=NULL
pending ──GET /api/public/verify?token=…──► pending, email_verified_at=now()
pending ──POST /api/admin/waitlist/:id/approve──► approved + api_keys row + key emailed
pending ──POST /api/admin/waitlist/:id/reject──► rejected
```

## Acceptance

- A new email round-trips through signup → verify → admin approve →
  API key delivery in <10s end-to-end (Resend latency dominates).
- A duplicate signup gets the same `200 {ok: true}` shape (no leak).
- An expired verification token returns `400 verification_token_expired`.
- Admin approval refuses to act on `email_verified_at IS NULL`
  (HTTP 409).
