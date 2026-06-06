DROP INDEX IF EXISTS idx_usage_reservations_settle_pending;
DROP INDEX IF EXISTS idx_usage_reservations_loc_job;
ALTER TABLE usage_reservations
  DROP COLUMN IF EXISTS settle_state,
  DROP COLUMN IF EXISTS settled_at,
  DROP COLUMN IF EXISTS billed_value_wei,
  DROP COLUMN IF EXISTS expected_value_wei,
  DROP COLUMN IF EXISTS funded_value_wei,
  DROP COLUMN IF EXISTS loc_work_id,
  DROP COLUMN IF EXISTS loc_job_id;
