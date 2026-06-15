package loc

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCreditError(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantCode  string
		wantInMsg string
	}{
		{
			name:      "insufficient credit",
			err:       &APIError{StatusCode: 402, Code: "INSUFFICIENT_CREDIT", Message: "balance too low"},
			wantCode:  "insufficient_credit",
			wantInMsg: "Top up your credit balance",
		},
		{
			name:      "spend cap exceeded",
			err:       &APIError{StatusCode: 402, Code: "SPEND_CAP_EXCEEDED", Message: "cap"},
			wantCode:  "spend_cap_exceeded",
			wantInMsg: "spend cap for the current period",
		},
		{
			name:      "session cap reached",
			err:       &APIError{StatusCode: 402, Code: "cap_reached", Message: "cap"},
			wantCode:  "session_cap_reached",
			wantInMsg: "session/credit cap",
		},
		{
			name:      "unknown error falls back to generic",
			err:       errors.New("boom"),
			wantCode:  "insufficient_credit",
			wantInMsg: "prepaid credit balance",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := CreditError(tc.err)
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
			if !strings.Contains(msg, tc.wantInMsg) {
				t.Errorf("msg = %q, want it to contain %q", msg, tc.wantInMsg)
			}
		})
	}
}

func TestCreditErrorAppendsRetryAfter(t *testing.T) {
	err := &APIError{StatusCode: 402, Code: "INSUFFICIENT_CREDIT", RetryAfter: 30 * time.Second}
	_, msg := CreditError(err)
	if !strings.Contains(msg, "Retry after ~30s") {
		t.Errorf("msg = %q, want it to contain the retry-after hint", msg)
	}
}
