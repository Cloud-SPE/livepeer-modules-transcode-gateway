package loc

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Wei is a wei amount decoded from LOC JSON. LOC serializes wei as a
// bare JSON number (Python unbounded int) which can exceed int64 at
// production magnitudes (1 ETH = 10^18), and the discovery catalog
// serializes prices as decimal strings — Wei accepts both.
type Wei struct {
	big.Int
}

func (w *Wei) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	if _, ok := w.SetString(s, 10); !ok {
		return fmt.Errorf("loc: invalid wei value %q", s)
	}
	return nil
}

func (w *Wei) MarshalJSON() ([]byte, error) {
	if w == nil {
		return []byte("null"), nil
	}
	return []byte(w.String()), nil
}

// BigInt returns the underlying value, nil-safe.
func (w *Wei) BigInt() *big.Int {
	if w == nil {
		return nil
	}
	return &w.Int
}

type SettleJobResponse struct {
	JobID          uuid.UUID `json:"job_id"`
	WorkID         string    `json:"work_id"`
	ActualUnits    int64     `json:"actual_units"`
	BilledValueWei *Wei      `json:"billed_value_wei"`
	RefundWei      *Wei      `json:"refund_wei"`
	Outcome        string    `json:"outcome"`
	ClosedAt       time.Time `json:"closed_at"`
	CapStatus      CapStatus `json:"cap_status"`
}

type CloseSessionResponse struct {
	SessionID      uuid.UUID `json:"session_id"`
	WorkID         string    `json:"work_id"`
	ActualUnits    int64     `json:"actual_units"`
	BilledValueWei *Wei      `json:"billed_value_wei"`
	RefundWei      *Wei      `json:"refund_wei"`
	Outcome        string    `json:"outcome"`
	ClosedAt       time.Time `json:"closed_at"`
}

type SessionStatusResponse struct {
	SessionID      uuid.UUID  `json:"session_id"`
	WorkID         string     `json:"work_id"`
	Capability     string     `json:"capability"`
	Offering       string     `json:"offering"`
	Protocol       string     `json:"protocol"`
	State          string     `json:"state"`
	EstimatedUnits int64      `json:"estimated_units"`
	MaxTotalUnits  int64      `json:"max_total_units"`
	FundedValueWei *Wei       `json:"funded_value_wei"`
	BilledValueWei *Wei       `json:"billed_value_wei"`
	RefillCount    int        `json:"refill_count"`
	CapStatus      *CapStatus `json:"cap_status"`
	ActualUnits    *int64     `json:"actual_units"`
	Outcome        *string    `json:"outcome"`
	OpenedAt       time.Time  `json:"opened_at"`
	ClosedAt       *time.Time `json:"closed_at"`
}

// CapStatus is LOC's cap-headroom snapshot. Percentages are in [0,1];
// nil means that cap isn't configured. WillRefuseNextRefill is the
// advance winddown warning — when true, the next refill WILL be
// refused, so long-running sessions should end gracefully.
type CapStatus struct {
	SessionPctUsed       float64  `json:"session_pct_used"`
	SpendPeriodPctUsed   *float64 `json:"spend_period_pct_used"`
	UserBalancePctUsed   *float64 `json:"user_balance_pct_used"`
	OperatorPoolPctUsed  *float64 `json:"operator_pool_pct_used"`
	WillRefuseNextRefill bool     `json:"will_refuse_next_refill"`
	WinddownReason       *string  `json:"winddown_reason"`
}

// ── discovery (read-only catalog) ─────────────────────────────────────

type Capability struct {
	Name              string          `json:"name"`
	WorkUnit          string          `json:"work_unit"`
	Offerings         []Offering      `json:"offerings"`
	WorkUnitEstimator json.RawMessage `json:"work_unit_estimator"`
}

type Offering struct {
	ID                  string          `json:"id"`
	Protocol            string          `json:"protocol"`
	UnitsPerPrice       *Wei            `json:"units_per_price"`
	WorkUnitEstimator   json.RawMessage `json:"work_unit_estimator"`
	Job                 json.RawMessage `json:"job"`
	Session             json.RawMessage `json:"session"`
	Extra               json.RawMessage `json:"extra"`
	PricePerWorkUnitWei *Wei            `json:"price_per_work_unit_wei"` // decimal string on the wire
	WorkUnit            string          `json:"work_unit"`
}

type Orchestrator struct {
	EthAddress      string       `json:"eth_address"`
	WorkerURL       string       `json:"worker_url"`
	Capabilities    []Capability `json:"capabilities"`
	SignatureStatus string       `json:"signature_status"`
	FreshnessStatus string       `json:"freshness_status"`
}
