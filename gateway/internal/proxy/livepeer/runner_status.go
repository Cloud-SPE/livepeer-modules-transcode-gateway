package livepeer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ABRRunnerStatus mirrors the abr-runner's status response. Field names
// follow the runner's JSON shape — leave them stable so unmarshaling
// stays loose (extra runner fields are ignored).
type ABRRunnerStatus struct {
	JobID           string                  `json:"job_id"`
	Status          string                  `json:"status"`
	Phase           string                  `json:"phase"`
	OverallProgress float64                 `json:"overall_progress"`
	ManifestURL     string                  `json:"manifest_url,omitempty"`
	Renditions      []ABRRunnerRendition    `json:"renditions,omitempty"`
	Error           string                  `json:"error,omitempty"`
	ErrorCode       string                  `json:"error_code,omitempty"`
	GPU             string                  `json:"gpu,omitempty"`
	CreatedAt       string                  `json:"created_at,omitempty"`
	CompletedAt     string                  `json:"completed_at,omitempty"`
}

type ABRRunnerRendition struct {
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	Progress    float64 `json:"progress"`
	EncodingFPS float64 `json:"encoding_fps,omitempty"`
	Speed       string  `json:"speed,omitempty"`
	Bitrate     int     `json:"bitrate,omitempty"`
	FileSize    int64   `json:"file_size,omitempty"`
}

// QueryABRRunnerStatus asks the runner (via its broker frontend) for
// the live state of a job. brokerURL is the worker_url from the
// registry; jobID is what the runner returned at submit time.
//
// The status endpoint isn't a paid /v1/cap call — the broker forwards
// it as a plain pass-through. If your broker happens to gate this path,
// the call returns *BrokerError 4xx/5xx and the caller falls back to
// the master.m3u8 HEAD-probe.
func (c *HTTPClient) QueryABRRunnerStatus(ctx context.Context, brokerURL, jobID string) (*ABRRunnerStatus, error) {
	if c == nil {
		c = NewHTTPClient(0)
	}
	url := strings.TrimRight(brokerURL, "/") + "/v1/video/transcode/abr/status"
	body, err := json.Marshal(map[string]string{"job_id": jobID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderSpecVersion, SpecVersion)
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &BrokerError{URL: url, StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	var out ABRRunnerStatus
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode runner status: %w (body: %s)", err, truncate(string(respBody), 200))
	}
	return &out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
