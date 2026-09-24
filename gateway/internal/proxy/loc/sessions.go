package loc

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// GetSession reads the session snapshot (state, totals, cap status).
func (c *Client) GetSession(ctx context.Context, sessionID uuid.UUID) (*SessionStatusResponse, error) {
	var out SessionStatusResponse
	if err := c.doJSON(ctx, http.MethodGet, "/v1/sessions/"+sessionID.String(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
