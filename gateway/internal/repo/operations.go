package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PaidOperation struct {
	ID            uuid.UUID
	APIKeyID      uuid.UUID
	ReservationID uuid.UUID
	LiveStreamID  *uuid.UUID
	Kind          string
	RequestKey    string
	RequestHash   string
	State         string
	Secrets       []byte
	PublicJSON    json.RawMessage
	LastError     *string
	Attempts      int
	NextAttemptAt time.Time
	StopRequested bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
	FinishedAt    *time.Time
}

const operationColumns = `id,api_key_id,reservation_id,live_stream_id,kind,request_key,request_hash,state,secrets,public_json,last_error,attempts,next_attempt_at,stop_requested,created_at,updated_at,finished_at`

type OperationRepo struct{ pool *pgxpool.Pool }

func NewOperationRepo(pool *pgxpool.Pool) *OperationRepo { return &OperationRepo{pool: pool} }

// PublicViews excludes encrypted credentials and request material from listings.
func (r *OperationRepo) PublicViews(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*PaidOperation, error) {
	rows, err := r.pool.Query(ctx, `SELECT id,state,public_json FROM paid_operations WHERE id=ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[uuid.UUID]*PaidOperation)
	for rows.Next() {
		o := new(PaidOperation)
		if err := rows.Scan(&o.ID, &o.State, &o.PublicJSON); err != nil {
			return nil, err
		}
		out[o.ID] = o
	}
	return out, rows.Err()
}
func scanOperation(row pgx.Row) (*PaidOperation, error) {
	o := new(PaidOperation)
	err := row.Scan(&o.ID, &o.APIKeyID, &o.ReservationID, &o.LiveStreamID, &o.Kind, &o.RequestKey, &o.RequestHash, &o.State, &o.Secrets, &o.PublicJSON, &o.LastError, &o.Attempts, &o.NextAttemptAt, &o.StopRequested, &o.CreatedAt, &o.UpdatedAt, &o.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return o, err
}
func (r *OperationRepo) Get(ctx context.Context, id uuid.UUID) (*PaidOperation, error) {
	return scanOperation(r.pool.QueryRow(ctx, `SELECT `+operationColumns+` FROM paid_operations WHERE id=$1`, id))
}
func (r *OperationRepo) FindRequest(ctx context.Context, key uuid.UUID, kind, request string) (*PaidOperation, error) {
	return scanOperation(r.pool.QueryRow(ctx, `SELECT `+operationColumns+` FROM paid_operations WHERE api_key_id=$1 AND kind=$2 AND request_key=$3`, key, kind, request))
}
func (r *OperationRepo) GetLive(ctx context.Context, id uuid.UUID) (*PaidOperation, error) {
	return scanOperation(r.pool.QueryRow(ctx, `SELECT `+operationColumns+` FROM paid_operations WHERE live_stream_id=$1`, id))
}
func (r *OperationRepo) Create(ctx context.Context, o *PaidOperation) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO paid_operations(id,api_key_id,reservation_id,live_stream_id,kind,request_key,request_hash,state,secrets,public_json) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, o.ID, o.APIKeyID, o.ReservationID, o.LiveStreamID, o.Kind, o.RequestKey, o.RequestHash, o.State, o.Secrets, o.PublicJSON)
	return err
}

// CreateIntent atomically journals a customer operation and its product rows
// before any upstream side effect. A conflicting idempotency key rolls back
// every row; callers then return the pre-existing operation.
func (r *OperationRepo) CreateIntent(ctx context.Context, o *PaidOperation, capability, offering, name, streamHash, streamHint string, estimated int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO usage_reservations(id,api_key_id,work_id,capability,offering,estimated_work_units) VALUES($1,$2,$3,$4,$5,$6)`, o.ReservationID, o.APIKeyID, o.ID, capability, offering, estimated)
	if err != nil {
		return err
	}
	if o.LiveStreamID != nil {
		_, err = tx.Exec(ctx, `INSERT INTO live_streams(id,api_key_id,reservation_id,name,capability,offering,stream_key_hash,stream_key_hint) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, *o.LiveStreamID, o.APIKeyID, o.ReservationID, name, capability, offering, streamHash, streamHint)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO paid_operations(id,api_key_id,reservation_id,live_stream_id,kind,request_key,request_hash,state,secrets,public_json) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, o.ID, o.APIKeyID, o.ReservationID, o.LiveStreamID, o.Kind, o.RequestKey, o.RequestHash, o.State, o.Secrets, o.PublicJSON)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *OperationRepo) Pending(ctx context.Context, kind string, limit int) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `SELECT id FROM paid_operations WHERE finished_at IS NULL AND kind=$1 AND next_attempt_at<=now() ORDER BY stop_requested DESC,next_attempt_at LIMIT $2`, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
func (r *OperationRepo) RequestStop(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `UPDATE paid_operations SET stop_requested=true,next_attempt_at=now(),updated_at=now() WHERE id=$1 AND finished_at IS NULL`, id)
	return err
}

// Lock uses a session advisory lock, never an expiring lease. A slow SSE
// exchange cannot lose ownership to a second worker. PostgreSQL releases the
// lock on process/connection death; upstream request IDs fence any retry.
type OperationLock struct {
	conn *pgxpool.Conn
	id   uuid.UUID
}

func (r *OperationRepo) Lock(ctx context.Context, id uuid.UUID) (*OperationLock, error) {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var ok bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, id.String()).Scan(&ok); err != nil || !ok {
		conn.Release()
		return nil, err
	}
	return &OperationLock{conn: conn, id: id}, nil
}
func (l *OperationLock) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var ok bool
	if err := l.conn.QueryRow(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, l.id.String()).Scan(&ok); err != nil || !ok {
		_ = l.conn.Conn().Close(ctx)
	}
	l.conn.Release()
}
func (l *OperationLock) Get(ctx context.Context) (*PaidOperation, error) {
	return scanOperation(l.conn.QueryRow(ctx, `SELECT `+operationColumns+` FROM paid_operations WHERE id=$1`, l.id))
}
func (l *OperationLock) Save(ctx context.Context, o *PaidOperation) error {
	_, err := l.conn.Exec(ctx, `UPDATE paid_operations SET state=$2,secrets=$3,public_json=$4,last_error=$5,attempts=$6,next_attempt_at=$7,finished_at=$8,updated_at=now() WHERE id=$1`, o.ID, o.State, o.Secrets, o.PublicJSON, o.LastError, o.Attempts, o.NextAttemptAt, o.FinishedAt)
	return err
}

// CheckLegacyDrain prevents a v2 process from silently abandoning unclosed
// v0 work. Operators must settle it using the old deployment before cutover.
func (r *OperationRepo) CheckLegacyDrain(ctx context.Context) error {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM usage_reservations u WHERE u.settle_state='pending' AND NOT EXISTS(SELECT 1 FROM paid_operations p WHERE p.reservation_id=u.id))+(SELECT count(*) FROM live_streams l WHERE l.status IN ('provisioning','live') AND NOT EXISTS(SELECT 1 FROM paid_operations p WHERE p.live_stream_id=l.id))`).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("v2 cutover refused: %d legacy pending jobs/live sessions must be drained with the previous gateway", n)
	}
	return nil
}
