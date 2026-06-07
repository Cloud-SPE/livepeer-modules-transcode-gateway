package loc

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// OpenSession opens a long-running refillable session (case d). Same
// no-retry rationale as CreateJob: opens are not idempotent.
func (c *Client) OpenSession(ctx context.Context, in CreateSessionRequest) (*CreateSessionResponse, error) {
	var out CreateSessionResponse
	if err := c.doJSON(ctx, http.MethodPost, "/v1/sessions", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RefillSession mints a top-up pinned to the session's original broker
// (LOC reuses the daemon session cache key). One retry on 429/5xx — a
// duplicate refill mints an extra top-up, undesirable but bounded.
func (c *Client) RefillSession(ctx context.Context, sessionID uuid.UUID, in RefillSessionRequest) (*RefillSessionResponse, error) {
	var out RefillSessionResponse
	if err := c.doJSONRetry(ctx, http.MethodPost, "/v1/sessions/"+sessionID.String()+"/refill", in, &out, 2); err != nil {
		return nil, err
	}
	return &out, nil
}

// CloseSession settles the session. Retried like SettleJob; a 409 /
// session_not_open means an earlier close stuck (IsAlreadySettled).
func (c *Client) CloseSession(ctx context.Context, sessionID uuid.UUID, in CloseSessionRequest) (*CloseSessionResponse, error) {
	var out CloseSessionResponse
	if err := c.doJSONRetry(ctx, http.MethodPost, "/v1/sessions/"+sessionID.String()+"/close", in, &out, 3); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetSession reads the session snapshot (state, totals, cap status).
func (c *Client) GetSession(ctx context.Context, sessionID uuid.UUID) (*SessionStatusResponse, error) {
	var out SessionStatusResponse
	if err := c.doJSON(ctx, http.MethodGet, "/v1/sessions/"+sessionID.String(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
