package livepeer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPClient is the broker call surface used by handlers. Default
// `http.DefaultClient` with a 30s timeout is fine for VOD dispatch.
type HTTPClient struct {
	*http.Client
}

func NewHTTPClient(timeout time.Duration) *HTTPClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &HTTPClient{Client: &http.Client{Timeout: timeout}}
}

// PostJSON dispatches a JSON-bodied request to a broker endpoint
// using http-reqresp@v0 conventions. The caller picks the URL —
// http-reqresp jobs target the broker's /v1/cap endpoint (which routes
// by Livepeer-Capability header); long-lived session modes use their
// own endpoints.
// Returns the decoded JSON response on 2xx, a *BrokerError on 4xx/5xx,
// or a transport error.
func (c *HTTPClient) PostJSON(
	ctx context.Context,
	url, capability, offering, requestID string,
	payment []byte,
	body any,
	out any,
) error {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("http-reqresp: marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("http-reqresp: build req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	SetWireHeaders(req.Header, capability, offering, ModeHTTPReqResp, requestID, payment)
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	bodyResp, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return &BrokerError{URL: url, StatusCode: resp.StatusCode, Body: string(bodyResp)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(bodyResp, out); err != nil {
		return fmt.Errorf("http-reqresp: decode response: %w", err)
	}
	return nil
}
