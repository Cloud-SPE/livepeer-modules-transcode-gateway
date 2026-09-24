package repo

import (
	"math/big"
	"time"

	"github.com/google/uuid"
)

type WaitlistStatus string

const (
	WaitlistPending  WaitlistStatus = "pending"
	WaitlistApproved WaitlistStatus = "approved"
	WaitlistRejected WaitlistStatus = "rejected"
)

type Waitlist struct {
	ID                         uuid.UUID
	Name                       string
	Email                      string
	IPHash                     *string
	EmailVerifiedAt            *time.Time
	VerificationTokenHash      *string
	VerificationTokenExpiresAt *time.Time
	Status                     WaitlistStatus
	ApprovedAt                 *time.Time
	ApprovedBy                 *string
	CreatedAt                  time.Time
}

type APIKey struct {
	ID         uuid.UUID
	WaitlistID uuid.UUID
	Label      *string
	KeyPrefix  string
	KeyHash    string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

type UserSession struct {
	ID          uuid.UUID
	APIKeyID    uuid.UUID
	SessionHash string
	ExpiresAt   time.Time
	RevokedAt   *time.Time
	CreatedAt   time.Time
}

type ReservationState string

const (
	ReservationOpen      ReservationState = "open"
	ReservationCommitted ReservationState = "committed"
	ReservationRefunded  ReservationState = "refunded"
)

type UsageReservation struct {
	ID                  uuid.UUID
	APIKeyID            uuid.UUID
	WorkID              uuid.UUID
	Capability          string
	Offering            string
	BrokerURL           *string
	EthAddress          *string
	State               ReservationState
	EstimatedWorkUnits  *int64
	CommittedWorkUnits  *int64
	PricePerWorkUnitWei *big.Int
	LatencyMs           *int
	StatusCode          *int
	ErrorText           *string
	RunnerJobID         *string
	WebhookSecret       *string
	RunnerStatus        *string
	RunnerPhase         *string
	RunnerProgress      *float64
	RunnerErrorCode     *string
	RunnerErrorText     *string
	RunnerStateJSON     []byte
	RunnerCompletedAt   *time.Time
	// LOC job linkage (migration 0009). The gateway mints payments via
	// LOC's jobs API; every LOC job must eventually be settled. Nil /
	// SettleNone for pre-LOC rows.
	LOCJobID         *uuid.UUID
	LOCWorkID        *string
	FundedValueWei   *big.Int
	ExpectedValueWei *big.Int
	BilledValueWei   *big.Int
	SettledAt        *time.Time
	SettleState      SettleState
	CreatedAt        time.Time
	ResolvedAt       *time.Time
}

// SettleState tracks the LOC settle lifecycle independently of the
// reservation state (which records the broker dispatch outcome).
type SettleState string

const (
	SettleNone     SettleState = "none"     // pre-LOC row, no settle owed
	SettlePending  SettleState = "pending"  // LOC job created; settle not yet confirmed
	SettleSettled  SettleState = "settled"  // billed actual units
	SettleRefunded SettleState = "refunded" // settled with 0 units (failed dispatch)
	SettleFailed   SettleState = "failed"   // terminal settle error (operator attention)
)

type LiveStreamStatus string

const (
	LiveProvisioning LiveStreamStatus = "provisioning"
	LiveActive       LiveStreamStatus = "live"
	LiveEnded        LiveStreamStatus = "ended"
	LiveFailed       LiveStreamStatus = "failed"
)

type LiveStream struct {
	ID            uuid.UUID
	APIKeyID      uuid.UUID
	ReservationID *uuid.UUID
	Name          *string
	Status        LiveStreamStatus
	Capability    string
	Offering      string
	BrokerURL     *string
	EthAddress    *string
	IngestURL     *string
	StreamKeyHash *string
	PlaybackURL   *string
	LadderJSON    []byte
	ErrorText     *string
	// live-session-remote-runner@v0 identifiers (migration 0005). Nil
	// for legacy rows opened before the broker-paid / runner-media split.
	BrokerSessionID  *string
	RunnerSessionID  *string
	BrokerWorkID     *uuid.UUID
	CloseReason      *string
	LastBrokerSyncAt *time.Time
	// live-session-gateway-ingest@v0 identifiers (migration 0006).
	S3OutputPrefix   *string
	PrivateIngestURL *string
	StreamKeyHint    *string
	CreatedAt        time.Time
	StartedAt        *time.Time
	LastHeartbeatAt  *time.Time
	EndedAt          *time.Time
	// RunnerStatusJSON is the raw runner-status surface the broker most
	// recently reported (ingest + output blocks). Nil if absent. Admin
	// UI parses opportunistically; the gateway never interprets it.
	RunnerStatusJSON []byte
	// LOC session linkage (migration 0010). Nil for pre-LOC rows.
	LOCSessionID   *uuid.UUID
	LOCWorkID      *string
	LOCRefillCount int
	LOCClosedAt    *time.Time
}

type Capability struct {
	Protocol              string
	WorkUnit              string
	UnitsPerPrice         *big.Int
	WorkUnitEstimatorJSON []byte
	JobJSON               []byte
	SessionJSON           []byte
	CapabilityID          string
	Capability            string
	Offering              string
	InteractionMode       *string
	Name                  *string
	Description           *string
	Provider              *string
	Category              *string
	EthAddress            *string
	PricePerWorkUnitWei   *big.Int
	BrokerURL             *string
	ExtraJSON             []byte
	ConstraintsJSON       []byte
	Active                bool
	SnapshotAt            time.Time
}
