-- 0002_capability_refresh_meta: track refresh runs independent of result.
--
-- The capabilities table reflects what the on-chain registry advertises
-- right now. When the filter matches zero capabilities, the table is
-- correctly empty — but operators still need to see that the refresh
-- DID run, when it last ran, and what filter it applied.

CREATE TABLE capability_refresh_meta (
    id                  INT  PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    last_refresh_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_outcome        TEXT NOT NULL DEFAULT 'ok'
        CHECK (last_outcome IN ('ok', 'error')),
    last_error          TEXT,
    rows_matched        INT  NOT NULL DEFAULT 0,
    capability_filter   TEXT[] NOT NULL DEFAULT '{}'::TEXT[]
);

INSERT INTO capability_refresh_meta (id, last_refresh_at, last_outcome)
VALUES (1, to_timestamp(0), 'ok')
ON CONFLICT (id) DO NOTHING;
