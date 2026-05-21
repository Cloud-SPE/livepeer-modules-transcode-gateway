# SECURITY

Threat model + auth surface for the gateway. Updated as the surface
evolves.

## Auth surfaces

| Surface | Auth | Notes |
|---|---|---|
| `/api/v1/*` | `Authorization: Bearer sk-…` | API key only. No cookie session accepted. |
| `/api/portal/*` | Cookie session | Issued via `/api/portal/login` (trades an API key for a session). HttpOnly, SameSite=Lax, Secure when `BASE_URL` starts with `https://`. |
| `/api/admin/*` | `X-Admin-Token` header | Env-var token. No admin user table — see ["What `ADMIN_TOKEN` is and is not"](#what-admin_token-is-and-is-not). |
| `/api/public/waitlist` | None (public) | Validated, rate-limited per IP-hash (5 signups / hour). |
| `/api/public/verify` | None (public) | Single-use, expiring token in the URL. |
| `/api/webhooks/abr` | HMAC + per-job secret | Runner callback for ABR job status; secret minted at dispatch time. |
| `/openapi.json`, `/docs` | None (app-layer) | Gate behind reverse-proxy auth in production. |
| `/metrics` | Bearer token (optional) | Stays at root for Prometheus convention. If `METRICS_TOKEN` is set, requires `Authorization: Bearer <token>`. |
| `/health` | None | Stays at root for load-balancer convention. |
| RTMP `:1935` | Per-stream key (peppered SHA-256 in `live_streams.stream_key_hash`) | Gateway is the public RTMP endpoint; valid stream key required to publish; broker authenticates the gateway's upstream relay independently. |
| MinIO direct access | S3 access key (gateway-only) + per-session STS creds (runners) | The gateway holds the only long-lived S3 access key. VOD: clients PUT via presigned URLs. Live: the runner gets short-lived STS creds scoped to `live-out/<api_key>/<live_id>/*` only. |

## API key lifecycle

1. Public signup via `POST /api/public/waitlist` → row in `waitlist`,
   `status='pending'`, verification token stored *hashed*.
2. User clicks the verification link (`GET /api/public/verify`) →
   `email_verified_at` set.
3. Admin (with `X-Admin-Token`) approves via
   `POST /api/admin/waitlist/:id/approve`. Handler refuses if email
   unverified (HTTP 409).
4. Approval transactionally inserts `api_keys` and sets
   `status='approved'`. Plaintext key emailed once (or logged to
   stdout if Resend disabled).
5. User uses the key on `/api/v1/*`. Server-side lookup by SHA-256
   hash of `key + API_KEY_HASH_PEPPER`.
6. User can list / mint / revoke keys from the portal. Revoking a key
   cascade-revokes every session using it.

## Hashing

- **API keys**: SHA-256 with `API_KEY_HASH_PEPPER`. Rotating the
  pepper invalidates every existing key (no dual-lookup in v1).
- **Verification tokens**: hashed before storage with `IP_HASH_PEPPER`.
  Expire 24h after issue.
- **Session tokens**: opaque 32 random bytes (base64url), hashed at
  rest with `IP_HASH_PEPPER`. Plaintext lives only in the cookie.
- **Stream keys** (for `/api/v1/live`): generated server-side, returned
  once to the client, hashed at rest in `live_streams.stream_key_hash`
  with `IP_HASH_PEPPER`. The plaintext stream key authenticates the
  RTMP push to the gateway's `:1935` listener; the gateway's upstream
  relay to the orchestrator's private RTMP endpoint carries its own
  broker-issued credential and never exposes the customer key.
- **Client IPs**: SHA-256 with `IP_HASH_PEPPER`. Without the pepper,
  IPs are confirmable via rainbow table — startup warns if unset.

## What `ADMIN_TOKEN` is and is not

- It **is** the canonical admin credential. Every `/api/admin/*`
  request carries it as `X-Admin-Token`. Comparison is constant-time
  (`subtle.ConstantTimeCompare`).
- It **is not** a "bootstrap" mechanism — there is no separate "real
  admin user" to promote to. v1 has no admin user table by design.
- Unset → every `/api/admin/*` request returns `503 admin disabled`.
- Leak = full admin takeover. Store like a database password.

## Per-API-key rate limit

`/api/v1/*` is rate-limited per `api_key_id` via an in-memory token
bucket. Defaults: **60 req/min, burst 30**. Exhaustion returns `429
rate_limit_exceeded` with `Retry-After`. Reservation NOT opened.

## MinIO / S3 security

- The gateway is the only holder of the long-lived S3 access key with
  full PUT/GET/DELETE rights. Clients PUT only via gateway-issued
  presigned URLs.
- **Per-live-session STS scoping.** When a live session opens, the
  gateway calls MinIO STS `AssumeRole` with an inline policy that
  permits only `s3:PutObject` / `s3:DeleteObject` /
  `s3:AbortMultipartUpload` against `live-out/<api_key>/<live_id>/*`.
  The runner gets those temp creds (access key + secret + session
  token + ExpiresAt). MinIO enforces the scope server-side, so a
  compromised runner can only write within its own session's prefix.
  The gateway's long-lived bucket credentials never leave the gateway
  process.
- Bucket `lvp-video-ingest` is **anonymous-read** in dev (so the
  runner can pull VOD inputs and viewers can fetch HLS). In production,
  restrict to runner IPs or use signed download URLs at job-dispatch
  time.
- Presigned URLs default to `S3_PRESIGN_TTL_SECONDS=3600`. Shorter
  values are safer; longer values aid resumable uploads.
- Object keys include a UUID prefix (`abr/<api_key_id>/<uuid>/...`,
  `live-out/<api_key_id>/<live_id>/...`) to prevent guessing and
  enable per-user GC.

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
