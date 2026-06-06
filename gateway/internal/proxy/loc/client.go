// Package loc is the gateway's HTTP client for the Livepeer Open
// Clearinghouse (LOC). LOC fronts the service-registry and payment
// daemons behind a jobs/sessions API: the gateway asks LOC to mint a
// payment (LOC also picks the route), attaches the returned envelope to
// its own broker call, then settles actual work units back to LOC.
//
// Wire contract source of truth: basic-pymnthouse
// src/livepeer_open_clearinghouse/domains/{jobs,sessions,discovery}.
// Conventions here mirror proxy/livepeer's doJSON so error handling
// stays consistent across upstream clients.
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
		httpc:   &http.Client{Timeout: timeout},
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		sdkID:   sdkID,
	}
}

// doJSON executes one LOC request. On >=400 it decodes LOC's error
// envelope {"error":{code,message,details}} into *APIError (falling
// back to the raw body when the envelope doesn't parse).
func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
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
	req.Header.Set("X-API-Key", c.apiKey)
	if c.sdkID != "" {
		req.Header.Set("Livepeer-Open-Clearinghouse-SDK", c.sdkID)
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
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

// doJSONRetry wraps doJSON with the settle-path retry policy: up to
// `attempts` total tries on 429/5xx/transport errors, honoring
// Retry-After, with capped exponential backoff. NEVER use this for
// create/open calls — LOC has no idempotency keys on jobs/sessions, so
// a retried create can double-encumber credit. Settle/close/refill are
// safe: a duplicate settle returns 409 job_already_settled, which
// callers treat as success.
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
