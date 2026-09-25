package server

import (
	"context"
	"testing"
)

func TestStopRejectedLiveDoesNotRetryAdmission(t *testing.T) {
	f := newPaidFixture(t)
	f.openStatus = 401
	f.evidence = false
	f.liveExchange = "ADMISSION_REJECTED"
	id := f.submitLive()
	opens := f.opens
	if err := f.engine.StopLive(context.Background(), id, f.key.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_ = f.process(id)
		f.restart()
		v, err := f.engine.ViewLive(context.Background(), id, f.key.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		if v.Body.Session.Status != "ended" || !v.Body.Session.SettlementPending || v.Body.Session.Ingest.StreamKey != "" {
			t.Fatalf("bad cancellation view: %+v", v.Body.Session)
		}
		if f.opens != opens || f.closes != 0 || f.keyIssues != 0 {
			t.Fatal("cancellation submitted work or invented settlement")
		}
		var state string
		if err := f.pool.QueryRow(context.Background(), `SELECT state FROM usage_reservations WHERE id=$1`, f.operation(id).ReservationID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "refunded" || state == "committed" {
			t.Fatal("invented final accounting")
		}
	}
	f.locClosed = true
	if err := f.process(id); err != nil {
		t.Fatal(err)
	}
	if f.operation(id).FinishedAt == nil {
		t.Fatal("LOC confirmed closure not recovered")
	}
}

func TestStopAmbiguousLiveOnlyReplaysKnownSession(t *testing.T) {
	for _, outcome := range []string{"IN_FLIGHT", "NO_RECORD", "ADMITTED_OUTCOME_UNKNOWN"} {
		t.Run(outcome, func(t *testing.T) {
			f := newPaidFixture(t)
			f.openStatus = 503
			f.evidence = false
			f.liveExchange = outcome
			id := f.submitLive()
			opens := f.opens
			if err := f.engine.StopLive(context.Background(), id, f.key.ID); err != nil {
				t.Fatal(err)
			}
			_ = f.process(id)
			f.restart()
			_ = f.process(id)
			if f.opens != opens || f.operation(id).FinishedAt != nil {
				t.Fatal("ambiguous outcome retried open or finalized")
			}
			v, _ := f.engine.ViewLive(context.Background(), id, f.key.ID, true)
			if v.Body.Session.Status != "ending" || v.Body.Session.StatusMessage == "" {
				t.Fatal("missing cancellation diagnosis")
			}
			if outcome != "IN_FLIGHT" {
				return
			}
			// Lost successful open response: exchange now confirms the original session.
			f.liveExchangeID = "broker-session"
			f.openStatus = 0
			f.evidence = true
			if err := f.process(id); err != nil {
				t.Fatal(err)
			}
			if f.opens != opens+1 || f.ends != 1 || f.keyIssues != 0 || f.operation(id).FinishedAt == nil {
				t.Fatal("known session was not recovered and stopped without issuing ingest")
			}
		})
	}
}

func TestStopUndispatchedLiveDoesNotOpenBroker(t *testing.T) {
	f := newPaidFixture(t)
	f.openStatus = 503
	f.evidence = false
	id := f.submitLive()
	// Model a durable issued authorization saved before any broker dispatch.
	lock, err := f.engine.ops.Lock(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	op, _ := lock.Get(context.Background())
	s, v, _ := f.engine.decode(op)
	s.DispatchAttempted = false
	op.StopRequested = true
	op.State = "authorized"
	if err := f.engine.save(context.Background(), lock, op, s, v); err != nil {
		t.Fatal(err)
	}
	lock.Close()
	if err := f.engine.StopLive(context.Background(), id, f.key.ID); err != nil {
		t.Fatal(err)
	}
	opens := f.opens
	_ = f.process(id)
	if f.opens != opens || f.operation(id).FinishedAt != nil {
		t.Fatal("issued authority dispatched or refunded")
	}
	view, _ := f.engine.ViewLive(context.Background(), id, f.key.ID, true)
	if view.Body.Session.Status != "ended" || !view.Body.Session.SettlementPending {
		t.Fatal("media cancellation not shown")
	}
}

func TestStopBeforeAuthorizationFinishesLocally(t *testing.T) {
	f := newPaidFixture(t)
	f.openStatus = 503
	f.evidence = false
	id := f.submitLive()
	lock, err := f.engine.ops.Lock(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	op, _ := lock.Get(context.Background())
	s, v, _ := f.engine.decode(op)
	// Model a queued intent before preparation or issuance has begun.
	s.Preparation = nil
	s.Session = nil
	s.DispatchAttempted = false
	op.State = "queued"
	if err := f.engine.save(context.Background(), lock, op, s, v); err != nil {
		t.Fatal(err)
	}
	lock.Close()
	if err := f.engine.StopLive(context.Background(), id, f.key.ID); err != nil {
		t.Fatal(err)
	}
	opens := f.opens
	if err := f.process(id); err != nil {
		t.Fatal(err)
	}
	if f.opens != opens || f.operation(id).FinishedAt == nil || f.operation(id).State != "canceled" {
		t.Fatal("unissued cancellation not completed locally")
	}
}
