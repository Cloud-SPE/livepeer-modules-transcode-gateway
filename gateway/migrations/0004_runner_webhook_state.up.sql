-- 0004_runner_webhook_state: persist what the runner tells us via webhook.
--
-- The abr-runner emits webhooks (job.probed → job.encoding → ... →
-- job.complete | job.error) when given a webhook_url + webhook_secret.
-- We store the latest payload in runner_state_json (loose schema, the
-- runner can evolve fields without DB changes) and the secret so the
-- receiver can verify incoming signatures.

ALTER TABLE usage_reservations
    ADD COLUMN webhook_secret      TEXT,
    ADD COLUMN runner_status       TEXT,
    ADD COLUMN runner_phase        TEXT,
    ADD COLUMN runner_progress     DOUBLE PRECISION,
    ADD COLUMN runner_error_code   TEXT,
    ADD COLUMN runner_error_text   TEXT,
    ADD COLUMN runner_state_json   JSONB,
    ADD COLUMN runner_completed_at TIMESTAMPTZ;

CREATE INDEX idx_usage_reservations_runner_status
    ON usage_reservations (runner_status)
    WHERE runner_status IS NOT NULL;
