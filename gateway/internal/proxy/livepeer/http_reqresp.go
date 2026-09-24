package livepeer

import (
	"net/http"
	"time"
)

// HTTPClient handles scoped broker invocations and credential-authenticated controls.
type HTTPClient struct{ *http.Client }

func NewHTTPClient(timeout time.Duration) *HTTPClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &HTTPClient{Client: &http.Client{Timeout: timeout}}
}
