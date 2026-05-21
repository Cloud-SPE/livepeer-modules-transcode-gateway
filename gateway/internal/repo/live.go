package repo

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type LiveRepo struct {
	pool *pgxpool.Pool
}

func NewLiveRepo(pool *pgxpool.Pool) *LiveRepo {
	return &LiveRepo{pool: pool}
}

// All SELECT lists must agree with the column set scanLive consumes —
// keep them in sync when adding fields.
const liveSelectCols = `id, api_key_id, reservation_id, name, status, capability, offering,
	broker_url, eth_address, ingest_url, stream_key_hash, playback_url,
	ladder_json, error_text,
	broker_session_id, runner_session_id, broker_work_id, close_reason, last_broker_sync_at,
	s3_output_prefix, private_ingest_url, stream_key_hint,
	runner_status_json,
	created_at, started_at, last_heartbeat_at, ended_at`

type InsertLiveInput struct {
	APIKeyID      uuid.UUID
	ReservationID *uuid.UUID
	Name          string
	Capability    string
	Offering      string
	LadderJSON    []byte
}

func (r *LiveRepo) Insert(ctx context.Context, in InsertLiveInput) (*LiveStream, error) {
	const q = `INSERT INTO live_streams (api_key_id, reservation_id, name, capability, offering, ladder_json, status)
	           VALUES ($1, $2, NULLIF($3,''), $4, $5, $6, 'provisioning')
	           RETURNING ` + liveSelectCols
	row := r.pool.QueryRow(ctx, q,
		in.APIKeyID, in.ReservationID, in.Name, in.Capability, in.Offering, in.LadderJSON)
	return scanLive(row)
}

// ActivateLiveGatewayInput is the gateway-ingest variant: no public
// RTMP URL or stream key (those are gateway-owned); instead carries the
// upstream private RTMP endpoint and the S3 prefix the runner writes to.
type ActivateLiveGatewayInput struct {
	BrokerURL        string
	EthAddress       string
	StreamKeyHash    string // peppered hash of customer's plaintext key
	StreamKeyHint    string // last 4 chars; for logs only
	S3OutputPrefix   string
	PrivateIngestURL string
	PlaybackURL      string // public S3 URL the customer's HLS player hits
	BrokerSessionID  string
	RunnerSessionID  string
	BrokerWorkID     *uuid.UUID
}

func (r *LiveRepo) ActivateGatewayIngest(ctx context.Context, id uuid.UUID, in ActivateLiveGatewayInput) error {
	const q = `UPDATE live_streams
	           SET status='live',
	               broker_url=$2, eth_address=$3,
	               stream_key_hash=$4, stream_key_hint=$5,
	               s3_output_prefix=$6, private_ingest_url=$7, playback_url=$8,
	               broker_session_id=$9, runner_session_id=$10, broker_work_id=$11,
	               started_at=now(), last_heartbeat_at=now()
	           WHERE id=$1 AND status='provisioning'`
	tag, err := r.pool.Exec(ctx, q, id,
		in.BrokerURL, in.EthAddress,
		in.StreamKeyHash, nullable(in.StreamKeyHint),
		in.S3OutputPrefix, in.PrivateIngestURL, in.PlaybackURL,
		nullable(in.BrokerSessionID), nullable(in.RunnerSessionID), in.BrokerWorkID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("live_streams: row not provisioning (gateway_ingest)")
	}
	return nil
}

// FindActiveByStreamKeyHash powers the RTMP server's per-publish auth
// lookup: customer sends stream key, gateway peppered-hashes it, looks
// up the matching row. Returns nil when no active session matches.
//
// Hash is the output of crypto.HashWithPepper(plaintext, pepper).
// Status filter is provisioning OR live — both indicate the gateway
// should accept the publish.
func (r *LiveRepo) FindActiveByStreamKeyHash(ctx context.Context, hash string) (*LiveStream, error) {
	const q = `SELECT ` + liveSelectCols + `
	           FROM live_streams
	           WHERE stream_key_hash = $1
	             AND status IN ('provisioning', 'live')
	           ORDER BY created_at DESC LIMIT 1`
	row := r.pool.QueryRow(ctx, q, hash)
	s, err := scanLive(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}


func (r *LiveRepo) Fail(ctx context.Context, id uuid.UUID, errText string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE live_streams SET status='failed', error_text=$2, ended_at=now() WHERE id=$1 AND status IN ('provisioning','live')`,
		id, errText)
	return err
}

// EndWithReason marks the session ended and persists the broker's
// terminal close_reason as-is. Used by both customer-initiated DELETE
// and the background reconciler when it observes broker.state=ended.
func (r *LiveRepo) EndWithReason(ctx context.Context, id uuid.UUID, status LiveStreamStatus, closeReason string) error {
	if status == "" {
		status = LiveEnded
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE live_streams SET status=$2, close_reason=NULLIF($3,''), ended_at=now()
		 WHERE id=$1 AND status IN ('provisioning','live')`,
		id, string(status), closeReason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("live_streams: already terminal")
	}
	return nil
}

// End preserves the pre-0005 customer-DELETE path (no close_reason).
// Kept so callers that haven't been migrated still compile; new
// callers should use EndWithReason("gateway_close") instead.
func (r *LiveRepo) End(ctx context.Context, id uuid.UUID) error {
	return r.EndWithReason(ctx, id, LiveEnded, "gateway_close")
}

func (r *LiveRepo) Heartbeat(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE live_streams SET last_heartbeat_at=$2 WHERE id=$1 AND status='live'`,
		id, at)
	return err
}

// ReconcileSnapshot is what the background reconciler/top-up engine
// writes after polling broker GET /v1/cap/{bsess}. Allows the caller
// to update state + playback URL + heartbeat in one round-trip.
type ReconcileSnapshot struct {
	Status      LiveStreamStatus
	PlaybackURL string
	CloseReason string
	Heartbeat   *time.Time // when broker says session was last seen
	EndedAt     *time.Time // when broker says session ended (terminal states only)
	// RunnerStatusJSON is the broker's runner-status surface as raw
	// bytes (ingest + output blocks per the status-hardening spec).
	// Nil when the broker didn't include them; the column accepts NULL
	// and the admin UI handles missing fields gracefully.
	RunnerStatusJSON []byte
}

func (r *LiveRepo) RecordBrokerSync(ctx context.Context, id uuid.UUID, snap ReconcileSnapshot) error {
	const q = `UPDATE live_streams
	           SET status              = COALESCE(NULLIF($2, ''), status),
	               playback_url        = COALESCE(NULLIF($3, ''), playback_url),
	               close_reason        = COALESCE(NULLIF($4, ''), close_reason),
	               last_heartbeat_at   = COALESCE($5, last_heartbeat_at),
	               ended_at            = COALESCE($6, ended_at),
	               runner_status_json  = COALESCE($7, runner_status_json),
	               last_broker_sync_at = now()
	           WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id,
		string(snap.Status), snap.PlaybackURL, snap.CloseReason, snap.Heartbeat, snap.EndedAt,
		snap.RunnerStatusJSON)
	return err
}

func (r *LiveRepo) GetByID(ctx context.Context, id, apiKeyID uuid.UUID) (*LiveStream, error) {
	const q = `SELECT ` + liveSelectCols + ` FROM live_streams WHERE id=$1 AND api_key_id=$2`
	row := r.pool.QueryRow(ctx, q, id, apiKeyID)
	s, err := scanLive(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

// ListAll powers the admin "all live sessions" view. Returns sessions
// across all api_keys, optionally filtered to active-only.
func (r *LiveRepo) ListAll(ctx context.Context, activeOnly bool, limit int) ([]LiveStream, error) {
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT ` + liveSelectCols + ` FROM live_streams`
	if activeOnly {
		q += " WHERE status IN ('provisioning', 'live')"
	}
	q += " ORDER BY created_at DESC LIMIT $1"
	rows, err := r.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LiveStream
	for rows.Next() {
		s, err := scanLive(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// ListActiveForReconcile is the scan the background reconciler uses.
// Returns active sessions that have a broker_session_id set — rows
// without one are ignored (failed-to-open).
func (r *LiveRepo) ListActiveForReconcile(ctx context.Context, limit int) ([]LiveStream, error) {
	if limit <= 0 {
		limit = 500
	}
	const q = `SELECT ` + liveSelectCols + `
	           FROM live_streams
	           WHERE status IN ('provisioning', 'live')
	             AND broker_session_id IS NOT NULL
	           ORDER BY COALESCE(last_broker_sync_at, '1970-01-01'::timestamptz) ASC
	           LIMIT $1`
	rows, err := r.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LiveStream
	for rows.Next() {
		s, err := scanLive(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *LiveRepo) ListByAPIKey(ctx context.Context, apiKeyID uuid.UUID, limit int) ([]LiveStream, error) {
	const q = `SELECT ` + liveSelectCols + `
	           FROM live_streams WHERE api_key_id=$1 ORDER BY created_at DESC LIMIT $2`
	rows, err := r.pool.Query(ctx, q, apiKeyID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LiveStream
	for rows.Next() {
		s, err := scanLive(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *LiveRepo) CountActive(ctx context.Context) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM live_streams WHERE status='live'`).Scan(&n)
	return n, err
}

func scanLive(s pgx.Row) (*LiveStream, error) {
	var l LiveStream
	var status string
	err := s.Scan(&l.ID, &l.APIKeyID, &l.ReservationID, &l.Name, &status, &l.Capability, &l.Offering,
		&l.BrokerURL, &l.EthAddress, &l.IngestURL, &l.StreamKeyHash, &l.PlaybackURL,
		&l.LadderJSON, &l.ErrorText,
		&l.BrokerSessionID, &l.RunnerSessionID, &l.BrokerWorkID, &l.CloseReason, &l.LastBrokerSyncAt,
		&l.S3OutputPrefix, &l.PrivateIngestURL, &l.StreamKeyHint,
		&l.RunnerStatusJSON,
		&l.CreatedAt, &l.StartedAt, &l.LastHeartbeatAt, &l.EndedAt)
	if err != nil {
		return nil, err
	}
	l.Status = LiveStreamStatus(status)
	return &l, nil
}

