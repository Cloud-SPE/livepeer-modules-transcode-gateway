DROP INDEX IF EXISTS idx_usage_reservations_runner_job_id;
ALTER TABLE usage_reservations DROP COLUMN IF EXISTS runner_job_id;
