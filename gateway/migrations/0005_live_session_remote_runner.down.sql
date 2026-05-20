DROP INDEX IF EXISTS idx_usage_reservations_live_stream_id;
ALTER TABLE usage_reservations
    DROP COLUMN IF EXISTS live_stream_id;

DROP INDEX IF EXISTS idx_live_streams_active_reconcile;
DROP INDEX IF EXISTS idx_live_streams_broker_session_id;
ALTER TABLE live_streams
    DROP COLUMN IF EXISTS last_broker_sync_at,
    DROP COLUMN IF EXISTS close_reason,
    DROP COLUMN IF EXISTS broker_work_id,
    DROP COLUMN IF EXISTS runner_session_id,
    DROP COLUMN IF EXISTS broker_session_id;
