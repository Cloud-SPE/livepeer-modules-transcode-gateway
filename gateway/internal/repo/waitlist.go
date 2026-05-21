package repo

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type WaitlistRepo struct {
	pool *pgxpool.Pool
}

func NewWaitlistRepo(pool *pgxpool.Pool) *WaitlistRepo {
	return &WaitlistRepo{pool: pool}
}

func (r *WaitlistRepo) Insert(ctx context.Context, name, email, ipHash, tokenHash string, tokenExpires time.Time) (*Waitlist, bool, error) {
	const q = `
		INSERT INTO waitlist (name, email, ip_hash, verification_token_hash, verification_token_expires_at)
		VALUES ($1, lower($2), $3, $4, $5)
		ON CONFLICT (email) DO NOTHING
		RETURNING id, name, email, ip_hash, email_verified_at, verification_token_hash,
		          verification_token_expires_at, status, approved_at, approved_by, created_at`
	row := r.pool.QueryRow(ctx, q, name, email, nullable(ipHash), tokenHash, tokenExpires)
	var w Waitlist
	var status string
	err := row.Scan(&w.ID, &w.Name, &w.Email, &w.IPHash, &w.EmailVerifiedAt,
		&w.VerificationTokenHash, &w.VerificationTokenExpiresAt, &status,
		&w.ApprovedAt, &w.ApprovedBy, &w.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil // already-existing email
	}
	if err != nil {
		return nil, false, err
	}
	w.Status = WaitlistStatus(status)
	return &w, true, nil
}

func (r *WaitlistRepo) ConsumeVerificationToken(ctx context.Context, tokenHash string) (*Waitlist, error) {
	const q = `
		UPDATE waitlist
		SET email_verified_at = COALESCE(email_verified_at, now()),
		    verification_token_hash = NULL,
		    verification_token_expires_at = NULL
		WHERE verification_token_hash = $1
		  AND (verification_token_expires_at IS NULL OR verification_token_expires_at > now())
		RETURNING id, name, email, ip_hash, email_verified_at, verification_token_hash,
		          verification_token_expires_at, status, approved_at, approved_by, created_at`
	row := r.pool.QueryRow(ctx, q, tokenHash)
	w, err := scanWaitlist(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return w, err
}

func (r *WaitlistRepo) GetByID(ctx context.Context, id uuid.UUID) (*Waitlist, error) {
	const q = `SELECT id, name, email, ip_hash, email_verified_at, verification_token_hash,
	                  verification_token_expires_at, status, approved_at, approved_by, created_at
	           FROM waitlist WHERE id = $1`
	return scanWaitlist(r.pool.QueryRow(ctx, q, id))
}

func (r *WaitlistRepo) ListByStatus(ctx context.Context, status WaitlistStatus, limit int) ([]Waitlist, error) {
	q := `SELECT id, name, email, ip_hash, email_verified_at, verification_token_hash,
	             verification_token_expires_at, status, approved_at, approved_by, created_at
	      FROM waitlist`
	args := []any{}
	if status != "" && status != "all" {
		q += ` WHERE status = $1`
		args = append(args, string(status))
	}
	q += ` ORDER BY created_at DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit)
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Waitlist
	for rows.Next() {
		w, err := scanWaitlist(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

func (r *WaitlistRepo) MarkApproved(ctx context.Context, tx pgx.Tx, id uuid.UUID, approver string) error {
	const q = `UPDATE waitlist SET status='approved', approved_at=now(), approved_by=$2
	           WHERE id=$1 AND status='pending'`
	tag, err := tx.Exec(ctx, q, id, approver)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("waitlist: row not pending")
	}
	return nil
}

func (r *WaitlistRepo) MarkRejected(ctx context.Context, id uuid.UUID, approver string) error {
	const q = `UPDATE waitlist SET status='rejected', approved_at=now(), approved_by=$2
	           WHERE id=$1 AND status='pending'`
	_, err := r.pool.Exec(ctx, q, id, approver)
	return err
}

func (r *WaitlistRepo) ResetVerificationToken(ctx context.Context, id uuid.UUID, tokenHash string, expires time.Time) error {
	const q = `UPDATE waitlist
	           SET verification_token_hash=$2, verification_token_expires_at=$3
	           WHERE id=$1`
	_, err := r.pool.Exec(ctx, q, id, tokenHash, expires)
	return err
}

func scanWaitlist(s pgx.Row) (*Waitlist, error) {
	var w Waitlist
	var status string
	err := s.Scan(&w.ID, &w.Name, &w.Email, &w.IPHash, &w.EmailVerifiedAt,
		&w.VerificationTokenHash, &w.VerificationTokenExpiresAt, &status,
		&w.ApprovedAt, &w.ApprovedBy, &w.CreatedAt)
	if err != nil {
		return nil, err
	}
	w.Status = WaitlistStatus(status)
	return &w, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
