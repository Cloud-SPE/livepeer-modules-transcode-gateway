package loc

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"net/http"
	"time"
)

// RequestID must be persisted before issuance and reused verbatim on recovery.
type CreateJobRequestV2 struct {
	RequestID             string         `json:"-"`
	Capability            string         `json:"capability"`
	Offering              string         `json:"offering"`
	Transport             string         `json:"transport"`
	EstimatedUnits        int64          `json:"estimated_units"`
	MaxTotalUnits         int64          `json:"max_total_units"`
	WorkloadRequestDigest string         `json:"workload_request_digest"`
	CallerPublicKey       string         `json:"caller_public_key"`
	RouteBinding          map[string]any `json:"route_binding,omitempty"`
}
type CreateJobResponseV2 struct {
	JobID              uuid.UUID      `json:"job_id"`
	RequestID          string         `json:"request_id"`
	WorkID             string         `json:"work_id"`
	BrokerURL          string         `json:"broker_url"`
	Protocol           string         `json:"protocol"`
	Transport          string         `json:"transport"`
	WorkUnit           string         `json:"work_unit"`
	SpendAuthorization string         `json:"spend_authorization"`
	AccountingMode     string         `json:"accounting_mode"`
	RouteSnapshot      map[string]any `json:"route_snapshot"`
	ExpectedValueWei   *Wei           `json:"expected_value_wei"`
	FundedValueWei     *Wei           `json:"funded_value_wei"`
	OpenedAt           time.Time      `json:"opened_at"`
}
type SettleJobRequestV2 struct {
	ActualUnits int64          `json:"actual_units"`
	BrokerJobID string         `json:"broker_job_id"`
	WorkUnit    string         `json:"work_unit"`
	Outcome     string         `json:"outcome,omitempty"`
	Settlement  map[string]any `json:"settlement"`
}
type PrepareSessionRequestV2 struct {
	RequestID        string         `json:"-"`
	Capability       string         `json:"capability"`
	Offering         string         `json:"offering"`
	DescriptorSchema string         `json:"descriptor_schema"`
	RouteBinding     map[string]any `json:"route_binding,omitempty"`
}
type PrepareSessionResponseV2 struct {
	GatewaySessionID uuid.UUID      `json:"gateway_session_id"`
	RouteBinding     map[string]any `json:"route_binding"`
	BrokerURL        string         `json:"broker_url"`
	PreparationToken string         `json:"preparation_token"`
	ExpiresAt        time.Time      `json:"expires_at"`
}
type CreateSessionRequestV2 struct {
	RequestID             string         `json:"-"`
	Capability            string         `json:"capability"`
	Offering              string         `json:"offering"`
	DescriptorSchema      string         `json:"descriptor_schema"`
	SessionParams         map[string]any `json:"session_params"`
	EstimatedRunwayUnits  int64          `json:"estimated_runway_units"`
	MaxTotalUnits         int64          `json:"max_total_units"`
	GatewaySessionID      uuid.UUID      `json:"gateway_session_id"`
	PreparationToken      string         `json:"preparation_token"`
	RouteBinding          map[string]any `json:"route_binding,omitempty"`
	WorkloadRequestDigest string         `json:"workload_request_digest"`
	CallerPublicKey       string         `json:"caller_public_key"`
}
type SessionAxesV2 struct {
	DescriptorSchema string `json:"descriptor_schema"`
	Attachment       string `json:"attachment"`
	Metering         string `json:"metering"`
	Refill           string `json:"refill"`
}
type CreateSessionResponseV2 struct {
	SessionID          uuid.UUID      `json:"session_id"`
	RequestID          string         `json:"request_id"`
	WorkID             string         `json:"work_id"`
	BrokerURL          string         `json:"broker_url"`
	Protocol           string         `json:"protocol"`
	Session            SessionAxesV2  `json:"session"`
	SpendAuthorization string         `json:"spend_authorization"`
	AccountingMode     string         `json:"accounting_mode"`
	RouteSnapshot      map[string]any `json:"route_snapshot"`
	ExpectedValueWei   *Wei           `json:"expected_value_wei"`
	FundedValueWei     *Wei           `json:"funded_value_wei"`
	OpenedAt           time.Time      `json:"opened_at"`
}
type RefillSessionRequestV2 struct {
	RequestID             string `json:"-"`
	ObservedConsumedUnits *int64 `json:"observed_consumed_units,omitempty"`
	MaxTotalUnits         int64  `json:"max_total_units"`
	WorkloadRequestDigest string `json:"workload_request_digest"`
}
type RefillSessionResponseV2 struct {
	WorkID             string    `json:"work_id"`
	RequestID          string    `json:"request_id"`
	RefillSeq          int       `json:"refill_seq"`
	SpendAuthorization string    `json:"spend_authorization"`
	AccountingMode     string    `json:"accounting_mode"`
	ExpectedValueWei   *Wei      `json:"expected_value_wei"`
	FundedValueWei     *Wei      `json:"funded_value_wei"`
	CapStatus          CapStatus `json:"cap_status"`
}
type CloseSessionRequestV2 struct {
	ActualUnits int64          `json:"actual_units"`
	Outcome     string         `json:"outcome,omitempty"`
	Settlement  map[string]any `json:"settlement"`
}
type JobStatusResponseV2 struct {
	JobID                 uuid.UUID  `json:"job_id"`
	RequestID             string     `json:"request_id"`
	WorkID                string     `json:"work_id"`
	State                 string     `json:"state"`
	AccountingOutcome     string     `json:"accounting_outcome"`
	BrokerExchangeOutcome *string    `json:"broker_exchange_outcome"`
	ActualUnits           *int64     `json:"actual_units"`
	BilledValueWei        *Wei       `json:"billed_value_wei"`
	FundedValueWei        *Wei       `json:"funded_value_wei"`
	ClosedAt              *time.Time `json:"closed_at"`
}

func (c *Client) issueV2(ctx context.Context, path, id string, in, out any) error {
	if id == "" {
		return fmt.Errorf("loc: persistent idempotency key is required")
	}
	return c.doJSONHeaders(ctx, http.MethodPost, path, in, out, http.Header{"Idempotency-Key": []string{id}})
}
func (c *Client) CreateJobV2(ctx context.Context, in CreateJobRequestV2) (*CreateJobResponseV2, error) {
	var out CreateJobResponseV2
	if err := c.issueV2(ctx, "/v1/jobs", in.RequestID, in, &out); err != nil {
		return nil, err
	}
	if out.Protocol != "paid-job/v1" || out.Transport != in.Transport || out.RequestID == "" || out.AccountingMode != "wholesale_account" || out.SpendAuthorization == "" || out.WorkID == "" || out.JobID == uuid.Nil || out.WorkUnit == "" {
		return nil, fmt.Errorf("loc: invalid paid-job authorization response")
	}
	return &out, nil
}
func (c *Client) PrepareSessionV2(ctx context.Context, in PrepareSessionRequestV2) (*PrepareSessionResponseV2, error) {
	var out PrepareSessionResponseV2
	if err := c.issueV2(ctx, "/v1/sessions/prepare", in.RequestID, in, &out); err != nil {
		return nil, err
	}
	if out.GatewaySessionID == uuid.Nil || out.PreparationToken == "" || out.BrokerURL == "" {
		return nil, fmt.Errorf("loc: incomplete session preparation")
	}
	return &out, nil
}
func (c *Client) OpenSessionV2(ctx context.Context, in CreateSessionRequestV2) (*CreateSessionResponseV2, error) {
	var out CreateSessionResponseV2
	if err := c.issueV2(ctx, "/v1/sessions", in.RequestID, in, &out); err != nil {
		return nil, err
	}
	if out.Protocol != "paid-session/v1" || out.Session.DescriptorSchema != in.DescriptorSchema || out.RequestID == "" || out.AccountingMode != "wholesale_account" || out.SpendAuthorization == "" || out.SessionID == uuid.Nil || out.WorkID == "" {
		return nil, fmt.Errorf("loc: invalid paid-session authorization response")
	}
	return &out, nil
}
func (c *Client) RefillSessionV2(ctx context.Context, id uuid.UUID, in RefillSessionRequestV2) (*RefillSessionResponseV2, error) {
	var out RefillSessionResponseV2
	if err := c.issueV2(ctx, "/v1/sessions/"+id.String()+"/refill", in.RequestID, in, &out); err != nil {
		return nil, err
	}
	if out.RequestID == "" || out.SpendAuthorization == "" || out.AccountingMode != "wholesale_account" {
		return nil, fmt.Errorf("loc: invalid authorization revision response")
	}
	return &out, nil
}
func (c *Client) SettleJobV2(ctx context.Context, id uuid.UUID, in SettleJobRequestV2) (*SettleJobResponse, error) {
	if in.Settlement == nil || in.BrokerJobID == "" || in.WorkUnit == "" || in.ActualUnits < 0 {
		return nil, fmt.Errorf("loc: signed terminal claim required")
	}
	var out SettleJobResponse
	if err := c.doJSONRetry(ctx, http.MethodPost, "/v1/jobs/"+id.String()+"/settle", in, &out, 3); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *Client) CloseSessionV2(ctx context.Context, id uuid.UUID, in CloseSessionRequestV2) (*CloseSessionResponse, error) {
	if in.Settlement == nil || in.ActualUnits < 0 {
		return nil, fmt.Errorf("loc: signed terminal claim required")
	}
	var out CloseSessionResponse
	if err := c.doJSONRetry(ctx, http.MethodPost, "/v1/sessions/"+id.String()+"/close", in, &out, 3); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *Client) GetJobV2(ctx context.Context, id uuid.UUID) (*JobStatusResponseV2, error) {
	var out JobStatusResponseV2
	if err := c.doJSON(ctx, http.MethodGet, "/v1/jobs/"+id.String(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *Client) GetSessionV2(ctx context.Context, id uuid.UUID) (*SessionStatusResponse, error) {
	return c.GetSession(ctx, id)
}
