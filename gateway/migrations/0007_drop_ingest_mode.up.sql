-- 0007_drop_ingest_mode: collapse to a single live ingest mode.
--
-- The runner team (livepeer-modules-transcode-runners) consolidated on
-- gateway-ingest only ("Make live runner gateway-ingest only" commit
-- 2bda972). With only one live ingest mode, the live_streams.ingest_mode
-- column is redundant — it can only ever be 'gateway_ingest'.
--
-- We drop the column, the CHECK constraint, and the partial index that
-- filtered on ingest_mode. Other gateway_ingest-specific columns
-- (s3_output_prefix, private_ingest_url, stream_key_hint) stay — they
-- describe the session, not the mode.

DROP INDEX IF EXISTS idx_live_streams_active_reconcile;

ALTER TABLE live_streams
    DROP CONSTRAINT IF EXISTS live_streams_ingest_mode_check,
    DROP COLUMN IF EXISTS ingest_mode;

-- Re-create the active-reconcile index without the (now-removed) mode
-- filter. Still bounded to active sessions so the background reconciler
-- only scans rows it can act on.
CREATE INDEX idx_live_streams_active_reconcile
    ON live_streams (status, last_broker_sync_at)
    WHERE status IN ('provisioning', 'live');
