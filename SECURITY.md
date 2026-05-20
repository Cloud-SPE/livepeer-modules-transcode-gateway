# SECURITY

Threat model + auth surface for the gateway. Updated as the surface
evolves.

## Auth surfaces

| Surface | Auth | Notes |
|---|---|---|
| `/v1/*` | `Authorization: Bearer sk-…` | API key only. No cookie session accepted. |
| `/portal/*` | Cookie session | Issued via `/portal/login` (trades an API key for a session). HttpOnly, SameSite=Lax, Secure when `BASE_URL` starts with `https://`. |
| `/admin/*` | `X-Admin-Token` header | Env-var token. No admin user table — see ["What `ADMIN_TOKEN` is and is not"](#what-admin_token-is-and-is-not). |
| `/api/waitlist` | None (public) | Validated, rate-limited per IP-hash (5 signups / hour). |
| `/api/verify` | None (public) | Single-use, expiring token in the URL. |
| `/openapi.json`, `/docs` | None (app-layer) | Gate behind reverse-proxy auth in production. |
| `/metrics` | Bearer token (optional) | If `METRICS_TOKEN` is set, requires `Authorization: Bearer <token>`. |
| RustFS direct access | S3 access key (out-of-band) | The gateway holds the only access key that can PUT/GET the ingest bucket; clients PUT only via gateway-issued presigned URLs. |

## API key lifecycle

1. Public signup via `POST /api/waitlist` → row in `waitlist`,
   `status='pending'`, verification token stored *hashed*.
2. User clicks the verification link → `email_verified_at` set.
3. Admin (with `X-Admin-Token`) approves via
   `POST /admin/waitlist/:id/approve`. Handler refuses if email
   unverified (HTTP 409).
4. Approval transactionally inserts `api_keys` and sets
   `status='approved'`. Plaintext key emailed once (or logged to
   stdout if Resend disabled).
5. User uses the key on `/v1/*`. Server-side lookup by SHA-256 hash of
   `key + API_KEY_HASH_PEPPER`.
6. User can list / mint / revoke keys from the portal. Revoking a key
   cascade-revokes every session using it.

## Hashing

- **API keys**: SHA-256 with `API_KEY_HASH_PEPPER`. Rotating the
  pepper invalidates every existing key (no dual-lookup in v1).
- **Verification tokens**: hashed before storage with `IP_HASH_PEPPER`.
  Expire 24h after issue.
- **Session tokens**: opaque 32 random bytes (base64url), hashed at
  rest with `IP_HASH_PEPPER`. Plaintext lives only in the cookie.
- **Stream keys** (for `/v1/live`): generated server-side, returned
  once to the client, hashed at rest in `live_streams.stream_key_hash`
  with `IP_HASH_PEPPER`. The plaintext stream key authenticates the
  RTMP push to the broker.
- **Client IPs**: SHA-256 with `IP_HASH_PEPPER`. Without the pepper,
  IPs are confirmable via rainbow table — startup warns if unset.

## What `ADMIN_TOKEN` is and is not

- It **is** the canonical admin credential. Every `/admin/*` request
  carries it as `X-Admin-Token`. Comparison is constant-time
  (`subtle.ConstantTimeCompare`).
- It **is not** a "bootstrap" mechanism — there is no separate "real
  admin user" to promote to. v1 has no admin user table by design.
- Unset → every `/admin/*` request returns `503 admin disabled`.
- Leak = full admin takeover. Store like a database password.

## Per-API-key rate limit

`/v1/*` is rate-limited per `api_key_id` via an in-memory token bucket.
Defaults: **60 req/min, burst 30**. Exhaustion returns `429
rate_limit_exceeded` with `Retry-After`. Reservation NOT opened.

## RustFS / S3 security

- The gateway is the only holder of S3 credentials with PUT/DELETE
  rights. Clients PUT only via gateway-issued presigned URLs.
- Bucket `lvp-video-ingest` is **anonymous-read** in dev (so the
  runner can pull). In production, configure read-restricted access
  + use signed download URLs at job-dispatch time.
- Presigned URLs default to `S3_PRESIGN_TTL_SECONDS=3600`. Shorter
  values are safer; longer values aid resumable uploads.
- Object keys include a UUID prefix (`abr/<api_key_id>/<uuid>/...`) to
  prevent guessing and enable per-user GC.

## Threats

| Threat | Mitigation |
|---|---|
| API key brute-force | 288 bits of entropy, peppered SHA-256, per-key 429 rate limit, constant-time hash compare. |
| Waitlist email enumeration | `POST /api/waitlist` returns identical `{ok: true}` on conflict vs new. |
| Verification-token replay | Tokens hashed at rest and expire (24h). Once consumed, the hash is cleared. |
| Session fixation | Cookie issued only after successful API-key validation. |
| Session theft via XSS | Cookie is HttpOnly, SameSite=Lax, Secure when BASE_URL is HTTPS. |
| Open redirect on email links | Verify + key-delivery URLs constructed server-side from `PUBLIC_SITE_URL` / `PUBLIC_PORTAL_URL`. |
| CORS abuse | `ALLOWED_ORIGINS` env var; default `*` in dev. |
| Admin-token brute-force | Constant-time compare; rate-limit at reverse proxy if exposed publicly. |
| Stream-key replay | Stream keys are random 32+ bytes, single-stream-bound; broker tears down on reuse. |
| Upload bucket flooding | Bucket has per-user prefix isolation; future plan adds quota on `live_streams` + `usage_reservations` counts per `api_key_id`. |
| OpenAPI spec disclosure | Gate `/openapi.json` + `/docs` behind reverse-proxy auth in prod. |

## Secrets and configuration

**Required for production**:

- `DATABASE_URL`
- `BASE_URL`, `PUBLIC_SITE_URL`, `PUBLIC_PORTAL_URL`
- `ADMIN_TOKEN`
- `API_KEY_HASH_PEPPER`
- `IP_HASH_PEPPER`
- `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` (or your S3-compatible store creds)

**Optional but strongly recommended**:

- `METRICS_TOKEN`
- `RESEND_API_KEY`
- `LIVEPEER_RESOLVER_SOCKET`, `LIVEPEER_PAYER_DAEMON_SOCKET`
- `ALLOWED_ORIGINS`

All secrets injected via env. No secrets in code, migrations, or docs.

## Out of scope (v1)

- OAuth, SSO, social login.
- Per-API-key scoping (read-only keys, capability-restricted keys).
- Self-service API-key recovery / magic-link login.
- Multi-operator role separation on the admin surface.
- Pepper rotation without invalidating existing keys.
- Penetration testing. This is beta.
