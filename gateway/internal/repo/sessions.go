package repo

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SessionRepo struct {
	pool *pgxpool.Pool
}

func NewSessionRepo(pool *pgxpool.Pool) *SessionRepo {
	return &SessionRepo{pool: pool}
}

func (r *SessionRepo) Insert(ctx context.Context, apiKeyID uuid.UUID, sessionHash string, expires time.Time) (*UserSession, error) {
	const q = `INSERT INTO user_sessions (api_key_id, session_hash, expires_at)
	           VALUES ($1, $2, $3)
	           RETURNING id, api_key_id, session_hash, expires_at, revoked_at, created_at`
	row := r.pool.QueryRow(ctx, q, apiKeyID, sessionHash, expires)
	return scanSession(row)
}

func (r *SessionRepo) GetByHash(ctx context.Context, hash string) (*UserSession, error) {
	const q = `SELECT id, api_key_id, session_hash, expires_at, revoked_at, created_at
	           FROM user_sessions WHERE session_hash = $1 AND revoked_at IS NULL AND expires_at > now()`
	row := r.pool.QueryRow(ctx, q, hash)
	s, err := scanSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func (r *SessionRepo) Revoke(ctx context.Context, sessionHash string) error {
	_, err := r.pool.Exec(ctx, `UPDATE user_sessions SET revoked_at = now() WHERE session_hash = $1`, sessionHash)
	return err
}

func scanSession(s pgx.Row) (*UserSession, error) {
	var us UserSession
	err := s.Scan(&us.ID, &us.APIKeyID, &us.SessionHash, &us.ExpiresAt, &us.RevokedAt, &us.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &us, nil
}
