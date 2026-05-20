package livepeer

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// RTMPSession captures the broker's response to an OpenSession call
// for the rtmp-ingress-hls-egress@v0 mode.
type RTMPSession struct {
	SessionID  string `json:"session_id"`
	RTMPURL    string `json:"rtmp_url"`
	StreamKey  string `json:"stream_key"`
	HLSURL     string `json:"hls_url"`
}

// OpenLiveSession asks the broker to allocate an RTMP ingest +
// HLS egress session. The broker is expected to expose
// POST <workerURL>/v1/session with the standard Livepeer-* headers.
func OpenLiveSession(
	ctx context.Context,
	c *HTTPClient,
	brokerURL, capability, offering, requestID string,
	payment []byte,
	ladderJSON json.RawMessage,
) (*RTMPSession, error) {
	if c == nil {
		c = NewHTTPClient(15 * time.Second)
	}
	body := map[string]any{
		"mode":   ModeRTMPIngressHLSOut,
		"ladder": json.RawMessage(ladderJSON),
	}
	var sess RTMPSession
	if err := c.PostJSON(ctx, brokerURL+"/v1/session", capability, offering, requestID, payment, body, &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

// CloseLiveSession tells the broker to tear down an ingest session.
func CloseLiveSession(ctx context.Context, c *HTTPClient, brokerURL, sessionID string) error {
	if c == nil {
		c = NewHTTPClient(15 * time.Second)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, brokerURL+"/v1/session/"+sessionID, nil)
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return &BrokerError{URL: brokerURL, StatusCode: resp.StatusCode}
	}
	return nil
}
