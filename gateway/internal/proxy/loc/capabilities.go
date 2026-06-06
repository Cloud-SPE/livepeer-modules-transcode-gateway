package loc

import (
	"context"
	"net/http"
	"net/url"
)

// ListCapabilities returns the network capability catalog (names,
// offerings, prices). Read-only; no charge.
func (c *Client) ListCapabilities(ctx context.Context) ([]Capability, error) {
	var out struct {
		Items []Capability `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/capabilities", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// ListOrchestrators returns the orchestrator catalog; pass capability=""
// for the full list. Read-only; no charge.
func (c *Client) ListOrchestrators(ctx context.Context, capability string) ([]Orchestrator, error) {
	path := "/v1/orchestrators"
	if capability != "" {
		path += "?capability=" + url.QueryEscape(capability)
	}
	var out struct {
		Items []Orchestrator `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Healthy probes LOC reachability cheaply via the capability catalog.
// Used by /health; returns the underlying error for surfacing.
func (c *Client) Healthy(ctx context.Context) error {
	_, err := c.ListCapabilities(ctx)
	return err
}
