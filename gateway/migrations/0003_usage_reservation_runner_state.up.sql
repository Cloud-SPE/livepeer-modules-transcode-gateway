-- 0003_usage_reservation_runner_state: track the runner's job id.
--
-- The broker returns 202 with a runner-assigned job_id (e.g.
-- "abr-1779264668487-b86c95fe") at submit time. The gateway needs to
-- remember this id so subsequent GET /v1/abr/{work_id} calls can ask
-- the runner for live status (phase, overall_progress, error_code).
--
-- Forward-compatible add — existing rows get NULL and behave as
-- before (HEAD-probe master.m3u8 fallback).

ALTER TABLE usage_reservations
    ADD COLUMN runner_job_id TEXT;

CREATE INDEX idx_usage_reservations_runner_job_id
    ON usage_reservations (runner_job_id)
    WHERE runner_job_id IS NOT NULL;
