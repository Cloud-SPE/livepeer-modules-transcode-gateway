package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/livepeer"
)

// stubCandidate is a minimal Candidate good enough for these tests.
func stubCandidate() Candidate {
	return Candidate{
		Capability: "video:transcode.abr",
		Offering:   "default",
		WorkerURL:  "https://example.test/v1/cap",
	}
}

// rotationError is the exact shape the v1.3.1 capability-broker emits
// when the receiver rotated mid-flight — IsInvalidRecipientRandError
// matches on 401 + body containing "INVALID_RECIPIENT_RAND".
func rotationError() error {
	return &livepeer.BrokerError{
		URL:        "https://example.test/v1/cap",
		StatusCode: 401,
		Body:       "process payment: INVALID_RECIPIENT_RAND",
	}
}

// TestDispatch_RotationRetry_SecondAttemptSucceeds is the happy path
// for the v1.3.1 retry contract: first Do call returns INVALID, we
// invoke ReportRotation + re-mint, second Do call succeeds, dispatcher
// returns OK.
func TestDispatch_RotationRetry_SecondAttemptSucceeds(t *testing.T) {
	t.Parallel()

	var (
		mintCalls     int
		doCalls       int
		reportCalls   int
		onRotation    int
		gotOutcome    string
		gotWorkID     string
	)
	a := Attempt{
		MintPayment: func(ctx context.Context, c Candidate) ([]byte, string, error) {
			mintCalls++
			return []byte("envelope"), "wid-1", nil
		},
		Do: func(ctx context.Context, c Candidate, payment []byte) (any, error) {
			doCalls++
			if doCalls == 1 {
				return nil, rotationError()
			}
			return "ok", nil
		},
		ReportRotation: func(ctx context.Context, c Candidate, workID string) error {
			reportCalls++
			if workID != "wid-1" {
				t.Errorf("ReportRotation got workID=%q, want wid-1", workID)
			}
			return nil
		},
		OnRotation: func(ctx context.Context, c Candidate, workID, outcome string, err error) {
			onRotation++
			gotOutcome = outcome
			gotWorkID = workID
		},
	}

	result, _, err := Dispatch(context.Background(), []Candidate{stubCandidate()}, nil, a)
	if err != nil {
		t.Fatalf("Dispatch returned err=%v, want nil", err)
	}
	if result != "ok" {
		t.Errorf("Dispatch result=%v, want \"ok\"", result)
	}
	if mintCalls != 2 || doCalls != 2 || reportCalls != 1 || onRotation != 1 {
		t.Errorf("call counts: mint=%d do=%d report=%d onRot=%d (want 2/2/1/1)",
			mintCalls, doCalls, reportCalls, onRotation)
	}
	if gotOutcome != RotationOutcomeSucceeded {
		t.Errorf("OnRotation outcome=%q, want %q", gotOutcome, RotationOutcomeSucceeded)
	}
	if gotWorkID != "wid-1" {
		t.Errorf("OnRotation workID=%q, want wid-1", gotWorkID)
	}
}

// TestDispatch_RotationRetry_SecondRotationFails verifies that when the
// second broker call also rejects with INVALID_RECIPIENT_RAND, we do
// NOT loop forever. The retry-once contract triggers, fails, and the
// dispatcher falls through to standard failover (next candidate or
// surfacing the error).
func TestDispatch_RotationRetry_SecondRotationFails(t *testing.T) {
	t.Parallel()

	var (
		doCalls     int
		mintCalls   int
		reportCalls int
		onRotation  int
		gotOutcome  string
	)
	a := Attempt{
		MintPayment: func(ctx context.Context, c Candidate) ([]byte, string, error) {
			mintCalls++
			return []byte("envelope"), "wid-1", nil
		},
		Do: func(ctx context.Context, c Candidate, payment []byte) (any, error) {
			doCalls++
			return nil, rotationError()
		},
		ReportRotation: func(ctx context.Context, c Candidate, workID string) error {
			reportCalls++
			return nil
		},
		OnRotation: func(ctx context.Context, c Candidate, workID, outcome string, err error) {
			onRotation++
			gotOutcome = outcome
		},
	}

	_, _, err := Dispatch(context.Background(), []Candidate{stubCandidate()}, nil, a)
	if err == nil {
		t.Fatal("Dispatch returned nil err on persistent rotation, want broker error")
	}
	if !livepeer.IsInvalidRecipientRandError(err) {
		t.Errorf("expected the persistent rotation error to surface, got %T %v", err, err)
	}
	if doCalls != 2 {
		t.Errorf("doCalls=%d, want 2 (initial + retry-once, no infinite loop)", doCalls)
	}
	if mintCalls != 2 {
		t.Errorf("mintCalls=%d, want 2 (initial + re-mint)", mintCalls)
	}
	if reportCalls != 1 {
		t.Errorf("reportCalls=%d, want 1", reportCalls)
	}
	if onRotation != 1 || gotOutcome != RotationOutcomeRetryFailed {
		t.Errorf("OnRotation: calls=%d outcome=%q (want 1 / retry_failed)", onRotation, gotOutcome)
	}
}

// TestDispatch_RotationRetry_ReportRotationFails covers the case where
// our payer-daemon RPC fails (daemon down, network blip). We still
// call OnRotation with report_failed and surface the original error
// to the failover loop — no infinite retry, no eviction lost forever.
func TestDispatch_RotationRetry_ReportRotationFails(t *testing.T) {
	t.Parallel()

	var (
		doCalls     int
		mintCalls   int
		reportCalls int
		gotOutcome  string
	)
	a := Attempt{
		MintPayment: func(ctx context.Context, c Candidate) ([]byte, string, error) {
			mintCalls++
			return []byte("envelope"), "wid-1", nil
		},
		Do: func(ctx context.Context, c Candidate, payment []byte) (any, error) {
			doCalls++
			return nil, rotationError()
		},
		ReportRotation: func(ctx context.Context, c Candidate, workID string) error {
			reportCalls++
			return errors.New("daemon unreachable")
		},
		OnRotation: func(ctx context.Context, c Candidate, workID, outcome string, err error) {
			gotOutcome = outcome
		},
	}

	_, _, err := Dispatch(context.Background(), []Candidate{stubCandidate()}, nil, a)
	if err == nil {
		t.Fatal("Dispatch returned nil err, want the wrapped report-failed error")
	}
	if mintCalls != 1 {
		t.Errorf("mintCalls=%d, want 1 (no re-mint when report fails)", mintCalls)
	}
	if doCalls != 1 {
		t.Errorf("doCalls=%d, want 1 (no retry-Do when report fails)", doCalls)
	}
	if reportCalls != 1 {
		t.Errorf("reportCalls=%d, want 1", reportCalls)
	}
	if gotOutcome != RotationOutcomeReportFailed {
		t.Errorf("OnRotation outcome=%q, want %q", gotOutcome, RotationOutcomeReportFailed)
	}
}

// TestDispatch_RotationRetry_NoWorkID_FallsThrough ensures the retry
// path is gated on the daemon returning a work_id. Older payer-daemons
// (pre-v1.3.1) don't populate it; we must not attempt the rotation
// dance with workID="" — that would call ReportPaymentResult with an
// empty work_id which the daemon rejects.
func TestDispatch_RotationRetry_NoWorkID_FallsThrough(t *testing.T) {
	t.Parallel()

	var reportCalls int
	a := Attempt{
		MintPayment: func(ctx context.Context, c Candidate) ([]byte, string, error) {
			return []byte("envelope"), "", nil // ← no work_id
		},
		Do: func(ctx context.Context, c Candidate, payment []byte) (any, error) {
			return nil, rotationError()
		},
		ReportRotation: func(ctx context.Context, c Candidate, workID string) error {
			reportCalls++
			return nil
		},
	}

	_, _, err := Dispatch(context.Background(), []Candidate{stubCandidate()}, nil, a)
	if err == nil {
		t.Fatal("Dispatch returned nil err, want surfaced rotation error")
	}
	if reportCalls != 0 {
		t.Errorf("ReportRotation called %d times with empty work_id; want 0", reportCalls)
	}
}

// TestDispatch_RotationRetry_HookOptional confirms the dispatcher
// works when callers don't provide a ReportRotation callback at all —
// rotation just surfaces upstream without retry, matching pre-v1.3.1
// behavior. Defensive against partial wiring.
func TestDispatch_RotationRetry_HookOptional(t *testing.T) {
	t.Parallel()

	var doCalls int
	a := Attempt{
		MintPayment: func(ctx context.Context, c Candidate) ([]byte, string, error) {
			return []byte("envelope"), "wid-1", nil
		},
		Do: func(ctx context.Context, c Candidate, payment []byte) (any, error) {
			doCalls++
			return nil, rotationError()
		},
		// No ReportRotation, no OnRotation.
	}

	_, _, err := Dispatch(context.Background(), []Candidate{stubCandidate()}, nil, a)
	if err == nil {
		t.Fatal("Dispatch returned nil err, want surfaced rotation error")
	}
	if doCalls != 1 {
		t.Errorf("doCalls=%d, want 1 (no retry without hook)", doCalls)
	}
}

// TestDispatch_HappyPath sanity-checks that the rotation plumbing
// doesn't disturb the normal 200 path — first Do succeeds, no rotation
// callbacks should fire.
func TestDispatch_HappyPath(t *testing.T) {
	t.Parallel()

	var reportCalls, onRot int
	a := Attempt{
		MintPayment: func(ctx context.Context, c Candidate) ([]byte, string, error) {
			return []byte("envelope"), "wid-1", nil
		},
		Do: func(ctx context.Context, c Candidate, payment []byte) (any, error) {
			return "ok", nil
		},
		ReportRotation: func(ctx context.Context, c Candidate, workID string) error {
			reportCalls++
			return nil
		},
		OnRotation: func(ctx context.Context, c Candidate, workID, outcome string, err error) {
			onRot++
		},
	}

	result, _, err := Dispatch(context.Background(), []Candidate{stubCandidate()}, nil, a)
	if err != nil {
		t.Fatalf("happy path err=%v, want nil", err)
	}
	if result != "ok" {
		t.Errorf("result=%v, want ok", result)
	}
	if reportCalls != 0 || onRot != 0 {
		t.Errorf("non-rotation path triggered rotation hooks: report=%d onRot=%d", reportCalls, onRot)
	}
}
