package repo

import (
	"context"
	"errors"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ReservationRepo struct {
	pool *pgxpool.Pool
}

func NewReservationRepo(pool *pgxpool.Pool) *ReservationRepo {
	return &ReservationRepo{pool: pool}
}

type OpenInput struct {
	APIKeyID            uuid.UUID
	WorkID              uuid.UUID
	Capability          string
	Offering            string
	EstimatedWorkUnits  *int64
	PricePerWorkUnitWei *big.Int
}

func (r *ReservationRepo) Open(ctx context.Context, in OpenInput) (*UsageReservation, error) {
	const q = `
		INSERT INTO usage_reservations
		  (api_key_id, work_id, capability, offering, state, estimated_work_units, price_per_work_unit_wei)
		VALUES ($1, $2, $3, $4, 'open', $5, $6)
		RETURNING id, api_key_id, work_id, capability, offering, broker_url, eth_address,
		          state, estimated_work_units, committed_work_units, price_per_work_unit_wei,
		          latency_ms, status_code, error_text, runner_job_id,
		          webhook_secret, runner_status, runner_phase, runner_progress,
		          runner_error_code, runner_error_text, runner_state_json, runner_completed_at,
		          created_at, resolved_at`
	row := r.pool.QueryRow(ctx, q, in.APIKeyID, in.WorkID, in.Capability, in.Offering,
		in.EstimatedWorkUnits, stringifyBig(in.PricePerWorkUnitWei))
	return scanReservation(row)
}

type CommitInput struct {
	BrokerURL          string
	EthAddress         string
	CommittedWorkUnits *int64
	LatencyMs          *int
	StatusCode         *int
}

func (r *ReservationRepo) Commit(ctx context.Context, id uuid.UUID, in CommitInput) error {
	const q = `UPDATE usage_reservations
	           SET state='committed', broker_url=$2, eth_address=$3,
	               committed_work_units=$4, latency_ms=$5, status_code=$6, resolved_at=now()
	           WHERE id=$1 AND state='open'`
	_, err := r.pool.Exec(ctx, q, id, nullable(in.BrokerURL), nullable(in.EthAddress),
		in.CommittedWorkUnits, in.LatencyMs, in.StatusCode)
	return err
}

func (r *ReservationRepo) Refund(ctx context.Context, id uuid.UUID, statusCode int, errorText string) error {
	const q = `UPDATE usage_reservations
	           SET state='refunded', status_code=$2, error_text=$3, resolved_at=now()
	           WHERE id=$1 AND state='open'`
	_, err := r.pool.Exec(ctx, q, id, statusCode, errorText)
	return err
}

func (r *ReservationRepo) GetByWorkID(ctx context.Context, workID uuid.UUID) (*UsageReservation, error) {
	const q = `SELECT id, api_key_id, work_id, capability, offering, broker_url, eth_address,
	                  state, estimated_work_units, committed_work_units, price_per_work_unit_wei,
	                  latency_ms, status_code, error_text, runner_job_id,
	                  webhook_secret, runner_status, runner_phase, runner_progress,
	                  runner_error_code, runner_error_text, runner_state_json, runner_completed_at,
	                  created_at, resolved_at
	           FROM usage_reservations WHERE work_id=$1`
	row := r.pool.QueryRow(ctx, q, workID)
	res, err := scanReservation(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return res, err
}

func (r *ReservationRepo) GetByID(ctx context.Context, id uuid.UUID) (*UsageReservation, error) {
	const q = `SELECT id, api_key_id, work_id, capability, offering, broker_url, eth_address,
	                  state, estimated_work_units, committed_work_units, price_per_work_unit_wei,
	                  latency_ms, status_code, error_text, runner_job_id,
	                  webhook_secret, runner_status, runner_phase, runner_progress,
	                  runner_error_code, runner_error_text, runner_state_json, runner_completed_at,
	                  created_at, resolved_at
	           FROM usage_reservations WHERE id=$1`
	row := r.pool.QueryRow(ctx, q, id)
	res, err := scanReservation(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return res, err
}

func (r *ReservationRepo) ListByAPIKey(ctx context.Context, apiKeyID uuid.UUID, since time.Time, limit int) ([]UsageReservation, error) {
	const q = `SELECT id, api_key_id, work_id, capability, offering, broker_url, eth_address,
	                  state, estimated_work_units, committed_work_units, price_per_work_unit_wei,
	                  latency_ms, status_code, error_text, runner_job_id,
	                  webhook_secret, runner_status, runner_phase, runner_progress,
	                  runner_error_code, runner_error_text, runner_state_json, runner_completed_at,
	                  created_at, resolved_at
	           FROM usage_reservations
	           WHERE api_key_id=$1 AND created_at >= $2
	           ORDER BY created_at DESC LIMIT $3`
	rows, err := r.pool.Query(ctx, q, apiKeyID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageReservation
	for rows.Next() {
		res, err := scanReservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *res)
	}
	return out, rows.Err()
}

// UsageSummaryByAPIKey is one row of the /admin/usage rollup.
type UsageSummaryByAPIKey struct {
	APIKeyID       uuid.UUID
	WaitlistID     uuid.UUID
	Email          string
	KeyPrefix      string
	TotalRequests  int
	CommittedTotal int
	RefundedTotal  int
	OpenTotal      int
	LastUsedAt     *time.Time
}

// SummaryByAPIKey returns the top `limit` API keys by recency with their
// usage rollup (joined to the owning user's email). Powers /admin/usage.
func (r *ReservationRepo) SummaryByAPIKey(ctx context.Context, limit int) ([]UsageSummaryByAPIKey, error) {
	if limit <= 0 {
		limit = 200
	}
	const q = `
		SELECT ak.id, w.id, w.email, ak.key_prefix,
		       count(*)                                          AS total,
		       count(*) FILTER (WHERE ur.state='committed')      AS committed,
		       count(*) FILTER (WHERE ur.state='refunded')       AS refunded,
		       count(*) FILTER (WHERE ur.state='open')           AS open_count,
		       max(ur.created_at)                                AS last_used
		FROM usage_reservations ur
		JOIN api_keys  ak ON ak.id = ur.api_key_id
		JOIN waitlist  w  ON w.id  = ak.waitlist_id
		GROUP BY ak.id, w.id, w.email, ak.key_prefix
		ORDER BY last_used DESC NULLS LAST
		LIMIT $1`
	rows, err := r.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageSummaryByAPIKey
	for rows.Next() {
		var s UsageSummaryByAPIKey
		if err := rows.Scan(&s.APIKeyID, &s.WaitlistID, &s.Email, &s.KeyPrefix,
			&s.TotalRequests, &s.CommittedTotal, &s.RefundedTotal, &s.OpenTotal,
			&s.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UsageSummaryByWaitlist powers the per-user drilldown.
type UsageSummaryByWaitlist struct {
	TotalRequests  int
	CommittedTotal int
	RefundedTotal  int
	OpenTotal      int
	LastUsedAt     *time.Time
}

// SummaryByWaitlist aggregates every reservation across every API key
// owned by one waitlist row (i.e. one user).
func (r *ReservationRepo) SummaryByWaitlist(ctx context.Context, waitlistID uuid.UUID) (*UsageSummaryByWaitlist, error) {
	const q = `
		SELECT count(*),
		       count(*) FILTER (WHERE ur.state='committed'),
		       count(*) FILTER (WHERE ur.state='refunded'),
		       count(*) FILTER (WHERE ur.state='open'),
		       max(ur.created_at)
		FROM usage_reservations ur
		JOIN api_keys ak ON ak.id = ur.api_key_id
		WHERE ak.waitlist_id = $1`
	var s UsageSummaryByWaitlist
	if err := r.pool.QueryRow(ctx, q, waitlistID).Scan(
		&s.TotalRequests, &s.CommittedTotal, &s.RefundedTotal, &s.OpenTotal, &s.LastUsedAt,
	); err != nil {
		return nil, err
	}
	return &s, nil
}

// ListByCapability returns recent reservations filtered by capability —
// powers /admin/abr-jobs and similar transcode-specific views.
func (r *ReservationRepo) ListByCapability(ctx context.Context, capability string, since time.Time, limit int) ([]UsageReservation, error) {
	if limit <= 0 {
		limit = 200
	}
	const q = `SELECT id, api_key_id, work_id, capability, offering, broker_url, eth_address,
	                  state, estimated_work_units, committed_work_units, price_per_work_unit_wei,
	                  latency_ms, status_code, error_text, runner_job_id,
	                  webhook_secret, runner_status, runner_phase, runner_progress,
	                  runner_error_code, runner_error_text, runner_state_json, runner_completed_at,
	                  created_at, resolved_at
	           FROM usage_reservations
	           WHERE capability = $1 AND created_at >= $2
	           ORDER BY created_at DESC LIMIT $3`
	rows, err := r.pool.Query(ctx, q, capability, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageReservation
	for rows.Next() {
		res, err := scanReservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *res)
	}
	return out, rows.Err()
}

// CapabilityRollup is one row of the "usage by capability" view.
type CapabilityRollup struct {
	Capability      string
	TotalRequests   int
	CommittedTotal  int
	RefundedTotal   int
	OpenTotal       int
	RunnerSucceeded int
	RunnerFailed    int
	LastUsedAt      *time.Time
}

// SummaryByCapability rolls up reservations by capability, including a
// view-through of how the runner's eventual outcome landed (when a
// webhook arrived). Separates the gateway's "committed" (broker
// accepted) from the runner's "complete/error" (job actually succeeded
// or not).
func (r *ReservationRepo) SummaryByCapability(ctx context.Context, since time.Time) ([]CapabilityRollup, error) {
	const q = `
		SELECT capability,
		       count(*),
		       count(*) FILTER (WHERE state='committed'),
		       count(*) FILTER (WHERE state='refunded'),
		       count(*) FILTER (WHERE state='open'),
		       count(*) FILTER (WHERE runner_status='complete'),
		       count(*) FILTER (WHERE runner_status='error'),
		       max(created_at)
		FROM usage_reservations
		WHERE created_at >= $1
		GROUP BY capability
		ORDER BY capability`
	rows, err := r.pool.Query(ctx, q, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CapabilityRollup
	for rows.Next() {
		var c CapabilityRollup
		if err := rows.Scan(&c.Capability, &c.TotalRequests, &c.CommittedTotal,
			&c.RefundedTotal, &c.OpenTotal, &c.RunnerSucceeded, &c.RunnerFailed,
			&c.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetWebhookSecret stores the per-job HMAC secret used to verify
// incoming runner webhooks against this reservation.
func (r *ReservationRepo) SetWebhookSecret(ctx context.Context, workID uuid.UUID, secret string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE usage_reservations SET webhook_secret = $2 WHERE work_id = $1`,
		workID, secret)
	return err
}

// RunnerStateUpdate is the projection of a runner webhook payload we
// persist on the reservation.
type RunnerStateUpdate struct {
	Status      string  // queued | downloading | probing | encoding | uploading | complete | error
	Phase       string
	Progress    float64
	ErrorCode   string
	ErrorText   string
	StateJSON   []byte // full runner payload, for diagnostics
	CompletedAt *time.Time
}

// RecordRunnerWebhook updates the reservation row from a verified
// runner webhook. Idempotent — same payload twice is a no-op.
func (r *ReservationRepo) RecordRunnerWebhook(ctx context.Context, workID uuid.UUID, in RunnerStateUpdate) error {
	const q = `UPDATE usage_reservations
	           SET runner_status       = NULLIF($2, ''),
	               runner_phase        = NULLIF($3, ''),
	               runner_progress     = $4,
	               runner_error_code   = NULLIF($5, ''),
	               runner_error_text   = NULLIF($6, ''),
	               runner_state_json   = $7,
	               runner_completed_at = $8
	           WHERE work_id = $1`
	_, err := r.pool.Exec(ctx, q, workID, in.Status, in.Phase, in.Progress,
		in.ErrorCode, in.ErrorText, in.StateJSON, in.CompletedAt)
	return err
}

// LookupByRunnerJobID resolves the reservation a runner webhook is
// referring to (the runner only knows its own job_id, not our work_id).
func (r *ReservationRepo) LookupByRunnerJobID(ctx context.Context, runnerJobID string) (*UsageReservation, error) {
	const q = `SELECT id, api_key_id, work_id, capability, offering, broker_url, eth_address,
	                  state, estimated_work_units, committed_work_units, price_per_work_unit_wei,
	                  latency_ms, status_code, error_text, runner_job_id,
	                  webhook_secret, runner_status, runner_phase, runner_progress,
	                  runner_error_code, runner_error_text, runner_state_json, runner_completed_at,
	                  created_at, resolved_at
	           FROM usage_reservations WHERE runner_job_id = $1`
	res, err := scanReservation(r.pool.QueryRow(ctx, q, runnerJobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return res, err
}

// SetRunnerJobID records the runner-assigned job id (e.g.
// "abr-1779264668487-b86c95fe") so the GET /v1/abr/{work_id} handler
// can ask the runner for live status.
func (r *ReservationRepo) SetRunnerJobID(ctx context.Context, workID uuid.UUID, jobID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE usage_reservations SET runner_job_id = $2 WHERE work_id = $1`,
		workID, jobID)
	return err
}

func scanReservation(s pgx.Row) (*UsageReservation, error) {
	var r UsageReservation
	var state string
	var price *string
	err := s.Scan(&r.ID, &r.APIKeyID, &r.WorkID, &r.Capability, &r.Offering,
		&r.BrokerURL, &r.EthAddress, &state, &r.EstimatedWorkUnits, &r.CommittedWorkUnits,
		&price, &r.LatencyMs, &r.StatusCode, &r.ErrorText, &r.RunnerJobID,
		&r.WebhookSecret, &r.RunnerStatus, &r.RunnerPhase, &r.RunnerProgress,
		&r.RunnerErrorCode, &r.RunnerErrorText, &r.RunnerStateJSON, &r.RunnerCompletedAt,
		&r.CreatedAt, &r.ResolvedAt)
	if err != nil {
		return nil, err
	}
	r.State = ReservationState(state)
	if price != nil && *price != "" {
		b := new(big.Int)
		if _, ok := b.SetString(*price, 10); ok {
			r.PricePerWorkUnitWei = b
		}
	}
	return &r, nil
}

func stringifyBig(b *big.Int) any {
	if b == nil {
		return nil
	}
	return b.String()
}
