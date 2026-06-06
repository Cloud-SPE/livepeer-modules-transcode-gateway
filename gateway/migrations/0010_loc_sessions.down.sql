ALTER TABLE live_streams
  DROP COLUMN IF EXISTS loc_closed_at,
  DROP COLUMN IF EXISTS loc_refill_count,
  DROP COLUMN IF EXISTS loc_work_id,
  DROP COLUMN IF EXISTS loc_session_id;
