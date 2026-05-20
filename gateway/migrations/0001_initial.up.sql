-- 0001_initial: full v1 schema.
--
-- No Stripe, no rate cards. The waitlist table IS the user identity table.
-- One unified schema; later migrations will add nullable columns only.

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ── waitlist (user identity) ──────────────────────────────────────────
CREATE TABLE waitlist (
    id                              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                            TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    email                           TEXT NOT NULL,
    ip_hash                         TEXT,

    email_verified_at               TIMESTAMPTZ,
    verification_token_hash         TEXT,
    verification_token_expires_at   TIMESTAMPTZ,

    status                          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'rejected')),
    approved_at                     TIMESTAMPTZ,
    approved_by                     TEXT,

    created_at                      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_waitlist_email             ON waitlist (email);
CREATE        INDEX idx_waitlist_status            ON waitlist (status);
CREATE UNIQUE INDEX idx_waitlist_verification_token
    ON waitlist (verification_token_hash)
    WHERE verification_token_hash IS NOT NULL;
CREATE        INDEX idx_waitlist_created_at        ON waitlist (created_at DESC);

-- ── api_keys (multiple per waitlist row) ──────────────────────────────
CREATE TABLE api_keys (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    waitlist_id   UUID NOT NULL REFERENCES waitlist(id) ON DELETE CASCADE,
    label         TEXT,
    key_prefix    TEXT NOT NULL,
    key_hash      TEXT NOT NULL,

    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at  TIMESTAMPTZ,
    revoked_at    TIMESTAMPTZ
);

CREATE INDEX idx_api_keys_hash     ON api_keys (key_hash);
CREATE INDEX idx_api_keys_waitlist ON api_keys (waitlist_id);

-- ── user_sessions (portal cookie sessions) ────────────────────────────
CREATE TABLE user_sessions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    api_key_id    UUID NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    session_hash  TEXT NOT NULL,

    expires_at    TIMESTAMPTZ NOT NULL,
    revoked_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_user_sessions_active_hash
    ON user_sessions (session_hash)
    WHERE revoked_at IS NULL;
CREATE INDEX idx_user_sessions_api_key ON user_sessions (api_key_id);

-- ── usage_reservations (per-request log) ──────────────────────────────
CREATE TABLE usage_reservations (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    api_key_id               UUID NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    work_id                  UUID NOT NULL,

    capability               TEXT NOT NULL,
    offering                 TEXT NOT NULL,
    broker_url               TEXT,
    eth_address              TEXT,

    state                    TEXT NOT NULL DEFAULT 'open'
        CHECK (state IN ('open', 'committed', 'refunded')),

    estimated_work_units     BIGINT,
    committed_work_units     BIGINT,
    price_per_work_unit_wei  NUMERIC(78, 0),

    latency_ms               INTEGER,
    status_code              INTEGER,
    error_text               TEXT,

    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at              TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_usage_reservations_work_id
    ON usage_reservations (work_id);
CREATE        INDEX idx_usage_reservations_api_key_created
    ON usage_reservations (api_key_id, created_at DESC);
CREATE        INDEX idx_usage_reservations_open_state
    ON usage_reservations (state)
    WHERE state = 'open';

-- ── live_streams (RTMP→HLS session lifecycle) ─────────────────────────
CREATE TABLE live_streams (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    api_key_id               UUID NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    reservation_id           UUID REFERENCES usage_reservations(id) ON DELETE SET NULL,

    name                     TEXT,
    status                   TEXT NOT NULL DEFAULT 'provisioning'
        CHECK (status IN ('provisioning', 'live', 'ended', 'failed')),

    capability               TEXT NOT NULL,
    offering                 TEXT NOT NULL,
    broker_url               TEXT,
    eth_address              TEXT,

    ingest_url               TEXT,
    stream_key_hash          TEXT,
    playback_url             TEXT,

    ladder_json              JSONB,
    error_text               TEXT,

    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at               TIMESTAMPTZ,
    last_heartbeat_at        TIMESTAMPTZ,
    ended_at                 TIMESTAMPTZ
);

CREATE INDEX idx_live_streams_api_key_created
    ON live_streams (api_key_id, created_at DESC);
CREATE INDEX idx_live_streams_active
    ON live_streams (status)
    WHERE status IN ('provisioning', 'live');

-- ── capabilities (pure cache of service-registry snapshot) ─────────────
CREATE TABLE capabilities (
    capability_id             TEXT PRIMARY KEY,
    capability                TEXT NOT NULL,
    offering                  TEXT NOT NULL,
    interaction_mode          TEXT,

    name                      TEXT,
    description               TEXT,
    provider                  TEXT,
    category                  TEXT,

    eth_address               TEXT,
    price_per_work_unit_wei   NUMERIC(78, 0),
    broker_url                TEXT,

    extra_json                JSONB,
    constraints_json          JSONB,

    active                    BOOLEAN NOT NULL DEFAULT true,
    snapshot_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_capabilities_capability
    ON capabilities (capability, offering)
    WHERE active = true;
CREATE INDEX idx_capabilities_active
    ON capabilities (active, snapshot_at);
