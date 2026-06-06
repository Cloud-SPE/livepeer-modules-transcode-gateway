-- LOC (Livepeer Open Clearinghouse) job tracking. The gateway now mints
-- payments via LOC's POST /v1/jobs instead of the payer daemon; these
-- columns link a reservation to its LOC job and track settle state so
-- the settle janitor can release encumbered credit after crashes.
--
-- The existing work_id (UUID) stays the customer-facing job id — LOC's
-- work_id is a hex string and lives in loc_work_id.
ALTER TABLE usage_reservations
  ADD COLUMN loc_job_id         uuid,
  ADD COLUMN loc_work_id        text,
  ADD COLUMN funded_value_wei   numeric(78,0),
  ADD COLUMN expected_value_wei numeric(78,0),
  ADD COLUMN billed_value_wei   numeric(78,0),
  ADD COLUMN settled_at         timestamptz,
  ADD COLUMN settle_state       text NOT NULL DEFAULT 'none'
    CHECK (settle_state IN ('none','pending','settled','refunded','failed'));

CREATE INDEX idx_usage_reservations_loc_job
  ON usage_reservations (loc_job_id);

-- Partial index drives the settle janitor's "what still needs settling"
-- scan; only pending rows are ever fetched.
CREATE INDEX idx_usage_reservations_settle_pending
  ON usage_reservations (settle_state, created_at)
  WHERE settle_state = 'pending';
