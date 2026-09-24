-- Immutable paid request and encrypted recovery material. Legacy paid work must
-- be drained before starting the v2 binary; old rows remain readable for audit.
CREATE TABLE paid_operations (
  id uuid PRIMARY KEY,
  api_key_id uuid NOT NULL REFERENCES api_keys(id),
  reservation_id uuid NOT NULL REFERENCES usage_reservations(id),
  live_stream_id uuid REFERENCES live_streams(id),
  kind text NOT NULL CHECK (kind IN ('abr','live')),
  request_key text NOT NULL,
  request_hash text NOT NULL,
  state text NOT NULL DEFAULT 'queued',
  secrets bytea NOT NULL,
  public_json jsonb NOT NULL DEFAULT '{}',
  last_error text,
  attempts integer NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  stop_requested boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  UNIQUE(api_key_id,kind,request_key)
);
CREATE INDEX paid_operations_pending ON paid_operations(next_attempt_at) WHERE finished_at IS NULL;
