package repo

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type APIKeyRepo struct {
	pool *pgxpool.Pool
}

func NewAPIKeyRepo(pool *pgxpool.Pool) *APIKeyRepo {
	return &APIKeyRepo{pool: pool}
}

func (r *APIKeyRepo) Insert(ctx context.Context, tx pgx.Tx, waitlistID uuid.UUID, label, prefix, hash string) (*APIKey, error) {
	const q = `INSERT INTO api_keys (waitlist_id, label, key_prefix, key_hash)
	           VALUES ($1, NULLIF($2,''), $3, $4)
	           RETURNING id, waitlist_id, label, key_prefix, key_hash, created_at, last_used_at, revoked_at`
	row := tx.QueryRow(ctx, q, waitlistID, label, prefix, hash)
	return scanAPIKey(row)
}

func (r *APIKeyRepo) GetByHash(ctx context.Context, hash string) (*APIKey, error) {
	const q = `SELECT id, waitlist_id, label, key_prefix, key_hash, created_at, last_used_at, revoked_at
	           FROM api_keys WHERE key_hash = $1 AND revoked_at IS NULL`
	row := r.pool.QueryRow(ctx, q, hash)
	k, err := scanAPIKey(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return k, err
}

func (r *APIKeyRepo) ListByWaitlist(ctx context.Context, waitlistID uuid.UUID) ([]APIKey, error) {
	const q = `SELECT id, waitlist_id, label, key_prefix, key_hash, created_at, last_used_at, revoked_at
	           FROM api_keys WHERE waitlist_id = $1 ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, q, waitlistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

func (r *APIKeyRepo) Revoke(ctx context.Context, id, waitlistID uuid.UUID) error {
	const q = `UPDATE api_keys SET revoked_at = now()
	           WHERE id = $1 AND waitlist_id = $2 AND revoked_at IS NULL`
	_, err := r.pool.Exec(ctx, q, id, waitlistID)
	return err
}

func (r *APIKeyRepo) TouchLastUsed(ctx context.Context, id uuid.UUID) {
	_, _ = r.pool.Exec(ctx, `UPDATE api_keys SET last_used_at = now() WHERE id = $1`, id)
}

func scanAPIKey(s pgx.Row) (*APIKey, error) {
	var k APIKey
	err := s.Scan(&k.ID, &k.WaitlistID, &k.Label, &k.KeyPrefix, &k.KeyHash,
		&k.CreatedAt, &k.LastUsedAt, &k.RevokedAt)
	if err != nil {
		return nil, err
	}
	return &k, nil
}
