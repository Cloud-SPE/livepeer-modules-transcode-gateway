DROP INDEX IF EXISTS idx_usage_reservations_runner_status;
ALTER TABLE usage_reservations
    DROP COLUMN IF EXISTS runner_state_json,
    DROP COLUMN IF EXISTS runner_completed_at,
    DROP COLUMN IF EXISTS runner_error_text,
    DROP COLUMN IF EXISTS runner_error_code,
    DROP COLUMN IF EXISTS runner_progress,
    DROP COLUMN IF EXISTS runner_phase,
    DROP COLUMN IF EXISTS runner_status,
    DROP COLUMN IF EXISTS webhook_secret;
