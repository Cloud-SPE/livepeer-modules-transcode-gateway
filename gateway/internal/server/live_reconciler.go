package server

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/livepeer"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/service"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"

	"github.com/google/uuid"
	paymentsv1 "github.com/Cloud-SPE/livepeer-network-modules/livepeer-network-protocol/proto-go/livepeer/payments/v1"
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

// executeTopUp does the per-envelope mint + broker call + reservation
// bookkeeping. The reservation is committed on success / refunded on
// failure — the standard reservation lifecycle, applied to a top-up
// envelope instead of an initial open.
// executeTopUp returns (outcome, error). On success, outcome is empty
// (the caller uses "succeeded"); on failure, outcome is one of:
// resolver_failed, broker_drift, mint_failed, reservation_failed,
// broker_failed. Distinct labels let dashboards surface the failure
// kind directly.
func (r *LiveReconciler) executeTopUp(ctx context.Context, live *repo.LiveStream) (string, error) {
	if r.deps.Resolver == nil || r.deps.Payer == nil {
		return "resolver_failed", errors.New("topup: resolver/payer unavailable")
	}
	// Re-resolve a candidate to get the current price + quote metadata
	// for this capability. We use the existing broker for the topup
	// regardless — the broker keeps session affinity, so we can't
	// failover mid-session even if the registry now prefers a different
	// orch. SelectMany is still the cleanest way to get the quote params.
	candidates, err := r.deps.Resolver.SelectMany(ctx, service.SelectRequest{
		Capability: live.Capability,
		Offering:   live.Offering,
	})
	if err != nil {
		return "resolver_failed", err
	}
	var c *service.Candidate
	for i := range candidates {
		if candidates[i].WorkerURL == *live.BrokerURL {
			c = &candidates[i]
			break
		}
	}
	if c == nil {
		return "broker_drift", errors.New("topup: original broker no longer advertises this capability")
	}

	estUnits := int64(r.topupFundSecs) * 1000 // ~1000 units/sec for live; matches the runner's seconds extractor cadence
	if estUnits < 1 {
		estUnits = 1
	}
	face := faceValue(estUnits, c.PricePerWorkUnitWei)
	envelope, mintErr := r.deps.Payer.MintEnvelope(ctx, livepeer.MintRequest{
		RecipientEthAddrHex:   c.EthAddress,
		BrokerURL:             c.WorkerURL,
		Capability:            c.Capability,
		Offering:              c.Offering,
		PricePerUnitWei:       c.PricePerWorkUnitWei,
		UnitsPerPrice:         c.UnitsPerPrice,
		WorkUnitName:          c.WorkUnit,
		QuoteID:               c.QuoteID,
		QuoteVersion:          c.QuoteVersion,
		ConstraintFingerprint: c.ConstraintFingerprint,
		RouteFingerprint:      c.RouteFingerprint,
		EstimatedUnits:        uint64(estUnits),
		FundedValueWei:        face,
		MaxTotalUnits:         0,
		TopUpAllowed:          true,
	})
	if mintErr != nil {
		return "mint_failed", mintErr
	}

	// Open a new per-envelope reservation row linked to this live session.
	topupWorkID := uuid.New()
	res, resErr := r.deps.Usage.Open(ctx, repo.OpenInput{
		APIKeyID:            live.APIKeyID,
		WorkID:              topupWorkID,
		Capability:          live.Capability,
		Offering:            live.Offering,
		EstimatedWorkUnits:  &estUnits,
		PricePerWorkUnitWei: c.PricePerWorkUnitWei,
	})
	if resErr != nil {
		return "reservation_failed", resErr
	}
	_ = r.deps.Usage.SetLiveStreamID(ctx, topupWorkID, live.ID)

	requestID := uuid.NewString()
	topupResp, err := r.deps.HTTP.TopUpLiveSession(ctx,
		c.WorkerURL, *live.BrokerSessionID,
		c.Capability, c.Offering, requestID, envelope.PaymentBytes,
		livepeer.LiveTopUpRequest{GatewaySessionID: live.ID},
	)
	if err != nil {
		// Session-rotation retry-once on the top-up path. If the broker
		// returns 401 + INVALID_RECIPIENT_RAND, evict the payer-daemon
		// cache, re-mint, retry once. Mirrors the dispatcher's logic.
		if livepeer.IsInvalidRecipientRandError(err) && envelope.WorkID != "" {
			if rerr := r.deps.Payer.ReportPaymentResult(ctx, envelope.WorkID, c.Capability, c.Offering,
				paymentsv1.PaymentRejectionReason_PAYMENT_REJECTION_REASON_INVALID_RECIPIENT_RAND); rerr == nil {
				if re, mintErr := r.deps.Payer.MintEnvelope(ctx, livepeer.MintRequest{
					RecipientEthAddrHex:   c.EthAddress,
					BrokerURL:             c.WorkerURL,
					Capability:            c.Capability,
					Offering:              c.Offering,
					PricePerUnitWei:       c.PricePerWorkUnitWei,
					UnitsPerPrice:         c.UnitsPerPrice,
					WorkUnitName:          c.WorkUnit,
					QuoteID:               c.QuoteID,
					QuoteVersion:          c.QuoteVersion,
					ConstraintFingerprint: c.ConstraintFingerprint,
					RouteFingerprint:      c.RouteFingerprint,
					EstimatedUnits:        uint64(estUnits),
					FundedValueWei:        face,
					MaxTotalUnits:         0,
					TopUpAllowed:          true,
				}); mintErr == nil {
					topupResp, err = r.deps.HTTP.TopUpLiveSession(ctx,
						c.WorkerURL, *live.BrokerSessionID,
						c.Capability, c.Offering, requestID, re.PaymentBytes,
						livepeer.LiveTopUpRequest{GatewaySessionID: live.ID},
					)
					r.deps.Metrics.SessionRotationRetries.WithLabelValues(c.Capability, service.RotationOutcomeSucceeded).Inc()
				}
			}
		}
		if err != nil {
			_ = r.deps.Usage.Refund(ctx, res.ID, 502, truncate(err.Error(), 200))
			r.deps.Metrics.ProxyReservationsTotal.WithLabelValues(c.Capability, "refunded").Inc()
			return "broker_failed", err
		}
	}

	statusCode := 200
	_ = r.deps.Usage.Commit(ctx, res.ID, repo.CommitInput{
		BrokerURL:  c.WorkerURL,
		EthAddress: c.EthAddress,
		StatusCode: &statusCode,
	})
	r.deps.Metrics.ProxyReservationsTotal.WithLabelValues(c.Capability, "committed").Inc()
	r.deps.Log.Info("live topup: succeeded",
		"live_id", live.ID,
		"broker_session_id", *live.BrokerSessionID,
		"new_runway_secs", topupResp.Balance.RunwaySecondsEstimate,
		"work_id", topupWorkID)
	return "", nil
}

// truncate keeps long broker error bodies from blowing out the usage
// table's error_text column.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}
