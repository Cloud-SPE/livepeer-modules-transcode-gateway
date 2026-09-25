package livepeer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	ProtocolJobV1       = "paid-job/v1"
	ProtocolSessionV1   = "paid-session/v1"
	HeaderProtocol      = "Livepeer-Protocol"
	HeaderAuthorization = "Livepeer-Authorization"
	HeaderCallerProof   = "Livepeer-Caller-Proof"
	HeaderSettlement    = "Livepeer-Settlement"
)

var ErrAccountingPending = errors.New("broker accounting evidence is not terminal")

// ExchangePendingError contains only allowlisted diagnostics, never broker detail
// text or settlement material. An admission rejection is not signed non-admission.
type ExchangePendingError struct {
	Outcome string
}

func (e *ExchangePendingError) Error() string { return "broker exchange: " + e.Outcome }
func (e *ExchangePendingError) Unwrap() error { return ErrAccountingPending }

// BrokerAuth is scoped to one immutable invocation. Persist it with its exact
// request body before dispatch, so retries cannot admit a second workload.
type BrokerAuth struct {
	Capability    string `json:"capability"`
	Offering      string `json:"offering"`
	RequestID     string `json:"request_id"`
	Authorization string `json:"authorization"`
	CallerProof   string `json:"caller_proof"`
	WorkID        string `json:"work_id"`
}

func (a BrokerAuth) headers(protocol string) (http.Header, error) {
	if a.Capability == "" || a.Offering == "" || a.RequestID == "" || a.Authorization == "" || a.CallerProof == "" {
		return nil, fmt.Errorf("broker: incomplete scoped authorization")
	}
	return http.Header{HeaderCapability: []string{a.Capability}, HeaderOffering: []string{a.Offering}, HeaderProtocol: []string{protocol}, HeaderRequestID: []string{a.RequestID}, HeaderAuthorization: []string{a.Authorization}, HeaderCallerProof: []string{a.CallerProof}}, nil
}

type SSEEvent struct {
	ID    string
	Event string
	Data  json.RawMessage
}
type TerminalClaim struct {
	BrokerJobID      string         `json:"broker_job_id,omitempty"`
	SessionID        string         `json:"session_id,omitempty"`
	GatewaySessionID string         `json:"gateway_session_id,omitempty"`
	RequestID        string         `json:"request_id,omitempty"`
	WorkID           string         `json:"work_id"`
	WorkUnit         string         `json:"work_unit"`
	ActualUnits      int64          `json:"actual_units"`
	Settlement       map[string]any `json:"settlement"`
	State            string         `json:"state,omitempty"`
	Outcome          string         `json:"outcome,omitempty"`
	CloseReason      string         `json:"close_reason,omitempty"`
	OutputState      string         `json:"output_state,omitempty"`
	FailureCode      string         `json:"failure_code,omitempty"`
}

func endpoint(base, path string) string { return strings.TrimRight(base, "/") + path }
func (c *HTTPClient) requestV2(ctx context.Context, method, target string, headers http.Header, body []byte, stream bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		for _, value := range v {
			req.Header.Add(k, value)
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	clone := *c.Client
	// A scoped invocation must never follow a redirect to another route.
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client := &clone
	// Stream lifetime is controlled by the worker's context, not a 30-second
	// unary timeout. Keep connection/response-header timeouts on the transport.
	if stream {
		clone.Timeout = 0
		req.Header.Set("Accept", "text/event-stream")
	}
	return client.Do(req)
}
func brokerResponseError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
	return &BrokerError{URL: resp.Request.URL.String(), StatusCode: resp.StatusCode, Body: string(b)}
}
func (c *HTTPClient) jsonV2(ctx context.Context, method, target string, headers http.Header, body []byte, out any) (http.Header, error) {
	resp, err := c.requestV2(ctx, method, target, headers, body, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.Header, brokerResponseError(resp)
	}
	if out != nil {
		dec := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
		dec.UseNumber()
		if err := dec.Decode(out); err != nil {
			return resp.Header, err
		}
	}
	return resp.Header, nil
}

// SubmitJobV2 consumes the entire SSE response. It never estimates settlement
// from progress or elapsed time; missing trailers recover through exchange lookup.
func (c *HTTPClient) SubmitJobV2(ctx context.Context, brokerURL string, auth BrokerAuth, body []byte, expectedWorkUnit string, onEvent func(SSEEvent) error) (*TerminalClaim, error) {
	headers, err := auth.headers(ProtocolJobV1)
	if err != nil {
		return nil, err
	}
	resp, err := c.requestV2(ctx, http.MethodPost, endpoint(brokerURL, "/v1/job"), headers, body, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, brokerResponseError(resp)
	}
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return nil, fmt.Errorf("broker: expected stream response")
	}
	if err := readSSE(resp.Body, onEvent); err != nil {
		return nil, err
	}
	merged := resp.Header.Clone()
	for key, values := range resp.Trailer {
		merged[key] = values
	}
	jobID := resp.Header.Get("Livepeer-Job-Id")
	if merged.Get(HeaderSettlement) != "" {
		claim, err := decodeClaim(merged.Get(HeaderSettlement))
		if err != nil {
			return nil, err
		}
		if err = validateJobClaim(claim, auth.RequestID, jobID, expectedWorkUnit, auth.WorkID); err != nil {
			return nil, err
		}
		if err = validateClaimHeaders(claim, merged); err != nil {
			return nil, err
		}
		return claim, nil
	}
	return c.LookupJobV2(ctx, brokerURL, auth.RequestID, jobID, expectedWorkUnit, auth.WorkID)
}
func readSSE(r io.Reader, callback func(SSEEvent) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	event := SSEEvent{}
	var data []string
	dispatch := func() error {
		if len(data) == 0 {
			return nil
		}
		event.Data = json.RawMessage(strings.Join(data, "\n"))
		if callback != nil {
			if err := callback(event); err != nil {
				return err
			}
		}
		event = SSEEvent{}
		data = nil
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		key, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch key {
		case "event":
			event.Event = value
		case "id":
			event.ID = value
		case "data":
			data = append(data, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return dispatch()
}
func (c *HTTPClient) LookupJobV2(ctx context.Context, brokerURL, requestID, expectedJobID, expectedWorkUnit, expectedWorkID string) (*TerminalClaim, error) {
	var body map[string]any
	headers, err := c.jsonV2(ctx, http.MethodGet, endpoint(brokerURL, "/v1/exchange/"+url.PathEscape(requestID)), nil, nil, &body)
	if err != nil {
		return nil, err
	}
	if scalar(body["request_id"]) != requestID {
		return nil, fmt.Errorf("broker: exchange request identity mismatch")
	}
	if scalar(body["outcome"]) != "SETTLED" {
		outcome := scalar(body["outcome"])
		switch outcome {
		case "ADMISSION_REJECTED", "ACCOUNTING_PENDING", "IN_FLIGHT", "ADMITTED_OUTCOME_UNKNOWN", "ADMITTED_EVIDENCE_EXPIRED", "NOT_ADMITTED", "NO_RECORD":
		default:
			outcome = "UNKNOWN"
		}
		return nil, &ExchangePendingError{Outcome: outcome}
	}
	encoded := headers.Get(HeaderSettlement)
	fromBody := scalar(body["settlement"])
	if encoded != "" && fromBody != "" && encoded != fromBody {
		return nil, fmt.Errorf("broker: settlement header/body mismatch")
	}
	if encoded == "" {
		encoded = fromBody
	}
	claim, err := decodeClaim(encoded)
	if err != nil {
		return nil, err
	}
	if err = validateJobClaim(claim, requestID, expectedJobID, expectedWorkUnit, expectedWorkID); err != nil {
		return nil, err
	}
	if scalar(body["job_id"]) != claim.BrokerJobID || scalar(body["unit"]) != claim.WorkUnit || scalar(body["work_units"]) != strconv.FormatInt(claim.ActualUnits, 10) {
		return nil, fmt.Errorf("broker: exchange terminal claim mismatch")
	}
	if err = validateClaimHeaders(claim, headers); err != nil {
		return nil, err
	}
	return claim, nil
}
func validateJobClaim(c *TerminalClaim, requestID, jobID, unit, workID string) error {
	if c.BrokerJobID == "" || c.RequestID != requestID || (jobID != "" && c.BrokerJobID != jobID) || c.WorkUnit != unit || (workID != "" && c.WorkID != workID) {
		return fmt.Errorf("broker: settlement identity or work-unit mismatch")
	}
	return nil
}
func validateClaimHeaders(c *TerminalClaim, h http.Header) error {
	for _, pair := range [][2]string{{"Livepeer-Job-Id", c.BrokerJobID}, {"Livepeer-Work-Unit", c.WorkUnit}, {"Livepeer-Work-Units", strconv.FormatInt(c.ActualUnits, 10)}} {
		if actual := h.Get(pair[0]); actual != "" && actual != pair[1] {
			return fmt.Errorf("broker: settlement conflicts with %s", pair[0])
		}
	}
	return nil
}

// decodeClaim checks the signed envelope structure and identity. LOC verifies
// the broker's signature and accepted quote before accepting any accounting.
func decodeClaim(encoded string) (*TerminalClaim, error) {
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("broker: missing or malformed signed settlement")
	}
	var envelope map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("broker: malformed settlement JSON")
	}
	payload, ok := envelope["payload"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("broker: missing settlement payload")
	}
	sig, ok := envelope["signature"].(map[string]any)
	if !ok || scalar(sig["algorithm"]) != "secp256k1" || scalar(sig["canonicalization"]) != "jcs" {
		return nil, fmt.Errorf("broker: unsigned or unsupported settlement")
	}
	value := scalar(sig["value"])
	signature, err := hex.DecodeString(strings.TrimPrefix(value, "0x"))
	if err != nil || !strings.HasPrefix(value, "0x") || len(signature) != 65 {
		return nil, fmt.Errorf("broker: malformed settlement signature")
	}
	claim := &TerminalClaim{BrokerJobID: scalar(payload["job_id"]), SessionID: scalar(payload["session_id"]), GatewaySessionID: scalar(payload["gateway_session_id"]), RequestID: scalar(payload["request_id"]), WorkID: scalar(payload["work_id"]), WorkUnit: scalar(payload["work_unit_name"]), State: scalar(payload["state"]), Outcome: scalar(payload["outcome"]), Settlement: envelope}
	if breakdown, ok := payload["breakdown"].(map[string]any); ok {
		claim.CloseReason = scalar(breakdown["termination_reason"])
		claim.OutputState = scalar(breakdown["output_state"])
		claim.FailureCode = scalar(breakdown["last_failure_code"])
	}
	if authorizationID := scalar(payload["authorization_id"]); authorizationID != "" && claim.WorkID != "" && authorizationID != claim.WorkID {
		return nil, fmt.Errorf("broker: inconsistent settlement authorization identity")
	}
	if claim.WorkID == "" {
		claim.WorkID = scalar(payload["authorization_id"])
	}
	unitKey := "actual_units"
	if claim.SessionID != "" {
		unitKey = "debited_units"
	}
	units := scalar(payload[unitKey])
	if units == "" {
		units = "0"
	} // protobuf omits zero scalar fields
	claim.ActualUnits, err = strconv.ParseInt(units, 10, 64)
	if err != nil || claim.ActualUnits < 0 || claim.WorkUnit == "" || claim.WorkID == "" {
		return nil, fmt.Errorf("broker: invalid settlement units or identity")
	}
	return claim, nil
}
func scalar(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case json.Number:
		return s.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

type SessionBalanceV2 struct {
	Status                         string `json:"status"`
	ClaimedUnits                   int64  `json:"claimed_units"`
	DebitedUnits                   int64  `json:"debited_units"`
	Unit                           string `json:"unit"`
	RunwayUnits                    int64  `json:"runway_units"`
	RunwaySecondsEstimate          *int64 `json:"runway_seconds_estimate"`
	AuthorizationMaxUnits          int64  `json:"authorization_max_units"`
	AuthorizationCapRemainingUnits int64  `json:"authorization_cap_remaining_units"`
	WillRefuseNextRefill           bool   `json:"will_refuse_next_refill"`
}
type RuntimeGrantV2 struct {
	ID         string   `json:"id"`
	Operations []string `json:"operations"`
	Secret     string   `json:"secret"`
	ExpiresAt  string   `json:"expires_at"`
}
type RuntimeV2 struct {
	Schema string           `json:"schema"`
	Public map[string]any   `json:"public"`
	Grants []RuntimeGrantV2 `json:"grants,omitempty"`
}
type BrokerSessionV2 struct {
	SessionID        string           `json:"session_id"`
	GatewaySessionID string           `json:"gateway_session_id"`
	WorkID           string           `json:"work_id"`
	State            string           `json:"state"`
	Credential       string           `json:"credential,omitempty"`
	Runtime          RuntimeV2        `json:"runtime"`
	Balance          SessionBalanceV2 `json:"balance"`
	Lease            struct {
		ExpiresAt string `json:"expires_at"`
	} `json:"lease"`
	Usage struct {
		Unit         string `json:"unit"`
		ClaimedTotal int64  `json:"claimed_total"`
	} `json:"usage"`
	Control         LiveControl `json:"control"`
	OutputState     string      `json:"output_state"`
	LastFailureCode string      `json:"last_failure_code"`
	CloseReason     string      `json:"close_reason"`
	StartedAt       string      `json:"started_at"`
	EndedAt         string      `json:"ended_at"`
}

func (c *HTTPClient) OpenSessionV2(ctx context.Context, brokerURL string, auth BrokerAuth, body []byte) (*BrokerSessionV2, error) {
	headers, err := auth.headers(ProtocolSessionV1)
	if err != nil {
		return nil, err
	}
	var out BrokerSessionV2
	if _, err = c.jsonV2(ctx, http.MethodPost, endpoint(brokerURL, "/v1/session"), headers, body, &out); err != nil {
		return nil, err
	}
	if out.SessionID == "" || out.Credential == "" || out.WorkID != auth.WorkID || out.Runtime.Schema == "" {
		return nil, fmt.Errorf("broker: incomplete session or mismatched work identity")
	}
	return &out, nil
}
func credentialHeader(credential string) (http.Header, error) {
	if credential == "" {
		return nil, fmt.Errorf("broker: session credential required")
	}
	return http.Header{"Authorization": []string{"Bearer " + credential}}, nil
}
func (c *HTTPClient) StatusSessionV2(ctx context.Context, brokerURL, id, credential string) (*BrokerSessionV2, error) {
	h, err := credentialHeader(credential)
	if err != nil {
		return nil, err
	}
	var out BrokerSessionV2
	if _, err = c.jsonV2(ctx, http.MethodGet, endpoint(brokerURL, "/v1/session/"+url.PathEscape(id)), h, nil, &out); err != nil {
		return nil, err
	}
	if out.SessionID != id {
		return nil, fmt.Errorf("broker: session identity mismatch")
	}
	return &out, nil
}
func (c *HTTPClient) TopUpSessionV2(ctx context.Context, brokerURL, id, credential string, auth BrokerAuth, body []byte) (*BrokerSessionV2, error) {
	h, err := auth.headers(ProtocolSessionV1)
	if err != nil {
		return nil, err
	}
	if credential == "" {
		return nil, fmt.Errorf("broker: session credential required")
	}
	h.Set("Authorization", "Bearer "+credential)
	var out BrokerSessionV2
	if _, err = c.jsonV2(ctx, http.MethodPost, endpoint(brokerURL, "/v1/session/"+url.PathEscape(id)+"/topup"), h, body, &out); err != nil {
		return nil, err
	}
	if out.SessionID != id {
		return nil, fmt.Errorf("broker: session identity mismatch")
	}
	return &out, nil
}
func (c *HTTPClient) EndSessionV2(ctx context.Context, brokerURL, id, credential, reason string) (*BrokerSessionV2, error) {
	h, err := credentialHeader(credential)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]string{"reason": reason})
	var out BrokerSessionV2
	if _, err = c.jsonV2(ctx, http.MethodPost, endpoint(brokerURL, "/v1/session/"+url.PathEscape(id)+"/end"), h, body, &out); err != nil {
		return nil, err
	}
	if out.SessionID != id {
		return nil, fmt.Errorf("broker: session identity mismatch")
	}
	return &out, nil
}
func (c *HTTPClient) LookupSessionSettlementV2(ctx context.Context, brokerURL, id, expectedWorkID, expectedWorkUnit string) (*TerminalClaim, error) {
	var body map[string]any
	headers, err := c.jsonV2(ctx, http.MethodGet, endpoint(brokerURL, "/v1/settlement/"+url.PathEscape(id)), nil, nil, &body)
	if err != nil {
		return nil, err
	}
	claim, err := decodeClaim(headers.Get(HeaderSettlement))
	if err != nil {
		return nil, err
	}
	if claim.State != "closed" {
		return nil, ErrAccountingPending
	}
	if (claim.SessionID != id && claim.GatewaySessionID != id) || claim.WorkUnit != expectedWorkUnit || (expectedWorkID != "" && claim.WorkID != expectedWorkID) {
		return nil, fmt.Errorf("broker: session settlement identity mismatch")
	}
	return claim, nil
}

// IssueRuntimeKeyV2 is workload-neutral: the caller selects an advertised
// grant operation and forwards the workload-specific request as exact JSON.
func (c *HTTPClient) IssueRuntimeKeyV2(ctx context.Context, target, secret string, body []byte, out any) error {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("broker: invalid runtime grant endpoint")
	}
	h, err := credentialHeader(secret)
	if err != nil {
		return err
	}
	_, err = c.jsonV2(ctx, http.MethodPost, target, h, body, out)
	return err
}

// SessionOpenOutcomeV2 is lifecycle evidence, not authority to settle or refund.
type SessionOpenOutcomeV2 struct {
	RequestID        string `json:"request_id"`
	SessionID        string `json:"session_id"`
	GatewaySessionID string `json:"gateway_session_id"`
	Outcome          string `json:"outcome"`
}

func (c *HTTPClient) LookupSessionOpenV2(ctx context.Context, brokerURL, requestID, gatewayID string) (*SessionOpenOutcomeV2, error) {
	var out SessionOpenOutcomeV2
	_, err := c.jsonV2(ctx, http.MethodGet, endpoint(brokerURL, "/v1/exchange/"+url.PathEscape(requestID)), nil, nil, &out)
	if err != nil {
		return nil, err
	}
	if out.RequestID != requestID || (out.SessionID != "" && out.GatewaySessionID != gatewayID) {
		return nil, fmt.Errorf("broker: session exchange identity mismatch")
	}
	switch out.Outcome {
	case "ADMISSION_REJECTED", "NOT_ADMITTED":
		if out.SessionID != "" {
			return nil, fmt.Errorf("broker: contradictory session admission outcome")
		}
	case "IN_FLIGHT", "ACCOUNTING_PENDING", "SETTLED", "NO_RECORD", "ADMITTED_OUTCOME_UNKNOWN", "ADMITTED_EVIDENCE_EXPIRED":
	default:
		return nil, ErrAccountingPending
	}
	return &out, nil
}
