DROP INDEX IF EXISTS idx_live_streams_stream_key_hash_active;
ALTER TABLE live_streams
    DROP CONSTRAINT IF EXISTS live_streams_ingest_mode_check;
ALTER TABLE live_streams
    DROP COLUMN IF EXISTS stream_key_hint,
    DROP COLUMN IF EXISTS private_ingest_url,
    DROP COLUMN IF EXISTS s3_output_prefix,
    DROP COLUMN IF EXISTS ingest_mode;
