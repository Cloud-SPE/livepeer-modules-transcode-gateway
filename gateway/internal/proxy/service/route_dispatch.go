package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/livepeer"
)

// Attempt describes one broker call attempt the dispatcher should try.
//
// MintPayment returns the wire-format Payment bytes the caller attaches
// as `Livepeer-Payment`, plus the daemon-assigned work_id. The work_id
// is what the dispatcher feeds to ReportRotation when the broker
// reports the receiver rotated mid-flight.
//
// ReportRotation is invoked once per dispatch, lazily, when the broker
// returns `INVALID_RECIPIENT_RAND`. It tells the payer-daemon to evict
// its cached session for this (recipient, capability, offering) so the
// follow-up MintPayment fetches fresh TicketParams. Optional — when
// nil, rotation surfaces upstream as a normal broker error.
//
// OnRotation is the observability hook fired exactly once per dispatch
// whenever the rotation branch executes. Receives the outcome label
// (`report_failed` / `mint_failed` / `retry_failed` / `succeeded`) and
// the old work_id. Caller wires metrics + structured log here. Optional.
type Attempt struct {
	Candidate      Candidate
	Do             func(ctx context.Context, c Candidate, payment []byte) (any, error)
	MintPayment    func(ctx context.Context, c Candidate) (envelope []byte, workID string, err error)
	ReportRotation func(ctx context.Context, c Candidate, workID string) error
	OnRotation     func(ctx context.Context, c Candidate, workID, outcome string, err error)
}

// Rotation outcome labels — keep in sync with the Prometheus counter
// at metrics.SessionRotationRetries{outcome}.
const (
	RotationOutcomeSucceeded    = "succeeded"
	RotationOutcomeReportFailed = "report_failed"
	RotationOutcomeMintFailed   = "mint_failed"
	RotationOutcomeRetryFailed  = "retry_failed"
)

// Dispatch walks candidates in order; for each, it skips ones in
// cooldown, mints a payment envelope, runs the attempt, and either
// returns the result or moves to the next candidate.
//
// Session-rotation retry: if the broker returns INVALID_RECIPIENT_RAND,
// we call ReportRotation (which evicts the payer-daemon's cached
// session) and re-mint + retry exactly once against the SAME candidate.
// A second rotation in a row falls through to normal failover.
func Dispatch(ctx context.Context, candidates []Candidate, health *Health, a Attempt) (any, Candidate, error) {
	var (
		lastErr error
		used    Candidate
	)
	if len(candidates) == 0 {
		return nil, used, livepeer.ErrNoCapableBroker
	}
	for _, c := range candidates {
		key := healthKey(c)
		if health != nil && health.CoolingDown(key, time.Now()) {
			continue
		}
		payment, workID, err := a.MintPayment(ctx, c)
		if err != nil {
			lastErr = fmt.Errorf("mint payment for %s: %w", c.WorkerURL, err)
			continue
		}
		result, err := a.Do(ctx, c, payment)
		if err == nil {
			if health != nil {
				health.RecordSuccess(key)
			}
			return result, c, nil
		}
		// Session-rotation retry-once. Only triggers when the dispatcher
		// has been given a ReportRotation hook AND the payer-daemon
		// returned us a work_id when we minted (older daemons left it
		// empty — defensive against pre-v1.3.1 deployments).
		if livepeer.IsInvalidRecipientRandError(err) && a.ReportRotation != nil && workID != "" {
			rerr := a.ReportRotation(ctx, c, workID)
			if rerr != nil {
				if a.OnRotation != nil {
					a.OnRotation(ctx, c, workID, RotationOutcomeReportFailed, rerr)
				}
				lastErr = fmt.Errorf("report rotation for %s: %w", c.WorkerURL, rerr)
				// Fall through to failover; eviction may not have
				// happened, but ReportPaymentResult returning Aborted
				// is already swallowed by the daemon client.
			} else {
				freshPayment, _, mintErr := a.MintPayment(ctx, c)
				if mintErr != nil {
					if a.OnRotation != nil {
						a.OnRotation(ctx, c, workID, RotationOutcomeMintFailed, mintErr)
					}
					err = fmt.Errorf("re-mint after rotation for %s: %w", c.WorkerURL, mintErr)
				} else {
					result, err2 := a.Do(ctx, c, freshPayment)
					if err2 == nil {
						if a.OnRotation != nil {
							a.OnRotation(ctx, c, workID, RotationOutcomeSucceeded, nil)
						}
						if health != nil {
							health.RecordSuccess(key)
						}
						return result, c, nil
					}
					if a.OnRotation != nil {
						a.OnRotation(ctx, c, workID, RotationOutcomeRetryFailed, err2)
					}
					err = err2
				}
			}
		}
		used = c
		lastErr = err
		if !livepeer.IsRetryable(err) {
			return nil, c, err
		}
		if health != nil {
			health.RecordFailure(key, time.Now())
		}
	}
	if lastErr == nil {
		lastErr = livepeer.ErrNoCapableBroker
	}
	return nil, used, lastErr
}

func healthKey(c Candidate) string {
	return c.Capability + "|" + c.Offering + "|" + c.WorkerURL
}
