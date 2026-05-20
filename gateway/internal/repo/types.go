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
	ID                          uuid.UUID
	Name                        string
	Email                       string
	IPHash                      *string
	EmailVerifiedAt             *time.Time
	VerificationTokenHash       *string
	VerificationTokenExpiresAt  *time.Time
	Status                      WaitlistStatus
	ApprovedAt                  *time.Time
	ApprovedBy                  *string
	CreatedAt                   time.Time
}

type APIKey struct {
	ID          uuid.UUID
	WaitlistID  uuid.UUID
	Label       *string
	KeyPrefix   string
	KeyHash     string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	RevokedAt   *time.Time
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
	ID                    uuid.UUID
	APIKeyID              uuid.UUID
	WorkID                uuid.UUID
	Capability            string
	Offering              string
	BrokerURL             *string
	EthAddress            *string
	State                 ReservationState
	EstimatedWorkUnits    *int64
	CommittedWorkUnits    *int64
	PricePerWorkUnitWei   *big.Int
	LatencyMs             *int
	StatusCode            *int
	ErrorText             *string
	RunnerJobID           *string
	WebhookSecret         *string
	RunnerStatus          *string
	RunnerPhase           *string
	RunnerProgress        *float64
	RunnerErrorCode       *string
	RunnerErrorText       *string
	RunnerStateJSON       []byte
	RunnerCompletedAt     *time.Time
	CreatedAt             time.Time
	ResolvedAt            *time.Time
}

type LiveStreamStatus string

const (
	LiveProvisioning LiveStreamStatus = "provisioning"
	LiveActive       LiveStreamStatus = "live"
	LiveEnded        LiveStreamStatus = "ended"
	LiveFailed       LiveStreamStatus = "failed"
)

type LiveStream struct {
	ID              uuid.UUID
	APIKeyID        uuid.UUID
	ReservationID   *uuid.UUID
	Name            *string
	Status          LiveStreamStatus
	Capability      string
	Offering        string
	BrokerURL       *string
	EthAddress      *string
	IngestURL       *string
	StreamKeyHash   *string
	PlaybackURL     *string
	LadderJSON      []byte
	ErrorText       *string
	CreatedAt       time.Time
	StartedAt       *time.Time
	LastHeartbeatAt *time.Time
	EndedAt         *time.Time
}

type Capability struct {
	CapabilityID         string
	Capability           string
	Offering             string
	InteractionMode      *string
	Name                 *string
	Description          *string
	Provider             *string
	Category             *string
	EthAddress           *string
	PricePerWorkUnitWei  *big.Int
	BrokerURL            *string
	ExtraJSON            []byte
	ConstraintsJSON      []byte
	Active               bool
	SnapshotAt           time.Time
}
