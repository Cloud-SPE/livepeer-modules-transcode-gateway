-- 0005_live_session_remote_runner: persist the live-session-remote-runner@v0
-- session identifiers + reconciliation metadata.
--
-- The new live mode introduces three cross-system identifiers the
-- gateway must reconcile against:
--   - broker_session_id  (bsess_...) — addresses the broker's session
--     control endpoints (topup, get, end)
--   - runner_session_id  (rsess_...) — embedded in the playback URL
--   - broker_work_id     (uuid)      — broker-bound work_id, distinct
--     from the gateway's reservation work_id; tracked for audit
--
-- close_reason captures the broker's terminal-state label as-is (e.g.
-- "insufficient_balance", "runner_failed", "idle_timeout") so the
-- gateway doesn't invent a local synonym. last_broker_sync_at lets the
-- background reconciler skip recently-checked rows.
--
-- usage_reservations.live_stream_id links every reservation row to the
-- live session that funded it (initial open + every top-up envelope).
-- One reservation per envelope; one live session has many reservations.

ALTER TABLE live_streams
    ADD COLUMN broker_session_id   TEXT,
    ADD COLUMN runner_session_id   TEXT,
    ADD COLUMN broker_work_id      UUID,
    ADD COLUMN close_reason        TEXT,
    ADD COLUMN last_broker_sync_at TIMESTAMPTZ;

CREATE INDEX idx_live_streams_broker_session_id
    ON live_streams (broker_session_id)
    WHERE broker_session_id IS NOT NULL;

CREATE INDEX idx_live_streams_active_reconcile
    ON live_streams (status, last_broker_sync_at)
    WHERE status IN ('provisioning', 'live');

ALTER TABLE usage_reservations
    ADD COLUMN live_stream_id UUID
        REFERENCES live_streams(id) ON DELETE SET NULL;

CREATE INDEX idx_usage_reservations_live_stream_id
    ON usage_reservations (live_stream_id)
    WHERE live_stream_id IS NOT NULL;
