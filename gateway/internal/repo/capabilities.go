package repo

import (
	"context"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CapabilityRepo struct {
	pool *pgxpool.Pool
}

func NewCapabilityRepo(pool *pgxpool.Pool) *CapabilityRepo {
	return &CapabilityRepo{pool: pool}
}

type UpsertCapability struct {
	CapabilityID        string
	Capability          string
	Offering            string
	InteractionMode     string
	Name                string
	Description         string
	Provider            string
	Category            string
	EthAddress          string
	PricePerWorkUnitWei *big.Int
	BrokerURL           string
	ExtraJSON           []byte
	ConstraintsJSON     []byte
}

// ReplaceSnapshot upserts every row and marks everything not in this
// set inactive. Used by the background refresh task.
func (r *CapabilityRepo) ReplaceSnapshot(ctx context.Context, rows []UpsertCapability) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `UPDATE capabilities SET active = false WHERE active = true`); err != nil {
		return err
	}
	const up = `
		INSERT INTO capabilities (capability_id, capability, offering, interaction_mode, name,
		                          description, provider, category, eth_address,
		                          price_per_work_unit_wei, broker_url, extra_json,
		                          constraints_json, active, snapshot_at)
		VALUES ($1, $2, $3, NULLIF($4,''), NULLIF($5,''), NULLIF($6,''), NULLIF($7,''),
		        NULLIF($8,''), NULLIF($9,''), $10, NULLIF($11,''), $12, $13, true, now())
		ON CONFLICT (capability_id) DO UPDATE SET
		  capability=EXCLUDED.capability,
		  offering=EXCLUDED.offering,
		  interaction_mode=EXCLUDED.interaction_mode,
		  name=EXCLUDED.name,
		  description=EXCLUDED.description,
		  provider=EXCLUDED.provider,
		  category=EXCLUDED.category,
		  eth_address=EXCLUDED.eth_address,
		  price_per_work_unit_wei=EXCLUDED.price_per_work_unit_wei,
		  broker_url=EXCLUDED.broker_url,
		  extra_json=EXCLUDED.extra_json,
		  constraints_json=EXCLUDED.constraints_json,
		  active=true,
		  snapshot_at=now()`
	for _, c := range rows {
		if _, err := tx.Exec(ctx, up,
			c.CapabilityID, c.Capability, c.Offering, c.InteractionMode, c.Name,
			c.Description, c.Provider, c.Category, c.EthAddress,
			stringifyBig(c.PricePerWorkUnitWei), c.BrokerURL, c.ExtraJSON, c.ConstraintsJSON); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *CapabilityRepo) ListActive(ctx context.Context) ([]Capability, error) {
	const q = `SELECT capability_id, capability, offering, interaction_mode, name, description,
	                  provider, category, eth_address, price_per_work_unit_wei, broker_url,
	                  extra_json, constraints_json, active, snapshot_at
	           FROM capabilities WHERE active=true ORDER BY capability, offering`
	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Capability
	for rows.Next() {
		c, err := scanCapability(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *CapabilityRepo) LastSnapshot(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(snapshot_at), to_timestamp(0)) FROM capabilities`).Scan(&t)
	return t, err
}

// RefreshMeta is the persisted record of the last registry refresh run.
type RefreshMeta struct {
	LastRefreshAt    time.Time
	LastOutcome      string
	LastError        *string
	RowsMatched      int
	CapabilityFilter []string
}

// GetRefreshMeta returns the singleton refresh-meta row.
func (r *CapabilityRepo) GetRefreshMeta(ctx context.Context) (*RefreshMeta, error) {
	const q = `SELECT last_refresh_at, last_outcome, last_error, rows_matched, capability_filter
	           FROM capability_refresh_meta WHERE id = 1`
	var m RefreshMeta
	if err := r.pool.QueryRow(ctx, q).Scan(
		&m.LastRefreshAt, &m.LastOutcome, &m.LastError, &m.RowsMatched, &m.CapabilityFilter,
	); err != nil {
		return nil, err
	}
	return &m, nil
}

// RecordRefresh updates the singleton meta row. Called by the refresher on
// every tick — success or failure — so operators can see liveness even when
// the filter matches zero capabilities.
func (r *CapabilityRepo) RecordRefresh(ctx context.Context, outcome string, errMsg string, rowsMatched int, filter []string) error {
	const q = `UPDATE capability_refresh_meta
	           SET last_refresh_at = now(),
	               last_outcome    = $1,
	               last_error      = NULLIF($2, ''),
	               rows_matched    = $3,
	               capability_filter = $4
	           WHERE id = 1`
	_, err := r.pool.Exec(ctx, q, outcome, errMsg, rowsMatched, filter)
	return err
}

func scanCapability(s pgx.Row) (*Capability, error) {
	var c Capability
	var price *string
	err := s.Scan(&c.CapabilityID, &c.Capability, &c.Offering, &c.InteractionMode, &c.Name,
		&c.Description, &c.Provider, &c.Category, &c.EthAddress, &price, &c.BrokerURL,
		&c.ExtraJSON, &c.ConstraintsJSON, &c.Active, &c.SnapshotAt)
	if err != nil {
		return nil, err
	}
	if price != nil && *price != "" {
		b := new(big.Int)
		if _, ok := b.SetString(*price, 10); ok {
			c.PricePerWorkUnitWei = b
		}
	}
	return &c, nil
}
