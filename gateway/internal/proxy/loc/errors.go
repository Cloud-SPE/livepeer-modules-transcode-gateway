package loc

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// APIError wraps LOC's error envelope:
//
//	{"error": {"code": "...", "message": "...", "details": {...}}}
//
// LOC's code casing is mixed — macro errors are UPPER
// ("INSUFFICIENT_CREDIT") while domain errors are lower
// ("job_already_settled") — so all matchers compare case-insensitively.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	Details    map[string]any
	// RetryAfter is the parsed Retry-After header, zero when absent.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("loc: %s (%s, http %d)", e.Message, e.Code, e.StatusCode)
	}
	return fmt.Sprintf("loc: %s (http %d)", e.Message, e.StatusCode)
}

// parseAPIError builds an *APIError from a non-2xx LOC response,
// tolerating bodies that don't carry the canonical envelope (proxies,
// FastAPI validation errors, etc.).
func parseAPIError(status int, retryAfter string, body []byte) *APIError {
	out := &APIError{StatusCode: status, Message: fmt.Sprintf("HTTP %d", status)}
	if secs, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && secs > 0 {
		out.RetryAfter = time.Duration(secs) * time.Second
	}
	var envelope struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil &&
		(envelope.Error.Code != "" || envelope.Error.Message != "") {
		out.Code = envelope.Error.Code
		if envelope.Error.Message != "" {
			out.Message = envelope.Error.Message
		}
		out.Details = envelope.Error.Details
		return out
	}
	if len(body) > 0 {
		out.Message = truncate(body, 200)
	}
	return out
}

func codeIs(err error, codes ...string) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	for _, c := range codes {
		if strings.EqualFold(ae.Code, c) {
			return true
		}
	}
	return false
}

// IsInsufficientCredit reports whether LOC refused because the
// operator-granted credit pool can't cover the worst-case encumbrance
// (balance, spend-period cap, or session cap).
func IsInsufficientCredit(err error) bool {
	return codeIs(err, "INSUFFICIENT_CREDIT", "SPEND_CAP_EXCEEDED", "cap_reached")
}

// CreditError classifies a credit refusal (the family IsInsufficientCredit
// matches) into a stable client-facing code and an actionable message.
// Callers gate on IsInsufficientCredit(err) first; for any other error the
// generic insufficient-credit wording is returned. A Retry-After hint is
// appended when LOC supplied one.
func CreditError(err error) (code, message string) {
	code = "insufficient_credit"
	message = "Payment required: your prepaid credit balance can't cover this " +
		"session (reserved = price × runway). Top up your credit balance and retry."

	var ae *APIError
	if errors.As(err, &ae) {
		switch {
		case strings.EqualFold(ae.Code, "SPEND_CAP_EXCEEDED"):
			code = "spend_cap_exceeded"
			message = "Payment required: the spend cap for the current period has " +
				"been reached. Raise the per-period spend cap or wait for the next period."
		case strings.EqualFold(ae.Code, "cap_reached"):
			code = "session_cap_reached"
			message = "Payment required: the session/credit cap has been reached. " +
				"Raise the cap or wait for it to reset."
		}
		if ae.RetryAfter > 0 {
			message += fmt.Sprintf(" Retry after ~%ds.", int(ae.RetryAfter.Seconds()))
		}
	}
	return code, message
}

// IsNoRoute reports whether no orchestrator currently advertises the
// requested capability+offering.
func IsNoRoute(err error) bool {
	return codeIs(err, "NO_ROUTE_AVAILABLE")
}

// IsNotFound reports an unknown (or other-owner) job/session id.
func IsNotFound(err error) bool {
	return codeIs(err, "job_not_found", "session_not_found")
}

// IsAlreadySettled reports a duplicate settle/close. Callers treat this
// as success — the first settle stuck.
func IsAlreadySettled(err error) bool {
	return codeIs(err, "job_already_settled", "session_not_open")
}

// IsDaemonUnavailable reports that LOC couldn't reach its own
// payment/registry daemons.
func IsDaemonUnavailable(err error) bool {
	return codeIs(err, "DAEMON_UNAVAILABLE")
}

// IsRetryable reports whether the call may be re-issued: rate limits,
// LOC-side 5xx, and transport errors. 4xx (other than 429) are terminal.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.StatusCode == 429 || (ae.StatusCode >= 500 && ae.StatusCode <= 599)
	}
	// transport / timeout / DNS errors
	return true
}

func retryAfterOf(err error) time.Duration {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.RetryAfter
	}
	return 0
}
