package server

import (
	"context"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"
)

// SettleJanitor releases LOC credit stuck in 'pending' settle state.
// Every LOC job encumbers worst-case credit at create and must be
// settled to release it; the inline settle in the ABR handler can be
// lost to a settle-call failure or a gateway crash between the broker
// call and the settle. Each tick re-drives those rows:
//
//   - reservation committed → broker accepted the job; settle with the
//     estimated units (same figure the inline path bills — the runner
//     webhook carries no unit counts).
//   - reservation open      → crashed mid-dispatch, broker outcome
//     unknown; settle 0 (full release) and refund the reservation.
//   - reservation refunded  → dispatch failed but the settle(0) didn't
//     stick; settle 0.
//
// A 409 job_already_settled from LOC means an earlier settle landed —
// the row is marked settled without re-billing (idempotent recovery).
type SettleJanitor struct {
	deps  Deps
	tick  time.Duration
	grace time.Duration
	batch int
}

// settleGrace is how old a pending row must be before the janitor
// touches it — long enough that the inline settle path has clearly
// given up, short enough that encumbered credit doesn't idle.
const settleGrace = 30 * time.Second

func NewSettleJanitor(deps Deps) *SettleJanitor {
	return &SettleJanitor{
		deps:  deps,
		tick:  time.Duration(deps.Cfg.SettleJanitorIntervalSecs) * time.Second,
		grace: settleGrace,
		batch: 200,
	}
}

// Run blocks until ctx is canceled; invoke on its own goroutine.
// Disabled when SETTLE_JANITOR_INTERVAL_SECS=0 or LOC isn't configured.
func (j *SettleJanitor) Run(ctx context.Context) {
	if j.tick <= 0 {
		j.deps.Log.Info("settle janitor disabled (SETTLE_JANITOR_INTERVAL_SECS=0)")
		return
	}
	if j.deps.LOC == nil {
		j.deps.Log.Info("settle janitor disabled (LOC not configured)")
		return
	}
	j.deps.Log.Info("settle janitor started",
		"interval_secs", int(j.tick.Seconds()), "grace_secs", int(j.grace.Seconds()))
	t := time.NewTicker(j.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			j.deps.Log.Info("settle janitor stopped")
			return
		case <-t.C:
			j.Once(ctx)
		}
	}
}

// Once drains one batch of stuck rows. Exported for tests.
func (j *SettleJanitor) Once(ctx context.Context) {
	rows, err := j.deps.Usage.ListPendingSettle(ctx, time.Now().Add(-j.grace), j.batch)
	if err != nil {
		j.deps.Log.Warn("settle janitor: scan failed", "err", err)
		return
	}
	for _, row := range rows {
		if row.LOCJobID == nil {
			// pending without a LOC job id shouldn't be reachable
			// (SetLOCJob writes both atomically); flag for an operator.
			j.deps.Log.Error("settle janitor: pending row without loc_job_id",
				"reservation_id", row.ID, "work_id", row.WorkID)
			_ = j.deps.Usage.MarkSettled(ctx, row.ID, nil, repo.SettleFailed)
			continue
		}
		var (
			units   int64
			outcome string
		)
		switch row.State {
		case repo.ReservationCommitted:
			units = derefInt64(row.EstimatedWorkUnits)
			if row.CommittedWorkUnits != nil {
				units = *row.CommittedWorkUnits
			}
			outcome = "janitor_estimate"
		case repo.ReservationOpen:
			// Crash between create and commit — broker outcome unknown.
			// Release the encumbrance and close out the reservation.
			outcome = "janitor_release"
			_ = j.deps.Usage.Refund(ctx, row.ID, 503, "settle_janitor_release")
		default: // refunded — dispatch failed, settle(0) didn't stick
			outcome = "janitor_release"
		}
		j.deps.Log.Info("settle janitor: re-driving settle",
			"reservation_id", row.ID, "loc_job_id", *row.LOCJobID,
			"reservation_state", string(row.State), "actual_units", units)
		settleLOCJob(ctx, j.deps, row.ID, *row.LOCJobID, units, outcome)
	}
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
