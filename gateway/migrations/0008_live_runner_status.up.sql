-- 0008_live_runner_status: cache the runner's richer status JSON so
-- the admin UI can render it without doing a broker round-trip per
-- session per page load.
--
-- The reconciler polls broker GET /v1/cap/{bsess} every
-- LIVE_RECONCILE_INTERVAL_SECS; on each tick it overwrites
-- runner_status_json with the broker's response payload (the
-- `ingest` + `output` blocks per the runner team's status-hardening
-- spec — see docs/exec-plans/active/runner-status-hardening.md).
--
-- JSONB rather than discrete columns because the runner-status surface
-- is still evolving; the admin UI parses opportunistically and shows
-- whichever fields are present. Once the wire stabilizes we can promote
-- specific fields to indexed columns if querying becomes a need.

ALTER TABLE live_streams
    ADD COLUMN runner_status_json JSONB;
