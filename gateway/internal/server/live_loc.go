package server

import (
	"context"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/loc"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"
)

// liveUnitsPerSecond is the work-unit cadence for live transcode
// (~1000 units per output second; matches the runner's seconds
// extractor and the refill sizing).
const liveUnitsPerSecond = 1000

// closeLiveLOCSession settles the LOC session funding a live stream.
// Called from both the customer DELETE path and the reconciler when it
// observes the broker ended the session — ClaimLOCClose makes sure
// exactly one caller talks to LOC.
//
// actual_units is a duration estimate (elapsed × ~1000 units/sec,
// capped at the configured session ceiling): the broker reports only a
// runway estimate, never consumed units, and LOC's daemon-ledger
// reconciliation is authoritative anyway. A 409 from LOC means an
// earlier close stuck and is treated as success.
func closeLiveLOCSession(ctx context.Context, deps Deps, live *repo.LiveStream, outcome string) {
	if deps.LOC == nil || live == nil || live.LOCSessionID == nil {
		return
	}
	claimed, err := deps.Live.ClaimLOCClose(ctx, live.ID)
	if err != nil {
		deps.Log.Warn("live: loc close claim failed", "live_id", live.ID, "err", err)
		return
	}
	if !claimed {
		return // someone else already closed (or is closing) this session
	}
	started := live.CreatedAt
	if live.StartedAt != nil {
		started = *live.StartedAt
	}
	units := int64(time.Since(started).Seconds()) * liveUnitsPerSecond
	if units < 0 {
		units = 0
	}
	if max := deps.Cfg.LiveMaxTotalUnits; max > 0 && units > max {
		units = max
	}
	resp, err := deps.LOC.CloseSession(ctx, *live.LOCSessionID, loc.CloseSessionRequest{
		ActualUnits: units,
		Outcome:     outcome,
	})
	switch {
	case err == nil:
		deps.Log.Info("live: loc session closed",
			"live_id", live.ID, "loc_session_id", *live.LOCSessionID,
			"actual_units", units, "billed_wei", resp.BilledValueWei.String(),
			"refund_wei", resp.RefundWei.String(), "outcome", outcome)
		if live.ReservationID != nil {
			_ = deps.Usage.MarkSettled(ctx, *live.ReservationID,
				resp.BilledValueWei.BigInt(), repo.SettleSettled)
		}
	case loc.IsAlreadySettled(err):
		if live.ReservationID != nil {
			_ = deps.Usage.MarkSettled(ctx, *live.ReservationID, nil, repo.SettleSettled)
		}
	default:
		// loc.CloseSession already retried 429/5xx. The claim stands so
		// we don't hammer LOC every tick; surface for the operator (LOC's
		// own session janitor is the backstop).
		deps.Log.Error("live: loc close failed; session may stay encumbered on LOC",
			"live_id", live.ID, "loc_session_id", *live.LOCSessionID, "err", err)
	}
}
