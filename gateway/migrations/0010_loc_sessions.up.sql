-- LOC session tracking for live streams (PR-3 of the LOC migration).
-- Live sessions now mint via LOC POST /v1/sessions; refills are pinned
-- to the session and the close settles against a duration estimate.
ALTER TABLE live_streams
  ADD COLUMN loc_session_id   uuid,
  ADD COLUMN loc_work_id      text,
  ADD COLUMN loc_refill_count int NOT NULL DEFAULT 0,
  -- Claimed exactly once (WHERE loc_closed_at IS NULL) so the DELETE
  -- handler and the reconciler don't race a double CloseSession.
  ADD COLUMN loc_closed_at    timestamptz;
