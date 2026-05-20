-- 0006_live_gateway_ingest: gateway-side RTMP ingest plumbing.
--
-- New live topology: the gateway accepts RTMP from customers on port
-- 1935, relays to a selected orchestrator's private RTMP endpoint, and
-- the orchestrator writes HLS output directly to gateway-owned S3 using
-- short-lived credentials.
--
-- See docs/exec-plans/active/0003-gateway-rtmp-ingest.md and the
-- live-session-gateway-ingest@v0 protocol mode (network-modules change
-- request).
--
-- ingest_mode discriminates how the row was opened:
--   broker_ingest  — legacy live-session-remote-runner@v0 path (orch
--                    owns public RTMP, customer pushes to orch's broker)
--   gateway_ingest — new live-session-gateway-ingest@v0 path (customer
--                    pushes to OUR gateway:1935; orch ingests from us)
--
-- s3_output_prefix is the bucket key prefix the orchestrator writes
-- HLS into (manifest + segments). Powers cleanup on DELETE /v1/live
-- and the customer-facing playback URL.
--
-- private_ingest_url is the upstream RTMP endpoint the gateway pushes
-- to. Returned by the broker on session-open in gateway_ingest mode.
--
-- stream_key_hint is the last 4 chars of the customer's plaintext
-- stream key. Logged for debugging without leaking the full credential.
-- The peppered full-key hash already lives in stream_key_hash.

ALTER TABLE live_streams
    ADD COLUMN ingest_mode       TEXT NOT NULL DEFAULT 'broker_ingest',
    ADD COLUMN s3_output_prefix  TEXT,
    ADD COLUMN private_ingest_url TEXT,
    ADD COLUMN stream_key_hint   TEXT;

ALTER TABLE live_streams
    ADD CONSTRAINT live_streams_ingest_mode_check
        CHECK (ingest_mode IN ('broker_ingest', 'gateway_ingest'));

-- The RTMP server looks up an incoming publish by the peppered hash of
-- the stream key the customer presented. Index for fast per-publish
-- lookup, partial so only active sessions are considered.
CREATE INDEX idx_live_streams_stream_key_hash_active
    ON live_streams (stream_key_hash)
    WHERE stream_key_hash IS NOT NULL
      AND status IN ('provisioning', 'live');
