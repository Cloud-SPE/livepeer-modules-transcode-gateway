package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/livepeer"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/loc"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"
	"github.com/google/uuid"
)

func TestLiveFinalViewUsesSignedEvidenceAndActualUnits(t *testing.T) {
	old := int64(900)
	for _, tc := range []struct{ name, reason, code, want string }{
		{"output failure", "output_failed", "upload_failed", "failed"},
		{"runner failure", "runner_failed", "", "failed"},
		{"graceful signed close", "gateway_close", "", "ended"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &operationSecrets{EndReason: "gateway_close", Claim: &livepeer.TerminalClaim{State: "closed", CloseReason: tc.reason, FailureCode: tc.code, OutputState: "producing"}}
			v := &operationView{Status: "ending", ActualUnits: &old, FailureCode: "transient_old_error"}
			applyLiveTerminalView(s, v, 17)
			if v.Status != tc.want || v.CloseReason != tc.reason || v.FailureCode != tc.code || v.OutputState != "producing" || *v.ActualUnits != 17 {
				t.Fatalf("wrong final projection: %#v", v)
			}
		})
	}
}
func TestLiveClaimAcceptsOnlyPersistedPredecessorOrPendingSuccessor(t *testing.T) {
	gatewayID := uuid.New()
	for _, workID := range []string{"predecessor", "successor", "unrelated"} {
		t.Run(workID, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"payload": map[string]any{"session_id": "broker-session", "gateway_session_id": gatewayID.String(), "authorization_id": workID, "work_id": workID, "state": "closed", "work_unit_name": "output_seconds", "debited_units": "12"}, "signature": map[string]any{"algorithm": "secp256k1", "canonicalization": "jcs", "value": "0x" + strings.Repeat("ab", 65)}})
			broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/settlement/broker-session" {
					t.Error("wrong settlement lookup identity")
				}
				w.Header().Set(livepeer.HeaderSettlement, base64.StdEncoding.EncodeToString(raw))
				w.Write([]byte(`{}`))
			}))
			defer broker.Close()
			e := &PaidEngine{deps: Deps{HTTP: livepeer.NewHTTPClient(time.Second)}}
			s := &operationSecrets{Preparation: &loc.PrepareSessionResponseV2{GatewaySessionID: gatewayID}, Session: &loc.CreateSessionResponseV2{BrokerURL: broker.URL, WorkID: "predecessor"}, Broker: &brokerSession{SessionID: "broker-session"}, Refill: &loc.RefillSessionResponseV2{WorkID: "successor"}}
			claim, err := e.lookupLiveClaim(context.Background(), s)
			if workID == "unrelated" {
				if err == nil {
					t.Fatal("unrelated signed authorization accepted")
				}
			} else if err != nil || claim.ActualUnits != 12 {
				t.Fatalf("owned grant rejected: %v", err)
			}
		})
	}
}
func TestLiveRecoveryNeverTreatsMissingLOCAccountingAsZero(t *testing.T) {
	sessionID := uuid.New()
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{"unknown", 404, `{"error":{"code":"session_not_found","message":"unknown"}}`, false},
		{"open", 200, `{"session_id":"` + sessionID.String() + `","state":"open","closed_at":null}`, false},
		{"closed missing units", 200, `{"session_id":"` + sessionID.String() + `","state":"closed","closed_at":"2026-09-24T00:00:00Z","billed_value_wei":"0"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
			defer server.Close()
			e := &PaidEngine{deps: Deps{LOC: loc.NewClient(server.URL, "key", "test/v2", time.Second)}}
			o := &repo.PaidOperation{State: "ending"}
			v := &operationView{Status: "ending"}
			s := &operationSecrets{Session: &loc.CreateSessionResponseV2{SessionID: sessionID}}
			recovered, err := e.recoverClosedLive(context.Background(), o, s, v)
			if recovered || (err != nil) != tc.wantErr || o.FinishedAt != nil || v.ActualUnits != nil {
				t.Fatalf("invented final accounting: recovered=%v err=%v operation=%#v", recovered, err, o)
			}
		})
	}
}

func TestPaidLivePendingRefillStopReplaysSuccessor(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitLive()
	initial := f.operation(id)
	before, _, err := f.engine.decode(initial)
	if err != nil {
		t.Fatal(err)
	}
	previousWorkID := before.Session.WorkID
	f.refillWorkID = strings.Repeat("c", 64)
	f.units = 110
	f.runway = 10
	f.topupLostOnce = true
	f.evidence = false
	if err = f.process(id); err == nil {
		t.Fatal("expected lost top-up response")
	}
	pending, _, err := f.engine.decode(f.operation(id))
	if err != nil {
		t.Fatal(err)
	}
	if pending.Refill == nil || pending.RefillID == "" || pending.Session.WorkID != previousWorkID || pending.Refill.WorkID == previousWorkID {
		t.Fatal("refill successor intent was not durably preserved")
	}
	if err = f.engine.StopLive(context.Background(), id, f.key.ID); err != nil {
		t.Fatal(err)
	}
	f.evidence = true
	f.restart()
	if err = f.process(id); err != nil {
		t.Fatal(err)
	}
	if f.topups != 2 || f.refills != 1 || len(f.topupRequests) != 2 || f.topupRequests[0] != f.topupRequests[1] {
		t.Fatalf("pending top-up was not replayed exactly: refills=%d topups=%d keys=%v", f.refills, f.topups, f.topupRequests)
	}
	out := f.operation(id)
	if out.FinishedAt == nil || f.ends != 1 || f.closes != 1 {
		t.Fatalf("stop did not settle successor: state=%s end=%d close=%d", out.State, f.ends, f.closes)
	}
	saved, view, err := f.engine.decode(out)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Session.WorkID != pending.Refill.WorkID || view.ActualUnits == nil || *view.ActualUnits != 110 {
		t.Fatal("terminal journal lost successor or measured units")
	}
}

func TestPaidLiveLOCClosureRecoversUnavailableBroker(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitLive()
	f.units = 31
	f.closeFail = true
	f.evidence = false
	f.locClosed = true
	if err := f.engine.StopLive(context.Background(), id, f.key.ID); err != nil {
		t.Fatal(err)
	}
	f.restart()
	if err := f.process(id); err != nil {
		t.Fatal(err)
	}
	o := f.operation(id)
	if o.FinishedAt == nil || o.State != "loc_settled" || f.closes != 0 {
		t.Fatalf("LOC terminal recovery failed: state=%s close_calls=%d", o.State, f.closes)
	}
	var units int64
	var state, settle string
	if err := f.pool.QueryRow(context.Background(), `SELECT committed_work_units,state,settle_state FROM usage_reservations WHERE id=$1`, o.ReservationID).Scan(&units, &state, &settle); err != nil {
		t.Fatal(err)
	}
	if units != 31 || state != "committed" || settle != "settled" {
		t.Fatalf("LOC accounting replaced by estimate/refund: %d %s %s", units, state, settle)
	}
}

func TestPaidLiveSignedFailureOverridesLocalGracefulStop(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitLive()
	f.units = 8
	f.closeReason = "output_failed"
	f.outputState = "stalled"
	f.failureCode = "storage_unavailable"
	if err := f.engine.StopLive(context.Background(), id, f.key.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.process(id); err != nil {
		t.Fatal(err)
	}
	_, view, err := f.engine.decode(f.operation(id))
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "failed" || view.CloseReason != "output_failed" || view.FailureCode != "storage_unavailable" || view.OutputState != "stalled" || view.ActualUnits == nil || *view.ActualUnits != 8 {
		t.Fatalf("signed failure hidden by stop request: %#v", view)
	}
}

func TestPaidLiveLostEndResponseRecoversSignedSettlement(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitLive()
	f.units = 17
	f.closeFail = true
	if err := f.engine.StopLive(context.Background(), id, f.key.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.process(id); err != nil {
		t.Fatal(err)
	}
	if f.operation(id).FinishedAt == nil || f.closes != 1 {
		t.Fatal("lost end response blocked available signed settlement")
	}
}

func TestPaidLiveFinalProjectionFailureRemainsRetryable(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitLive()
	f.units = 9
	if err := f.engine.StopLive(context.Background(), id, f.key.ID); err != nil {
		t.Fatal(err)
	}
	_, err := f.pool.Exec(context.Background(), `CREATE FUNCTION reject_live_projection() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'injected projection failure'; END; $$ LANGUAGE plpgsql; CREATE TRIGGER reject_live_projection BEFORE UPDATE ON live_streams FOR EACH ROW EXECUTE FUNCTION reject_live_projection()`)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.process(id); err == nil {
		t.Fatal("expected projection failure")
	}
	o := f.operation(id)
	if o.FinishedAt != nil {
		t.Fatal("failed final projection left worker queue")
	}
	saved, _, err := f.engine.decode(o)
	if err != nil || saved.Claim == nil {
		t.Fatalf("signed terminal claim lost after projection failure: %v", err)
	}
	if _, err = f.pool.Exec(context.Background(), `DROP TRIGGER reject_live_projection ON live_streams`); err != nil {
		t.Fatal(err)
	}
	f.restart()
	if err = f.process(id); err != nil {
		t.Fatal(err)
	}
	if f.operation(id).FinishedAt == nil {
		t.Fatal("final projection did not recover")
	}
}

func TestPaidLiveSavedFailedOpenClaimNeverReopens(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitLive()
	f.units = 0
	o := f.operation(id)
	s, v, err := f.engine.decode(o)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := f.engine.lookupLiveClaim(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	// Model a crash after preserving the broker's terminal failed-open evidence
	// but before LOC's close completed. No broker credential was returned.
	s.Claim = claim
	s.Broker = nil
	s.UpstreamURL = ""
	s.EndReason = "open_failed"
	o.State = "accounting_pending"
	lock, err := f.engine.ops.Lock(context.Background(), id)
	if err != nil || lock == nil {
		t.Fatalf("operation lock: %v", err)
	}
	if err = f.engine.save(context.Background(), lock, o, s, v); err != nil {
		lock.Close()
		t.Fatal(err)
	}
	lock.Close()
	f.restart()
	if err = f.process(id); err != nil {
		t.Fatal(err)
	}
	if f.opens != 1 || f.closes != 1 || f.operation(id).FinishedAt == nil {
		t.Fatalf("saved terminal claim reopened workload: opens=%d closes=%d", f.opens, f.closes)
	}
}

func editFixtureLiveSecrets(t *testing.T, f *paidFixture, id uuid.UUID, edit func(*operationSecrets)) {
	t.Helper()
	o := f.operation(id)
	s, v, err := f.engine.decode(o)
	if err != nil {
		t.Fatal(err)
	}
	edit(s)
	lock, err := f.engine.ops.Lock(context.Background(), id)
	if err != nil || lock == nil {
		t.Fatalf("operation lock: %v", err)
	}
	defer lock.Close()
	if err = f.engine.save(context.Background(), lock, o, s, v); err != nil {
		t.Fatal(err)
	}
}

func TestPaidLiveStreamKeyRenewsOnlyOnReconnect(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitLive()
	editFixtureLiveSecrets(t, f, id, func(s *operationSecrets) { s.UpstreamKeyExpiresAt = time.Now().Add(-time.Minute) })
	// Ordinary reconciliation must not rotate a key and kick a healthy publisher.
	if err := f.process(id); err != nil {
		t.Fatal(err)
	}
	if f.keyIssues != 1 {
		t.Fatal("background control rotated active publisher credential")
	}
	f.restart()
	upstream, err := f.engine.UpstreamURL(context.Background(), id)
	if err != nil || upstream == "" {
		t.Fatalf("reconnect renewal failed: %v", err)
	}
	if f.keyIssues != 2 || f.keyRequests[0] == f.keyRequests[1] {
		t.Fatalf("expired key replayed instead of renewed: %v", f.keyRequests)
	}
	s, _, err := f.engine.decode(f.operation(id))
	if err != nil {
		t.Fatal(err)
	}
	if !liveKeyUsable(s, time.Now()) {
		t.Fatal("renewed expiry was not durably retained")
	}
	f.restart()
	if _, err = f.engine.UpstreamURL(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if f.keyIssues != 2 {
		t.Fatal("restart rotated still-valid key")
	}
}

func TestPaidLiveStreamKeyLostResponseReplaysIssuanceIdentity(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitLive()
	editFixtureLiveSecrets(t, f, id, func(s *operationSecrets) { s.UpstreamKeyExpiresAt = time.Now().Add(-time.Minute) })
	f.keyLostOnce = true
	if _, err := f.engine.UpstreamURL(context.Background(), id); err == nil {
		t.Fatal("expected lost key issuance response")
	}
	pending, _, err := f.engine.decode(f.operation(id))
	if err != nil || pending.KeyIssueRequestID == "" {
		t.Fatalf("key renewal intent was lost: %v", err)
	}
	f.restart()
	if _, err = f.engine.UpstreamURL(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if f.keyIssues != 3 || f.keyRequests[1] != f.keyRequests[2] || f.keyRequests[1] != pending.KeyIssueRequestID {
		t.Fatalf("ambiguous issuance rotated twice: %v", f.keyRequests)
	}
}

func TestPaidLiveExpiredGrantSchedulesSupportedWinddown(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitLive()
	editFixtureLiveSecrets(t, f, id, func(s *operationSecrets) {
		s.UpstreamKeyExpiresAt = time.Now().Add(-time.Minute)
		for i := range s.Broker.Runtime.Grants {
			s.Broker.Runtime.Grants[i].ExpiresAt = time.Now().Add(-time.Minute).Format(time.RFC3339)
		}
	})
	if _, err := f.engine.UpstreamURL(context.Background(), id); err == nil {
		t.Fatal("expired grant allowed reconnect")
	}
	if f.keyIssues != 1 {
		t.Fatal("expired grant sent to runner")
	}
	o := f.operation(id)
	s, v, err := f.engine.decode(o)
	if err != nil {
		t.Fatal(err)
	}
	if o.State != "ending" || s.EndReason != "recovery_failed" || v.FailureCode != "stream_key_grant_expired" {
		t.Fatalf("grant expiry not queued for supported close: %s %#v", o.State, v)
	}
	if err = f.process(id); err != nil {
		t.Fatal(err)
	}
	_, v, err = f.engine.decode(f.operation(id))
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "failed" || v.FailureCode != "stream_key_grant_expired" {
		t.Fatalf("grant expiry lost at settlement: %#v", v)
	}
}

func TestPaidLiveConcurrentReconnectIssuesOneReplacementKey(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitLive()
	editFixtureLiveSecrets(t, f, id, func(s *operationSecrets) { s.UpstreamKeyExpiresAt = time.Now().Add(-time.Minute) })
	results := make(chan error, 2)
	for range 2 {
		go func() { _, err := f.engine.UpstreamURL(context.Background(), id); results <- err }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if f.keyIssues != 2 {
		t.Fatalf("concurrent reconnect rotated more than once: %d", f.keyIssues)
	}
}

func TestPaidLiveExpiredIssuanceReplayAdvancesOnlyAfterDefinitiveResponse(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitLive()
	editFixtureLiveSecrets(t, f, id, func(s *operationSecrets) { s.UpstreamKeyExpiresAt = time.Now().Add(-time.Minute) })
	f.keyLostOnce = true
	if _, err := f.engine.UpstreamURL(context.Background(), id); err == nil {
		t.Fatal("expected lost issuance response")
	}
	pending, _, err := f.engine.decode(f.operation(id))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate downtime longer than the runner key TTL but shorter than the grant.
	f.mu.Lock()
	f.keyExpiries[pending.KeyIssueRequestID] = time.Now().Add(-time.Minute)
	f.mu.Unlock()
	f.restart()
	if _, err = f.engine.UpstreamURL(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if f.keyIssues != 4 || f.keyRequests[1] != f.keyRequests[2] || f.keyRequests[2] == f.keyRequests[3] {
		t.Fatalf("expired replay did not safely advance identity: %v", f.keyRequests)
	}
}
