package server

import (
	"context"
	"strings"
	"testing"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"
)

func TestLiveStartupMessageIsSanitizedAndScoped(t *testing.T) {
	for _, code := range []string{"broker_http_401", "broker_http_403", "loc_http_403", "broker_http_503", "secret upstream body"} {
		op := &repo.PaidOperation{State: "dispatching", LastError: &code}
		message := liveStartupMessage(op, "provisioning")
		if !strings.Contains(message, "retrying automatically") || strings.Contains(message, code) || strings.Contains(message, "credit") {
			t.Fatalf("unsafe or missing message: %q", message)
		}
		for _, status := range []string{"live", "ended", "failed", "ending"} {
			if liveStartupMessage(op, status) != "" {
				t.Fatalf("stale message for %s", status)
			}
		}
		op.StopRequested = true
		if liveStartupMessage(op, "provisioning") != "" {
			t.Fatal("stop overridden")
		}
	}
	if liveStartupMessage(&repo.PaidOperation{}, "provisioning") != "" {
		t.Fatal("healthy startup marked blocked")
	}
}

func TestLiveAdmissionMessageSurvivesRestartAndClearsOnRecovery(t *testing.T) {
	f := newPaidFixture(t)
	f.openStatus = 401
	f.evidence = false
	id := f.submitLive()
	for i := 0; i < 2; i++ {
		if err := f.process(id); err == nil {
			t.Fatal("expected admission refusal")
		}
		f.restart()
		view, err := f.engine.ViewLive(context.Background(), id, f.key.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		if view.Body.Session.Status != "provisioning" || !strings.Contains(view.Body.Session.StatusMessage, "rejected payment authorization") {
			t.Fatalf("missing diagnosis: %+v", view.Body.Session)
		}
		if f.operation(id).FinishedAt != nil {
			t.Fatal("refusal falsely finalized accounting")
		}
	}
	f.openStatus = 0
	if err := f.process(id); err != nil {
		t.Fatal(err)
	}
	view, err := f.engine.ViewLive(context.Background(), id, f.key.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if view.Body.Session.Status != "live" || view.Body.Session.StatusMessage != "" {
		t.Fatalf("stale diagnosis: %+v", view.Body.Session)
	}
}
