package server

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/livepeer"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/loc"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"

	"github.com/google/uuid"
)

// LiveReconciler is the background loop that polls broker session state
// for active live_streams rows and triggers auto-topup when runway falls
// below the threshold. Both concerns share one scan of the active-session
// set, but the state-reconciliation and top-up paths stay separate
// internally per the runner team's preference — each path can be tuned
// (or disabled) independently.
//
// Lifecycle:
//   - cfg.LiveReconcileIntervalSecs == 0 → background loop disabled;
//     on-GET reconciliation in handlers_v1.go remains the only path.
//   - otherwise → ticker fires every N seconds; each tick scans active
//     sessions, reconciles state, then evaluates top-up for any session
//     in publishing state whose runway estimate is below threshold.
type LiveReconciler struct {
	deps             Deps
	tick             time.Duration
	topupThresholdSecs int
	topupFundSecs    int
}

func NewLiveReconciler(deps Deps) *LiveReconciler {
	return &LiveReconciler{
		deps:             deps,
		tick:             time.Duration(deps.Cfg.LiveReconcileIntervalSecs) * time.Second,
		topupThresholdSecs: deps.Cfg.LiveTopupRunwayThresholdSecs,
		topupFundSecs:    deps.Cfg.LiveTopupFundSecs,
	}
}

// Run blocks until ctx is canceled. Caller should invoke on its own
// goroutine. Returns immediately when reconciliation is disabled.
func (r *LiveReconciler) Run(ctx context.Context) {
	if r.tick <= 0 {
		r.deps.Log.Info("live reconciler disabled (LIVE_RECONCILE_INTERVAL_SECS=0)")
		return
	}
	r.deps.Log.Info("live reconciler started",
		"interval_secs", int(r.tick.Seconds()),
		"topup_threshold_secs", r.topupThresholdSecs,
		"topup_fund_secs", r.topupFundSecs)

	t := time.NewTicker(r.tick)
	defer t.Stop()
	// Tick once immediately so a freshly-started gateway doesn't wait a
	// full interval to catch sessions that opened during startup.
	r.scanOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			r.deps.Log.Info("live reconciler stopped")
			return
		case <-t.C:
			r.scanOnce(ctx)
		}
	}
}

// scanOnce walks the active-session set and runs both reconcile + topup
// on each row. Errors per-row are logged and don't stop the scan.
func (r *LiveReconciler) scanOnce(ctx context.Context) {
	rows, err := r.deps.Live.ListActiveForReconcile(ctx, 500)
	if err != nil {
		r.deps.Log.Warn("live reconciler: scan failed", "err", err)
		return
	}
	for _, live := range rows {
		// Each row gets its own time budget so a slow broker doesn't
		// starve the rest of the scan. 5s is generous for an HTTP GET.
		rowCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		refreshed := r.reconcileState(rowCtx, &live)
		if refreshed == nil {
			refreshed = &live
		}
		// Top-up only fires when reconciliation found us in publishing
		// state (so we know there's something to fund). Provisioning /
		// ending / terminal states skip the top-up path.
		if refreshed.Status == repo.LiveActive {
			r.maybeTopUp(rowCtx, refreshed)
		}
		cancel()
	}
}

// reconcileState is the state-reconciliation half of the loop. Identical
// behavior to the on-GET reconciler in handlers_v1.go — calling broker
// GET /v1/cap/{bsess}, mapping state, persisting the snapshot. Returns
// the freshly-loaded row when a write happened; nil if the row was left
// untouched (e.g. broker unreachable).
func (r *LiveReconciler) reconcileState(ctx context.Context, live *repo.LiveStream) *repo.LiveStream {
	return reconcileLiveSession(ctx, r.deps, live)
}

// maybeTopUp is the funding half of the loop. Polls runway estimate
// from the broker (via GET, since the spec doesn't push runway on its
// own); if below threshold, opens a new reservation, mints a new
// payment envelope, and calls broker POST /v1/cap/{bsess}/topup.
//
// Per the runner team's call (D): one reservation per envelope. We open
// a new reservation row keyed to the same live_stream_id every time we
// top up — explicit audit trail for each funded chunk.
func (r *LiveReconciler) maybeTopUp(ctx context.Context, live *repo.LiveStream) {
	if r.topupThresholdSecs <= 0 || r.topupFundSecs <= 0 {
		return // top-up disabled
	}
	if live.BrokerSessionID == nil || live.BrokerURL == nil {
		return
	}
	// We need a runway estimate to decide. The broker emits balance on
	// top-up responses; the GET response shape carries it opportunistically.
	// Re-poll specifically for balance (cheap, cached on broker side).
	resp, err := r.deps.HTTP.GetLiveSession(ctx, *live.BrokerURL, *live.BrokerSessionID)
	if err != nil || resp == nil || resp.Balance == nil {
		// No runway signal → assume we're fine. Conservative: if the
		// session is in publishing without observable runway, the next
		// tick will catch it once balance shows up.
		return
	}
	if resp.Balance.RunwaySecondsEstimate >= r.topupThresholdSecs {
		return
	}
	r.deps.Log.Info("live topup: runway below threshold; minting",
		"live_id", live.ID,
		"broker_session_id", *live.BrokerSessionID,
		"runway_secs", resp.Balance.RunwaySecondsEstimate,
		"threshold_secs", r.topupThresholdSecs,
		"fund_secs", r.topupFundSecs)
	if outcome, err := r.executeTopUp(ctx, live); err != nil {
		r.deps.Metrics.LiveTopupAttempts.WithLabelValues(live.Capability, outcome).Inc()
		r.deps.Log.Warn("live topup: failed",
			"live_id", live.ID,
			"broker_session_id", *live.BrokerSessionID,
			"outcome", outcome,
			"err", err)
		return
	}
	r.deps.Metrics.LiveTopupAttempts.WithLabelValues(live.Capability, "succeeded").Inc()
}

// executeTopUp refills the live session's LOC funding and delivers the
// fresh envelope to the broker. LOC pins the refill to the session's
// original broker (same recipient + work_id, nonce incremented), so no
// re-resolve / match-by-broker step exists anymore. Refills don't open
// per-envelope reservation rows — LOC's session ledger is the
// authoritative money log; the gateway tracks loc_refill_count for
// operator visibility.
//
// executeTopUp returns (outcome, error). On success, outcome is empty
// (the caller uses "succeeded"); on failure, outcome is one of:
// loc_unavailable, no_loc_session, cap_reached, refill_failed,
// rotation_unrecoverable, broker_failed. Distinct labels let dashboards
// surface the failure kind directly.
func (r *LiveReconciler) executeTopUp(ctx context.Context, live *repo.LiveStream) (string, error) {
	if r.deps.LOC == nil {
		return "loc_unavailable", errors.New("topup: LOC not configured")
	}
	if live.LOCSessionID == nil {
		// Pre-migration row — there's no LOC session to refill. The
		// stream runs until its broker-side balance drains.
		return "no_loc_session", errors.New("topup: live session has no LOC session id (pre-LOC row)")
	}
	refill, err := r.deps.LOC.RefillSession(ctx, *live.LOCSessionID, loc.RefillSessionRequest{})
	if err != nil {
		if loc.IsInsufficientCredit(err) {
			// Session / spend-period cap reached — LOC will keep refusing.
			// End the stream gracefully instead of letting the broker
			// starve it mid-broadcast.
			r.windDown(ctx, live, "cap_reached")
			return "cap_reached", err
		}
		return "refill_failed", err
	}
	payment, err := refill.PaymentBytes()
	if err != nil {
		return "refill_failed", err
	}

	requestID := uuid.NewString()
	topupResp, err := r.deps.HTTP.TopUpLiveSession(ctx,
		*live.BrokerURL, *live.BrokerSessionID,
		live.Capability, live.Offering, requestID, payment,
		livepeer.LiveTopUpRequest{GatewaySessionID: live.ID},
	)
	if err != nil && livepeer.IsInvalidRecipientRandError(err) {
		// The broker rotated its payment session. LOC exposes no
		// rotation-recovery primitive (no ReportPaymentResult) and the
		// refill is pinned to the rotated recipient — retry once in case
		// LOC's daemon refreshed its cache, then give up gracefully.
		if refill2, rerr := r.deps.LOC.RefillSession(ctx, *live.LOCSessionID, loc.RefillSessionRequest{}); rerr == nil {
			if payment2, perr := refill2.PaymentBytes(); perr == nil {
				topupResp, err = r.deps.HTTP.TopUpLiveSession(ctx,
					*live.BrokerURL, *live.BrokerSessionID,
					live.Capability, live.Offering, requestID, payment2,
					livepeer.LiveTopUpRequest{GatewaySessionID: live.ID},
				)
				outcome := "succeeded"
				if err != nil {
					outcome = "retry_failed"
				}
				r.deps.Metrics.SessionRotationRetries.WithLabelValues(live.Capability, outcome).Inc()
			}
		}
		if err != nil && livepeer.IsInvalidRecipientRandError(err) {
			// Unrecoverable: end the stream cleanly rather than letting
			// the balance starve. Tracked as an upstream LOC gap.
			r.windDown(ctx, live, "rotation_unrecoverable")
			return "rotation_unrecoverable", err
		}
	}
	if err != nil {
		return "broker_failed", err
	}

	_ = r.deps.Live.IncrementLOCRefill(ctx, live.ID)
	if refill.CapStatus.WillRefuseNextRefill {
		reason := ""
		if refill.CapStatus.WinddownReason != nil {
			reason = *refill.CapStatus.WinddownReason
		}
		r.deps.Log.Warn("live topup: LOC will refuse the next refill — stream ends when this funding drains",
			"live_id", live.ID, "winddown_reason", reason,
			"refill_seq", refill.RefillSeq)
	}
	r.deps.Log.Info("live topup: succeeded",
		"live_id", live.ID,
		"broker_session_id", *live.BrokerSessionID,
		"new_runway_secs", topupResp.Balance.RunwaySecondsEstimate,
		"loc_refill_seq", refill.RefillSeq)
	return "", nil
}

// windDown ends a live stream gracefully when its funding can't
// continue (cap reached, unrecoverable payment-session rotation): end
// the broker session, settle the LOC session, mark the row ended, and
// tear down the customer's RTMP relay so OBS sees a clean disconnect.
func (r *LiveReconciler) windDown(ctx context.Context, live *repo.LiveStream, reason string) {
	r.deps.Log.Warn("live: winding down session", "live_id", live.ID, "reason", reason)
	if live.BrokerURL != nil && live.BrokerSessionID != nil {
		if _, err := r.deps.HTTP.EndLiveSession(ctx,
			*live.BrokerURL, *live.BrokerSessionID, uuid.NewString(), reason); err != nil {
			r.deps.Log.Warn("live winddown: broker end failed; continuing local teardown",
				"live_id", live.ID, "err", err)
		}
	}
	_ = r.deps.Live.EndWithReason(ctx, live.ID, repo.LiveEnded, reason)
	closeLiveLOCSession(ctx, r.deps, live, reason)
	if r.deps.RTMPProbe != nil {
		if closed := r.deps.RTMPProbe.CloseSession(live.ID.String()); closed {
			r.deps.Log.Info("live winddown: rtmp relay torn down", "live_id", live.ID)
		}
	}
	r.deps.Metrics.LiveStreamsActive.Dec()
}

// truncate keeps long broker error bodies from blowing out the usage
// table's error_text column.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}
