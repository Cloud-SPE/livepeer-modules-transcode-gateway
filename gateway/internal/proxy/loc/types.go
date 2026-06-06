package loc

import (
	"encoding/base64"
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

// ── jobs (one-shot, post-settled) ────────────────────────────────────

type CreateJobRequest struct {
	Capability     string `json:"capability"`
	Offering       string `json:"offering"`
	EstimatedUnits int64  `json:"estimated_units"`
	MaxTotalUnits  *int64 `json:"max_total_units,omitempty"`
}

type CreateJobResponse struct {
	JobID            uuid.UUID `json:"job_id"`
	WorkID           string    `json:"work_id"` // hex recipient_rand_hash
	BrokerURL        string    `json:"broker_url"`
	Mode             string    `json:"mode"`
	PaymentEnvelope  string    `json:"payment_envelope"` // base64 Payment bytes
	ExpectedValueWei *Wei      `json:"expected_value_wei"`
	FundedValueWei   *Wei      `json:"funded_value_wei"`
	SettleEndpoint   string    `json:"settle_endpoint"`
	OpenedAt         time.Time `json:"opened_at"`
}

// PaymentBytes decodes the base64 envelope into the raw Payment bytes
// the broker expects in the Livepeer-Payment header.
func (r *CreateJobResponse) PaymentBytes() ([]byte, error) {
	return base64.StdEncoding.DecodeString(r.PaymentEnvelope)
}

type SettleJobRequest struct {
	ActualUnits int64          `json:"actual_units"`
	Outcome     string         `json:"outcome,omitempty"`
	Settlement  map[string]any `json:"settlement,omitempty"`
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

// ── sessions (long-running, refillable) ──────────────────────────────

type CreateSessionRequest struct {
	Capability           string `json:"capability"`
	Offering             string `json:"offering"`
	EstimatedRunwayUnits int64  `json:"estimated_runway_units"`
	MaxTotalUnits        int64  `json:"max_total_units"`
}

type CreateSessionResponse struct {
	SessionID        uuid.UUID `json:"session_id"`
	WorkID           string    `json:"work_id"`
	BrokerURL        string    `json:"broker_url"`
	Mode             string    `json:"mode"`
	PaymentEnvelope  string    `json:"payment_envelope"`
	ExpectedValueWei *Wei      `json:"expected_value_wei"`
	FundedValueWei   *Wei      `json:"funded_value_wei"`
	RefillEndpoint   string    `json:"refill_endpoint"`
	CloseEndpoint    string    `json:"close_endpoint"`
	OpenedAt         time.Time `json:"opened_at"`
}

func (r *CreateSessionResponse) PaymentBytes() ([]byte, error) {
	return base64.StdEncoding.DecodeString(r.PaymentEnvelope)
}

type RefillSessionRequest struct {
	// ObservedConsumedUnits is advisory (logged by LOC for triage); the
	// payment daemon's ledger remains authoritative for sizing.
	ObservedConsumedUnits *int64 `json:"observed_consumed_units,omitempty"`
}

type RefillSessionResponse struct {
	WorkID           string    `json:"work_id"` // same as the session's
	RefillSeq        int       `json:"refill_seq"`
	PaymentEnvelope  string    `json:"payment_envelope"`
	ExpectedValueWei *Wei      `json:"expected_value_wei"`
	FundedValueWei   *Wei      `json:"funded_value_wei"`
	CapStatus        CapStatus `json:"cap_status"`
}

func (r *RefillSessionResponse) PaymentBytes() ([]byte, error) {
	return base64.StdEncoding.DecodeString(r.PaymentEnvelope)
}

type CloseSessionRequest struct {
	ActualUnits int64          `json:"actual_units"`
	Outcome     string         `json:"outcome,omitempty"`
	Settlement  map[string]any `json:"settlement,omitempty"`
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
	Mode           string     `json:"mode"`
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
	Name      string     `json:"name"`
	WorkUnit  string     `json:"work_unit"`
	Offerings []Offering `json:"offerings"`
}

type Offering struct {
	ID                  string `json:"id"`
	PricePerWorkUnitWei *Wei   `json:"price_per_work_unit_wei"` // decimal string on the wire
	WorkUnit            string `json:"work_unit"`
}

type Orchestrator struct {
	EthAddress      string       `json:"eth_address"`
	WorkerURL       string       `json:"worker_url"`
	Capabilities    []Capability `json:"capabilities"`
	SignatureStatus string       `json:"signature_status"`
	FreshnessStatus string       `json:"freshness_status"`
}
