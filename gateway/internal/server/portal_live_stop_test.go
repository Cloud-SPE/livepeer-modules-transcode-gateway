package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/danielgtaylor/huma/v2/humatest"
)

func TestPortalLiveStopOwnershipAndRefresh(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitLive()
	deps := f.engine.deps
	deps.Paid = f.engine
	_, api := humatest.New(t)
	RegisterPortal(api, deps)
	path := "/api/portal/live-streams/" + id.String()
	if got := api.Delete(path); got.Code != 401 {
		t.Fatalf("unauthenticated: %d", got.Code)
	}
	other := context.WithValue(context.Background(), ctxKeyAPIKey, f.newKey())
	if got := api.DeleteCtx(other, path); got.Code != 404 {
		t.Fatalf("other owner: %d", got.Code)
	}
	if f.operation(id).StopRequested {
		t.Fatal("cross-tenant stop")
	}
	owner := context.WithValue(context.Background(), ctxKeyAPIKey, f.key)
	if got := api.Get(path); got.Code != 401 {
		t.Fatalf("unauthenticated read: %d", got.Code)
	}
	if got := api.GetCtx(other, path); got.Code != 404 {
		t.Fatalf("cross-tenant read: %d", got.Code)
	}
	recovered := api.GetCtx(owner, path)
	var detail LiveCreateOut
	if err := json.Unmarshal(recovered.Body.Bytes(), &detail.Body); err != nil {
		t.Fatal(err)
	}
	if recovered.Code != 200 || recovered.Header().Get("Cache-Control") != "no-store" || detail.Body.Session.Ingest.StreamKey == "" {
		t.Fatalf("restore: %d %s", recovered.Code, recovered.Body.String())
	}

	for i := 0; i < 2; i++ {
		if got := api.DeleteCtx(owner, path); got.Code != 202 {
			t.Fatalf("owner stop: %d %s", got.Code, got.Body.String())
		}
	}
	if !f.operation(id).StopRequested || f.operation(id).FinishedAt != nil {
		t.Fatal("stop must be durable but await settlement")
	}
	got := api.GetCtx(owner, "/api/portal/live-streams")
	var list struct {
		Items []PortalLiveStreamView `json:"items"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if got.Code != 200 || len(list.Items) != 1 || list.Items[0].Status != "ending" {
		t.Fatalf("refreshed list: %s", got.Body.String())
	}
	if err := f.process(id); err != nil {
		t.Fatal(err)
	}
	got = api.GetCtx(owner, "/api/portal/live-streams")
	if err := json.Unmarshal(got.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	recovered = api.GetCtx(owner, path)
	detail = LiveCreateOut{}
	if err := json.Unmarshal(recovered.Body.Bytes(), &detail.Body); err != nil {
		t.Fatal(err)
	}
	if detail.Body.Session.Ingest.StreamKey != "" {
		t.Fatal("terminal restore exposed stream key")
	}
	if list.Items[0].Status != "ended" {
		t.Fatalf("settled list: %s", got.Body.String())
	}
}
