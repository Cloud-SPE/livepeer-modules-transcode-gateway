package livepeer

import (
	"errors"
	"testing"
)

func TestDefinitiveRefillRefusalRequiresExactStatusAndCode(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{409, `{"error":{"code":"refill_refused"}}`, true},
		{503, `{"error":{"code":"refill_refused"}}`, false},
		{409, `{"error":{"code":"revision_pending","message":"refill_refused"}}`, false},
		{409, `refill_refused`, false},
	} {
		if got := IsRefillRefusedError(&BrokerError{StatusCode: tc.status, Body: tc.body}); got != tc.want {
			t.Fatalf("%+v: %v", tc, got)
		}
	}
	if IsRefillRefusedError(errors.New("refill_refused")) {
		t.Fatal("transport error classified as refusal")
	}
}
