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
	           RETURNING id, api_key_id, reservation_id, name, status, capability, offering,
	                     broker_url, eth_address, ingest_url, stream_key_hash, playback_url,
	                     ladder_json, error_text, created_at, started_at, last_heartbeat_at, ended_at`
	row := r.pool.QueryRow(ctx, q, in.APIKeyID, in.ReservationID, in.Name, in.Capability, in.Offering, in.LadderJSON)
	return scanLive(row)
}

type ActivateLiveInput struct {
	BrokerURL     string
	EthAddress    string
	IngestURL     string
	StreamKeyHash string
	PlaybackURL   string
}

func (r *LiveRepo) Activate(ctx context.Context, id uuid.UUID, in ActivateLiveInput) error {
	const q = `UPDATE live_streams
	           SET status='live', broker_url=$2, eth_address=$3, ingest_url=$4,
	               stream_key_hash=$5, playback_url=$6, started_at=now(), last_heartbeat_at=now()
	           WHERE id=$1 AND status='provisioning'`
	tag, err := r.pool.Exec(ctx, q, id, in.BrokerURL, in.EthAddress, in.IngestURL,
		in.StreamKeyHash, in.PlaybackURL)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("live_streams: row not provisioning")
	}
	return nil
}

func (r *LiveRepo) Fail(ctx context.Context, id uuid.UUID, errText string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE live_streams SET status='failed', error_text=$2, ended_at=now() WHERE id=$1 AND status IN ('provisioning','live')`,
		id, errText)
	return err
}

func (r *LiveRepo) End(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE live_streams SET status='ended', ended_at=now() WHERE id=$1 AND status IN ('provisioning','live')`,
		id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("live_streams: already ended")
	}
	return nil
}

func (r *LiveRepo) Heartbeat(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE live_streams SET last_heartbeat_at=$2 WHERE id=$1 AND status='live'`,
		id, at)
	return err
}

func (r *LiveRepo) GetByID(ctx context.Context, id, apiKeyID uuid.UUID) (*LiveStream, error) {
	const q = `SELECT id, api_key_id, reservation_id, name, status, capability, offering,
	                  broker_url, eth_address, ingest_url, stream_key_hash, playback_url,
	                  ladder_json, error_text, created_at, started_at, last_heartbeat_at, ended_at
	           FROM live_streams WHERE id=$1 AND api_key_id=$2`
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
	q := `SELECT id, api_key_id, reservation_id, name, status, capability, offering,
	             broker_url, eth_address, ingest_url, stream_key_hash, playback_url,
	             ladder_json, error_text, created_at, started_at, last_heartbeat_at, ended_at
	      FROM live_streams`
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

func (r *LiveRepo) ListByAPIKey(ctx context.Context, apiKeyID uuid.UUID, limit int) ([]LiveStream, error) {
	const q = `SELECT id, api_key_id, reservation_id, name, status, capability, offering,
	                  broker_url, eth_address, ingest_url, stream_key_hash, playback_url,
	                  ladder_json, error_text, created_at, started_at, last_heartbeat_at, ended_at
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
		&l.LadderJSON, &l.ErrorText, &l.CreatedAt, &l.StartedAt, &l.LastHeartbeatAt, &l.EndedAt)
	if err != nil {
		return nil, err
	}
	l.Status = LiveStreamStatus(status)
	return &l, nil
}
