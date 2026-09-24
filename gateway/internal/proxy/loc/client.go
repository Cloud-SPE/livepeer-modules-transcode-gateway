// Package loc talks to the Livepeer Open Clearinghouse. LOC selects a route
// and issues a workload-scoped spend authorization; the gateway invokes the
// broker directly and forwards signed terminal accounting evidence to LOC.
// v2.go carries the current contract. Issuance uses persisted idempotency keys.
package loc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to one LOC deployment with one API key. Nil-safe by
// convention: a nil *Client means "LOC not configured" and handlers
// return 503.
type Client struct {
	httpc   *http.Client
	baseURL string
	apiKey  string
	sdkID   string
}

// NewClient builds a Client. baseURL is e.g. https://loc.cloudspe.com
// (no trailing slash needed); apiKey is the operator-issued pymth_ key.
// sdkID is sent as the Livepeer-Open-Clearinghouse-SDK identity header
// ({name}/{version}/{sha} per the LOC trust-scoring convention).
func NewClient(baseURL, apiKey, sdkID string, timeout time.Duration) *Client {
	if baseURL == "" || apiKey == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		httpc:   &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		sdkID:   sdkID,
	}
}

// doJSON executes one LOC request. On >=400 it decodes LOC's error
// envelope {"error":{code,message,details}} into *APIError (falling
// back to the raw body when the envelope doesn't parse).
func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	return c.doJSONHeaders(ctx, method, path, body, out, nil)
}

func (c *Client) doJSONHeaders(ctx context.Context, method, path string, body, out any, headers http.Header) error {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("loc: marshal %s %s: %w", method, path, err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, values := range headers {
		req.Header[key] = values
	}
	req.Header.Set("X-API-Key", c.apiKey)
	if c.sdkID != "" {
		req.Header.Set("Livepeer-Open-Clearinghouse-SDK", c.sdkID)
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAPIError(resp.StatusCode, resp.Header.Get("Retry-After"), respBody)
	}
	if out != nil && len(respBody) > 0 {
		dec := json.NewDecoder(bytes.NewReader(respBody))
		dec.UseNumber() // wei amounts can exceed int64; Wei decodes via json.Number
		if err := dec.Decode(out); err != nil {
			return fmt.Errorf("loc: decode %s %s: %w (body: %s)",
				method, path, err, truncate(respBody, 200))
		}
	}
	return nil
}

// doJSONRetry retries idempotent accounting operations on 429/5xx/transport
// failures. Issuance/revision retries are owned by the durable operation worker,
// which reuses the original request body and Idempotency-Key.
func (c *Client) doJSONRetry(ctx context.Context, method, path string, body, out any, attempts int) error {
	backoff := 200 * time.Millisecond
	var err error
	for i := 0; i < attempts; i++ {
		err = c.doJSON(ctx, method, path, body, out)
		if err == nil || !IsRetryable(err) {
			return err
		}
		if i == attempts-1 {
			break
		}
		wait := backoff
		if ra := retryAfterOf(err); ra > 0 {
			wait = ra
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		backoff *= 2
	}
	return err
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
