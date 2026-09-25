package livepeer

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// BrokerError carries an upstream broker response that we treat as terminal.
type BrokerError struct {
	URL        string
	StatusCode int
	Body       string
}

func (e *BrokerError) Error() string {
	return fmt.Sprintf("broker %s: status=%d body=%s", e.URL, e.StatusCode, e.Body)
}

// IsRetryable reports whether err warrants trying the next candidate.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var be *BrokerError
	if errors.As(err, &be) {
		return be.StatusCode == 429 || (be.StatusCode >= 500 && be.StatusCode <= 599)
	}
	// transport / timeout / DNS errors are retryable
	return true
}

// IsInvalidRecipientRandError reports whether err is the broker's
// signal that the receiver's payment session has rotated and the
// caller needs a freshly-minted envelope (a new LOC job / refill).
//
// v1.3.1 capability-broker shape:
//
//	HTTP 401  +  Livepeer-Error: payment_invalid
//	body contains "INVALID_RECIPIENT_RAND"
//
// We match on body substring because that's the only field carrying
// the rejection reason; the Livepeer-Error header is `payment_invalid`
// for several other 401 cases too (missing header, malformed payment,
// bad sender) that don't warrant a retry — only INVALID_RECIPIENT_RAND
// is recoverable.
func IsInvalidRecipientRandError(err error) bool {
	if err == nil {
		return false
	}
	var be *BrokerError
	if !errors.As(err, &be) {
		return false
	}
	if be.StatusCode != 401 {
		return false
	}
	return strings.Contains(be.Body, "INVALID_RECIPIENT_RAND")
}

// IsRefillRefusedError matches only the broker's durable, definitive rejection.
// A generic 409 or a transport failure may hide an accepted successor.
func IsRefillRefusedError(err error) bool {
	var be *BrokerError
	if !errors.As(err, &be) || be.StatusCode != 409 {
		return false
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal([]byte(be.Body), &body) == nil && body.Error.Code == "refill_refused"
}
