package livepeer

import (
	"errors"
	"fmt"
	"strings"
)

// ErrPayerUnavailable is returned when the payer-daemon socket isn't reachable.
var ErrPayerUnavailable = errors.New("payer-daemon unavailable")

// ErrResolverUnavailable is returned when the resolver socket isn't reachable.
var ErrResolverUnavailable = errors.New("resolver daemon unavailable")

// ErrNoCapableBroker is returned when the resolver returns zero candidates.
var ErrNoCapableBroker = errors.New("no capable broker found")

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
// signal that the receiver's session has rotated and we need to evict
// our payer-daemon cache + re-mint the payment.
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
