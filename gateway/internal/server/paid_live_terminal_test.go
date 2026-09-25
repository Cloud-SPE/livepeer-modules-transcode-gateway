package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/danielgtaylor/huma/v2/humatest"
)

func TestLiveMediaEndsBeforeLOCSettlementAndSurvivesRestart(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitLive()
	f.units = 120
	f.brokerState = "ended"
	f.closeReason = "authorization_exhausted"
	f.locCloseStatus = 400
	if err := f.process(id); err == nil {
		t.Fatal("expected LOC close rejection")
	}
	op := f.operation(id)
	if op.State != "accounting_pending" || op.FinishedAt != nil {
		t.Fatalf("accounting lost: %+v", op)
	}
	view, err := f.engine.ViewLive(context.Background(), id, f.key.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	session := view.Body.Session
	if session.Status != "ended" || !session.SettlementPending || session.EndedAt == nil || session.Ingest.StreamKey != "" || session.ActualUnits != 120 {
		t.Fatalf("stale live view: %+v", session)
	}
	endedAt := *session.EndedAt
	if upstream, err := f.engine.UpstreamURL(context.Background(), id); err != nil || upstream != "" {
		t.Fatalf("terminal ingest admitted: %q %v", upstream, err)
	}
	var state string
	var settledAt *string
	if err := f.pool.QueryRow(context.Background(), `SELECT status,loc_closed_at::text FROM live_streams WHERE id=$1`, id).Scan(&state, &settledAt); err != nil {
		t.Fatal(err)
	}
	if state != "ended" || settledAt != nil {
		t.Fatalf("media/accounting conflated: %s %v", state, settledAt)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT state FROM usage_reservations WHERE id=$1`, op.ReservationID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == "committed" || state == "refunded" {
		t.Fatal("invented final accounting")
	}

	f.restart()
	if err := f.engine.StopLive(context.Background(), id, f.key.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.process(id); err == nil {
		t.Fatal("close should still be pending")
	}
	deps := f.engine.deps
	deps.Paid = f.engine
	_, api := humatest.New(t)
	RegisterPortal(api, deps)
	ctx := context.WithValue(context.Background(), ctxKeyAPIKey, f.key)
	got := api.GetCtx(ctx, "/api/portal/live-streams")
	var list struct {
		Items []PortalLiveStreamView `json:"items"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if got.Code != 200 || len(list.Items) != 1 || list.Items[0].Status != "ended" || !list.Items[0].SettlementPending {
		t.Fatalf("refresh: %s", got.Body.String())
	}
	f.locCloseStatus = 0
	if err := f.process(id); err != nil {
		t.Fatal(err)
	}
	view, err = f.engine.ViewLive(context.Background(), id, f.key.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if view.Body.Session.SettlementPending || !view.Body.Session.EndedAt.Equal(endedAt) || f.operation(id).FinishedAt == nil {
		t.Fatal("settlement failed to converge or changed media end time")
	}
}

func TestLiveDefinitiveRefillRefusalDoesNotReplayOrAdoptSuccessor(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitLive()
	predecessor := f.workID
	f.refillWorkID = "refused-successor"
	f.units = 91
	f.runway = 29
	f.topupErrorCode = "refill_refused"
	f.evidence = false
	if err := f.process(id); err == nil {
		t.Fatal("expected pending terminal evidence")
	}
	s, _, err := f.engine.decode(f.operation(id))
	if err != nil {
		t.Fatal(err)
	}
	if !s.RefillRefused || s.Refill == nil || s.Session.WorkID != predecessor || s.EndReason != "gateway_close" {
		t.Fatal("refusal did not preserve grant ownership and closure intent")
	}
	f.restart()
	f.evidence = true
	f.locCloseStatus = 400
	if err := f.process(id); err == nil {
		t.Fatal("expected LOC rejection")
	}
	if f.refills != 1 || f.topups != 1 {
		t.Fatalf("replayed refusal: refill=%d topup=%d", f.refills, f.topups)
	}
	s, v, err := f.engine.decode(f.operation(id))
	if err != nil {
		t.Fatal(err)
	}
	if s.Claim.WorkID != predecessor || v.Status != "failed" || v.FailureCode != "refill_refused" {
		t.Fatalf("refusal not surfaced: %+v", v)
	}
	f.locCloseStatus = 0
	if err := f.process(id); err != nil {
		t.Fatal(err)
	}
	if f.operation(id).FinishedAt == nil || f.topups != 1 {
		t.Fatal("predecessor settlement did not complete")
	}
}

func TestLiveUnknownConflictKeepsRefillRecovery(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitLive()
	f.units = 91
	f.runway = 29
	f.topupErrorCode = "revision_pending"
	f.evidence = false
	if err := f.process(id); err == nil {
		t.Fatal("expected ambiguous conflict")
	}
	f.restart()
	if err := f.process(id); err == nil {
		t.Fatal("expected pending recovery")
	}
	s, _, err := f.engine.decode(f.operation(id))
	if err != nil {
		t.Fatal(err)
	}
	if s.RefillRefused || f.topups != 2 || f.ends != 0 || s.Refill == nil {
		t.Fatal("unknown conflict treated as definitive rejection")
	}
}
