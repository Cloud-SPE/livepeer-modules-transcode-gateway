DROP INDEX IF EXISTS idx_live_streams_active_reconcile;

ALTER TABLE live_streams
    ADD COLUMN ingest_mode TEXT NOT NULL DEFAULT 'gateway_ingest';

ALTER TABLE live_streams
    ADD CONSTRAINT live_streams_ingest_mode_check
        CHECK (ingest_mode IN ('broker_ingest', 'gateway_ingest'));

CREATE INDEX idx_live_streams_active_reconcile
    ON live_streams (status, last_broker_sync_at)
    WHERE status IN ('provisioning', 'live');
