package livepeer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Wire types for the live-session-remote-runner@v0 mode. See
// livepeer-network-protocol/modes/live-session-remote-runner.md for the
// authoritative spec. Field names mirror the JSON wire shape exactly.

// LiveLadderRung is one rendition spec in the session-open request body.
type LiveLadderRung struct {
	Name        string `json:"name"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	BitrateKbps int    `json:"bitrate_kbps,omitempty"`
	Passthrough bool   `json:"passthrough,omitempty"`
}

type LiveLadder struct {
	Rungs []LiveLadderRung `json:"rungs"`
}

// LiveOpenParams is the `session_params` block of the session-open body.
type LiveOpenParams struct {
	Name               string            `json:"name,omitempty"`
	Preset             string            `json:"preset,omitempty"`
	Ladder             *LiveLadder       `json:"ladder,omitempty"`
	IdleTimeoutSeconds int               `json:"idle_timeout_seconds,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
}

// LiveMediaIngest is the inbound RTMP coordinates. stream_key is returned
// once at open time only; the broker omits it on GET responses.
type LiveMediaIngest struct {
	RTMPURL   string `json:"rtmp_url"`
	StreamKey string `json:"stream_key,omitempty"`
}

type LiveMediaPlayback struct {
	HLSURL string `json:"hls_url"`
}

type LiveMedia struct {
	Ingest   LiveMediaIngest   `json:"ingest"`
	Playback LiveMediaPlayback `json:"playback"`
}

// LiveControl is the absolute URLs the broker uses for top-up / status /
// end. We don't have to honour these (the spec also lets us compose them
// from broker_session_id + the worker URL we already know) but echoing
// them when calling makes the audit trail explicit.
type LiveControl struct {
	TopupURL  string `json:"topup_url"`
	StatusURL string `json:"status_url"`
	EndURL    string `json:"end_url"`
}

// LiveTopUpRequest is the POST /v1/cap/{bsess}/topup body. Just the
// gateway's view of which session is being topped up — the broker
// already knows broker_session_id from the URL path.
type LiveTopUpRequest struct {
	GatewaySessionID uuid.UUID `json:"gateway_session_id"`
}

// LiveTopUpBalance is the inline balance summary the broker emits after
// crediting a top-up.
type LiveTopUpBalance struct {
	Status                string `json:"status"`
	RunwaySecondsEstimate int    `json:"runway_seconds_estimate"`
}

type LiveTopUpResponse struct {
	BrokerSessionID string           `json:"broker_session_id"`
	WorkID          uuid.UUID        `json:"work_id"`
	State           string           `json:"state"`
	Balance         LiveTopUpBalance `json:"balance"`
}

// LiveGetResponse is GET /v1/cap/{bsess}. Stream key MUST NOT appear
// in this shape — the spec is explicit.
type LiveGetResponse struct {
	GatewaySessionID uuid.UUID `json:"gateway_session_id"`
	BrokerSessionID  string    `json:"broker_session_id"`
	RunnerSessionID  string    `json:"runner_session_id"`
	WorkID           uuid.UUID `json:"work_id"`
	State            string    `json:"state"`
	Media            LiveMedia `json:"media"`
	StartedAt        *string   `json:"started_at"`
	LastHeartbeatAt  *string   `json:"last_heartbeat_at"`
	EndedAt          *string   `json:"ended_at"`
	CloseReason      *string   `json:"close_reason"`
	// Balance is undocumented in the GET response spec today but the
	// broker emits it on top-up; reconciler reads it opportunistically
	// when present so we can estimate runway between top-ups too.
	Balance *LiveTopUpBalance `json:"balance,omitempty"`
}

// LiveEndRequest is the POST /v1/cap/{bsess}/end body. The reason is
// stored as-is by the broker; the gateway sends "gateway_close" for
// customer-initiated DELETE.
type LiveEndRequest struct {
	Reason string `json:"reason"`
}

type LiveEndResponse struct {
	BrokerSessionID string `json:"broker_session_id"`
	RunnerSessionID string `json:"runner_session_id"`
	State           string `json:"state"`
	CloseReason     string `json:"close_reason"`
	EndedAt         string `json:"ended_at"`
}

// ── live-session-gateway-ingest@v0 wire types (plan 0003) ──
//
// Differs from live-session-remote-runner@v0 in three places:
//   - request body carries `output_credential` (S3 write creds for runner)
//     and `ingest_accept.stream_key` (so runner can authenticate the
//     gateway's upstream RTMP push)
//   - response carries `private_ingest_url` (where the gateway pushes RTMP)
//     and omits the customer-facing media block (gateway owns those)

type LiveOutputCredential struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	KeyPrefix       string `json:"key_prefix"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	// SessionToken is always forwarded so temporary S3 credentials include
	// the x-amz-security-token header during runner-side SigV4 signing.
	SessionToken string `json:"session_token"`
	ExpiresAt    string `json:"expires_at"`
}

type LiveIngestAccept struct {
	StreamKey string `json:"stream_key"`
}

type LiveOpenGatewayIngestRequest struct {
	GatewaySessionID uuid.UUID            `json:"gateway_session_id"`
	SessionParams    LiveOpenParams       `json:"session_params"`
	OutputCredential LiveOutputCredential `json:"output_credential"`
	IngestAccept     LiveIngestAccept     `json:"ingest_accept"`
}

type LiveOpenGatewayIngestResponse struct {
	BrokerSessionID  string      `json:"broker_session_id"`
	RunnerSessionID  string      `json:"runner_session_id"`
	WorkID           uuid.UUID   `json:"work_id"`
	State            string      `json:"state"`
	PrivateIngestURL string      `json:"private_ingest_url"`
	Control          LiveControl `json:"control"`
	ExpiresAt        string      `json:"expires_at,omitempty"`
}

// OpenLiveSessionGatewayIngest executes POST /v1/cap with the new mode
// header + body shape. Returns *BrokerError on 4xx/5xx so the standard
// rotation-retry / failover matchers still work.
func (c *HTTPClient) OpenLiveSessionGatewayIngest(
	ctx context.Context,
	brokerURL, capability, offering, requestID string,
	payment []byte,
	body LiveOpenGatewayIngestRequest,
) (*LiveOpenGatewayIngestResponse, error) {
	url := strings.TrimRight(brokerURL, "/") + "/v1/cap"
	var out LiveOpenGatewayIngestResponse
	if err := c.doJSON(ctx, http.MethodPost, url,
		ModeLiveSessionGatewayIngest, capability, offering, requestID, payment, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Canonical state values the broker emits per the spec. The gateway
// maps these onto LiveStreamStatus in the handlers.
const (
	LiveStateProvisioning = "provisioning"
	LiveStateReady        = "ready"
	LiveStatePublishing   = "publishing"
	LiveStateEnding       = "ending"
	LiveStateEnded        = "ended"
	LiveStateFailed       = "failed"
)

// Canonical close-reason strings the broker emits. The gateway persists
// the broker's value as-is; this list is for reference only.
const (
	LiveCloseGatewayClose       = "gateway_close"
	LiveCloseInsufficientFunds  = "insufficient_balance"
	LiveCloseRunnerFailed       = "runner_failed"
	LiveCloseIdleTimeout        = "idle_timeout"
)

// TopUpLiveSession executes POST /v1/cap/{bsess}/topup. The spec lists
// only Content-Type / Request-Id / Payment as required; we also send
// Capability + Offering as defensive compatibility headers (per the
// runner team's guidance).
func (c *HTTPClient) TopUpLiveSession(
	ctx context.Context,
	brokerURL, brokerSessionID, capability, offering, requestID string,
	payment []byte,
	body LiveTopUpRequest,
) (*LiveTopUpResponse, error) {
	url := strings.TrimRight(brokerURL, "/") + "/v1/cap/" + brokerSessionID + "/topup"
	var out LiveTopUpResponse
	if err := c.doJSON(ctx, http.MethodPost, url,
		"", capability, offering, requestID, payment, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetLiveSession executes GET /v1/cap/{bsess}. No payment / no mode
// header — the broker serves this from persisted session state.
func (c *HTTPClient) GetLiveSession(ctx context.Context, brokerURL, brokerSessionID string) (*LiveGetResponse, error) {
	url := strings.TrimRight(brokerURL, "/") + "/v1/cap/" + brokerSessionID
	var out LiveGetResponse
	if err := c.doJSON(ctx, http.MethodGet, url, "", "", "", "", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EndLiveSession executes POST /v1/cap/{bsess}/end. Idempotent on the
// broker side — calling twice is safe.
func (c *HTTPClient) EndLiveSession(ctx context.Context, brokerURL, brokerSessionID, requestID, reason string) (*LiveEndResponse, error) {
	url := strings.TrimRight(brokerURL, "/") + "/v1/cap/" + brokerSessionID + "/end"
	body := LiveEndRequest{Reason: reason}
	var out LiveEndResponse
	if err := c.doJSON(ctx, http.MethodPost, url, "", "", "", requestID, nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// doJSON is a small helper that handles the body marshal / status check /
// response decode for the four live endpoints. Centralised so all four
// callers produce *BrokerError on 4xx/5xx the same way the existing
// PostJSON does — keeps IsInvalidRecipientRandError + IsRetryable
// detection consistent.
func (c *HTTPClient) doJSON(
	ctx context.Context,
	method, url, mode, capability, offering, requestID string,
	payment []byte,
	body any,
	out any,
) error {
	if c == nil {
		c = NewHTTPClient(0)
	}
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("live: marshal %s: %w", method, err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	SetWireHeaders(req.Header, capability, offering, mode, requestID, payment)
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return &BrokerError{URL: url, StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("live: decode %s %s: %w (body: %s)",
				method, url, err, truncateBody(respBody, 200))
		}
	}
	return nil
}

func truncateBody(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// _ = time.Time is here to keep the import non-dead even when no caller
// references time directly inside this file. Future helpers (e.g. an
// ExpiresAt parser) will use it.
var _ = time.Time{}
