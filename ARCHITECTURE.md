# ARCHITECTURE

Top-level map of the repository. Follows the
[ARCHITECTURE.md convention](https://matklad.github.io/2021/02/06/ARCHITECTURE.md.html):
this file is for *bird's-eye orientation*. Deeper detail lives in
[`docs/design-docs/`](./docs/design-docs/) and in each file's docstring.

For "what does this thing do?" see [`DESIGN.md`](./DESIGN.md).
For invariants, see
[`docs/design-docs/core-beliefs.md`](./docs/design-docs/core-beliefs.md).

---

## 1. System overview

```mermaid
flowchart LR
  user[Developer<br/>HTTP client] -->|/v1/*<br/>Bearer sk-…| GW
  visitor[Web visitor] -->|HTTP| SITE
  portalUser[Approved user] -->|HTTP + cookie| PORTAL
  admin[Operator] -->|HTTP + X-Admin-Token| ADMIN

  SITE[web/site<br/>Lit zero-build] -->|/api/*| GW
  PORTAL[web/portal<br/>Lit zero-build] -->|/api/*, /portal/*, /v1/*| GW
  ADMIN[web/admin<br/>Lit zero-build] -->|/api/*, /admin/*| GW

  GW[gateway<br/>Go / huma + chi] -->|SQL| DB[(Postgres)]
  GW -->|S3| RFS[RustFS<br/>S3-compatible]
  GW -->|gRPC UDS| REG[service-registry-daemon]
  GW -->|gRPC UDS| PAYER[payment-daemon]
  GW -->|Livepeer-* headers<br/>+ Livepeer-Payment| BROKER[capability-broker<br/>on orchestrator host]
  BROKER --> RUN_ABR[abr-runner<br/>VOD ladder]
  BROKER --> RUN_LIVE[rtmp-ingress-hls-egress<br/>live mode]
  GW -->|optional| RESEND[Resend<br/>email]

  REG -.->|reads| CHAIN[(EVM chain<br/>AI service registry)]
  PAYER -.->|reads| CHAIN

  classDef ours fill:#1f3a2a,stroke:#4cd97b,color:#e8eaed;
  classDef ext fill:#1a1c20,stroke:#9aa0a6,color:#9aa0a6,stroke-dasharray: 4 2;
  class GW,SITE,PORTAL,ADMIN,DB,RFS ours;
  class REG,PAYER,BROKER,RUN_ABR,RUN_LIVE,RESEND,CHAIN ours;
```

Green = in this repo or a local compose service. Dashed gray = external
runtime peers (run as their own containers / on other hosts).

---

## 2. Components

| Component | Path | Purpose | Owns |
|---|---|---|---|
| **Gateway** | `gateway/` | Translates transcode requests → Livepeer wire. Hosts the SaaS shell (waitlist, sessions, API keys, admin). Presigns RustFS PUTs for VOD ingest. | The only stateful Go service in this repo. |
| **Marketing site** | `web/site/` | Public landing + waitlist signup + email-verification page. | Generic copy; rebrand at deploy time. |
| **Portal** | `web/portal/` | Authenticated user dashboard: account, API keys, usage, playground (Live + Transcode tabs). | Cookie-session UX. |
| **Admin** | `web/admin/` | Operator console: waitlist queue, users, usage, capability registry debug. | `X-Admin-Token` UX (stored in localStorage). |
| **Protos** | `proto/` | Vendored gRPC definitions for `payment-daemon` + `service-registry-daemon`. | Codegen target: `gateway/gen/proto/`. |

External services pulled at runtime:

| Service | Image | Local profile |
|---|---|---|
| `service-registry-daemon` | `tztcloud/livepeer-service-registry-daemon:v1.3.0` | `livepeer` |
| `payment-daemon` | `tztcloud/livepeer-payment-daemon:v1.3.0` | `livepeer` |
| `rustfs` | `rustfs/rustfs:latest` | default |
| `rustfs-bootstrap` (one-shot) | `minio/mc:latest` | default |
| `capability-broker` + runners | (operator side) | not in compose |

---

## 3. Gateway internal layering

```
            ┌────────────────────────────────────────────┐
            │ cmd/gateway/main.go  (process wiring)      │
            ├────────────────────────────────────────────┤
            │ internal/handlers/{waitlist,portal,admin,  │  ← HTTP surface
            │   v1}/  proxy/                             │
            ├────────────────────────────────────────────┤
            │ internal/proxy/service/  proxy/livepeer/   │  ← service / wire
            │ internal/email/  internal/s3/              │
            ├────────────────────────────────────────────┤
            │ internal/repo/  internal/registry/         │  ← data / RPC
            │ internal/schema/                           │
            ├────────────────────────────────────────────┤
            │ internal/config/  internal/db/             │  ← primitives
            │ internal/crypto/  internal/metrics/        │
            ├────────────────────────────────────────────┤
            │ gen/proto/  gen/db/                        │  ← generated
            └────────────────────────────────────────────┘
```

Edges go *down* only. Cross-cutting deps (config, DB pool, S3 client,
email, route selector, rate limiter, payment client) are bundled into
a `ServerDeps` struct in `main.go` and threaded into every handler.

### Source-of-truth split

| Subtree | Origin | Notes |
|---|---|---|
| `internal/proxy/livepeer/` | Ported from `livepeer-modules-openai/gateway/src/proxy/livepeer/` (TS→Go) | Load-bearing wire mechanics — payment minting, headers, http-reqresp dispatch, rtmp session lifecycle. |
| `internal/proxy/service/` | Same | Route selection, route health, dispatch loop. |
| `internal/proxy/{abr,live,capabilities}.go` | Built here | Transcode-specific handlers. |
| Everything else (`internal/handlers/`, `internal/repo/`, `internal/schema/`, `internal/crypto/`, `internal/email/`, `internal/metrics/`, `internal/db/`, `internal/config/`, `cmd/gateway/`) | Built here | Native Go, written for this repository. |

---

## 4. Data storage

```mermaid
erDiagram
  WAITLIST ||--o{ API_KEYS : "owns"
  API_KEYS ||--o{ USER_SESSIONS : "issues"
  API_KEYS ||--o{ USAGE_RESERVATIONS : "logs"
  API_KEYS ||--o{ LIVE_STREAMS : "owns"
  USAGE_RESERVATIONS ||--o| LIVE_STREAMS : "1:1 (live)"

  WAITLIST {
    uuid id PK
    text email UK
    text name
    text ip_hash
    timestamptz email_verified_at
    text verification_token_hash UK "nullable"
    timestamptz verification_token_expires_at
    text status "pending|approved|rejected"
    timestamptz approved_at
    text approved_by
    timestamptz created_at
  }

  API_KEYS {
    uuid id PK
    uuid waitlist_id FK
    text label
    text key_prefix "sk-XXXXNNNN"
    text key_hash "SHA-256+pepper"
    timestamptz created_at
    timestamptz last_used_at
    timestamptz revoked_at
  }

  USER_SESSIONS {
    uuid id PK
    uuid api_key_id FK
    text session_hash
    timestamptz expires_at
    timestamptz revoked_at
    timestamptz created_at
  }

  USAGE_RESERVATIONS {
    uuid id PK
    uuid api_key_id FK
    uuid work_id UK
    text capability
    text offering
    text broker_url
    text eth_address
    text state "open|committed|refunded"
    bigint estimated_work_units
    bigint committed_work_units
    numeric price_per_work_unit_wei
    integer latency_ms
    integer status_code
    text error_text
    timestamptz created_at
    timestamptz resolved_at
  }

  LIVE_STREAMS {
    uuid id PK
    uuid api_key_id FK
    uuid reservation_id FK
    text status "provisioning|live|ended|failed"
    text capability
    text offering
    text broker_url
    text ingest_url
    text stream_key_hash
    text playback_url
    timestamptz created_at
    timestamptz last_heartbeat_at
    timestamptz ended_at
  }

  CAPABILITIES {
    text capability_id PK
    text offering_id
    text interaction_mode
    text name
    text description
    text provider
    text category
    text eth_address
    numeric price_per_work_unit_wei
    text broker_url
    jsonb extra_json
    jsonb constraints_json
    boolean active
    timestamptz snapshot_at
  }
```

**One Postgres database. One migration track.** `gateway/migrations/`
holds numbered `.sql` files applied by `golang-migrate` at boot.

### Why a `live_streams` table

VOD ABR maps cleanly to a per-request `usage_reservations` row. Live
RTMP sessions are long-lived (minutes-to-hours), need their own
client-facing ID, ingest URL, stream key, playback URL, and lifecycle
status. Splitting that into `live_streams` keeps `usage_reservations`
as the per-attempt billing log and gives live streams their own
identity surface.

### Why a `capabilities` cache table

`/v1/capabilities` must be cheap. Querying the gRPC resolver on every
call would couple catalog reads to chain availability. The background
refresh task (every `REGISTRY_REFRESH_INTERVAL_MS`, default 60s) writes
the latest snapshot into `capabilities`; HTTP reads from there.

---

## 5. Process flows

### 5.1 Signup → verify → approve → key

Identical to `livepeer-modules-openai`. See its
[ARCHITECTURE.md §5.1](../livepeer-modules-openai/ARCHITECTURE.md#51-signup--verify--approve--key)
for the sequence diagram — this repo's flow is byte-for-byte the same.

### 5.2 `/v1/abr` request lifecycle

```mermaid
sequenceDiagram
  participant C as Client
  participant GW as gateway
  participant DB as postgres
  participant RFS as rustfs
  participant PAY as payment-daemon
  participant REG as service-registry-daemon
  participant BRK as capability-broker
  participant RUN as abr-runner

  opt VOD upload first
    C->>GW: POST /v1/abr/upload-url
    GW->>RFS: PresignPut(key)
    GW-->>C: {upload_url, object_url}
    C->>RFS: PUT bytes
  end

  C->>GW: POST /v1/abr {input_url}<br/>Authorization: Bearer sk-…
  GW->>DB: SELECT api_keys WHERE key_hash=…
  GW->>DB: INSERT usage_reservations (state='open', work_id)
  GW->>REG: gRPC: select candidates (livepeer:transcode/abr-ladder)
  REG-->>GW: ranked candidates
  GW->>PAY: gRPC: CreatePayment(face_value, recipient, capability, offering)
  PAY-->>GW: payment_bytes
  GW->>BRK: POST broker /v1/cap (http-reqresp)<br/>Livepeer-Capability, Livepeer-Payment, …
  BRK->>RUN: dispatch
  RUN-->>BRK: {job_id, master_playlist_url}
  BRK-->>GW: response

  alt success
    GW->>DB: UPDATE usage_reservations<br/>state='committed', committed_work_units=…
    GW-->>C: {job_id, status_url, master_playlist_url}
  else upstream failure
    Note over GW: failover loop:<br/>retry next candidate
    GW->>DB: UPDATE usage_reservations<br/>state='refunded', error_text=…
    GW-->>C: error (502/500)
  end
```

### 5.3 `/v1/live` session lifecycle

```mermaid
sequenceDiagram
  participant C as Client
  participant GW as gateway
  participant DB as postgres
  participant PAY as payment-daemon
  participant REG as service-registry-daemon
  participant BRK as capability-broker
  participant RUN as rtmp-runner

  C->>GW: POST /v1/live
  GW->>DB: INSERT usage_reservations (state='open', long-lived)
  GW->>DB: INSERT live_streams (status='provisioning')
  GW->>REG: gRPC: select (livepeer:transcode/live-rtmp-hls-abr)
  GW->>PAY: gRPC: CreatePayment (session-open face value)
  GW->>BRK: OpenSession (rtmp-ingress-hls-egress mode)
  BRK-->>GW: {rtmp_url, stream_key, hls_url}
  GW->>DB: UPDATE live_streams status='live', urls
  GW-->>C: {id, ingest, playback}

  C->>BRK: RTMP push
  BRK->>RUN: ingest + transcode ladder
  RUN-->>BRK: LL-HLS segments
  loop interim debit
    BRK->>PAY: Debit(session_id, units)
  end

  C->>GW: GET /v1/live/:id
  GW-->>C: {status, playback_url, started_at}

  C->>GW: DELETE /v1/live/:id
  GW->>BRK: CloseSession
  GW->>PAY: settle session
  GW->>DB: UPDATE live_streams status='ended'
  GW->>DB: UPDATE usage_reservations state='committed'
  GW-->>C: 204
```

### 5.4 Registry refresh

Identical to openai gateway, retargeted at the transcode capability set.
Writes to `capabilities` table instead of `models`.

### 5.5 Portal cookie auth

Identical to openai gateway.

---

## 6. External dependencies

| What | How it talks to us |
|---|---|
| HTTP clients | HTTPS → `/v1/*` (Bearer auth) |
| Portal / admin / site users | HTTPS → static SPAs + JSON APIs |
| `service-registry-daemon` | gRPC over UDS (`/var/run/livepeer/service-registry.sock`) |
| `payment-daemon` | gRPC over UDS (`/var/run/livepeer/payer-daemon.sock`) |
| `capability-broker` (on orch host) | HTTPS, per Livepeer wire spec |
| RustFS | S3 API over HTTP (compose network) |
| Postgres | TCP, single DB for all SaaS + live-stream data |
| Resend | HTTPS, email delivery (optional in dev) |
| EVM chain (Arbitrum One by default) | Indirectly — only via the two daemons |

---

## 7. Boundaries that matter

- **The proxy doesn't know about humans.** `/v1/*` authenticates via
  API key and joins to `usage_reservations.api_key_id`. Names + emails
  live in `waitlist`. The only join between the two namespaces is
  `api_keys.waitlist_id`.
- **The wire spec is product-agnostic.** `proxy/livepeer/` only knows
  `Livepeer-Capability` headers + interaction modes. Mapping
  transcode-product → capability happens in
  `proxy/{abr,live,capabilities}.go`.
- **The SaaS shell is product-agnostic.** The same shell powers
  `livepeer-modules-openai`. Transcode specifics live in
  `internal/proxy/{abr,live,capabilities}.go` and the `live_streams`
  table.
- **Media bytes never traverse Go.** VOD bytes go client → RustFS →
  runner. Live bytes go client → broker → runner. The gateway only
  signs URLs and reads catalog state.
- **Runners don't import from the gateway and vice versa.** The only
  contract is the Livepeer wire spec, mediated by the broker.

---

## 8. Observability

- **Prometheus** `/metrics` on the gateway, optionally Bearer-gated
  via `METRICS_TOKEN`. Surfaces:
  - Default Go runtime metrics under prefix `video_gateway_*`
  - HTTP: `video_gateway_http_requests_total{method,route,status}`,
    `video_gateway_http_request_duration_seconds`
  - Proxy: `video_gateway_proxy_reservations_total{capability,outcome}`,
    `video_gateway_live_streams_active`
  - Waitlist: `video_gateway_waitlist_signups_total`
  - Route health: `livepeer_gateway_route_health_*`
- **Structured JSON logs** to stdout via `log/slog`. Request IDs
  propagated as `Livepeer-Request-Id` on `/v1/*`.
- **`usage_reservations`** + **`live_streams`** are the durable
  per-request and per-session logs (queryable via `/admin/usage` and
  `/portal/usage`).

---

## 9. Deployment shape

```mermaid
flowchart TB
  subgraph host[Single host or k8s pod]
    GW[gateway]
    DB[(postgres)]
    RFS[(rustfs)]
    REG[service-registry-daemon]
    PAYER[payment-daemon]
    UDS[(livepeer-run<br/>volume<br/>UDS sockets)]
  end

  GW <-->|TCP| DB
  GW <-->|S3| RFS
  GW <-->|UDS| UDS
  REG <-->|UDS| UDS
  PAYER <-->|UDS| UDS

  CDN[CDN / static host]
  cdn_site[web/site] --> CDN
  cdn_portal[web/portal] --> CDN
  cdn_admin[web/admin] --> CDN

  proxy[Reverse proxy<br/>Traefik / nginx / Cloud LB] -->|host: api.*| GW
  proxy -->|host: example.com| CDN
  proxy -->|host: portal.*| CDN
  proxy -->|host: admin.*| CDN
  proxy -->|host: ingest.*| RFS
  proxy -->|host: metrics.*<br/>+ basic auth| GW
```

In dev, the same shape collapses: `make dev` runs gateway + db + rustfs
+ bootstrap; each SPA runs via its own `dev-server.js`.

---

## 10. Out of scope here

- The Livepeer wire spec itself — owned by `livepeer-network-protocol`
  in the source monorepo.
- The on-chain service registry contracts — operated separately.
- The `capability-broker` + `abr-runner` + `rtmp-ingress-hls-egress`
  binaries — owned by `livepeer-network-modules`.
- Gateway-side playback proxy — v2 concern; v1 returns broker HLS URLs.
- Multi-region deployment topology.
