package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/config"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/livepeer"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/loc"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/s3"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests use a disposable database selected explicitly by the operator.
// Each test creates its own schema, applies real migrations, and removes only
// that schema. All LOC/broker/media URLs point at httptest, never production.
func paidTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "paid_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	paths, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("migration %s: %v", p, err)
		}
	}
	return pool
}

var fixtureSignature = "0x" + strings.Repeat("1", 130)

type paidFixture struct {
	liveExchange                                                                     string
	liveExchangeID                                                                   string
	openStatus                                                                       int
	admissionRejected, jobNotAdmitted                                                bool
	jobEstimated, jobLimit                                                           int64
	blockABR                                                                         <-chan struct{}
	abrStarted                                                                       chan struct{}
	t                                                                                *testing.T
	pool                                                                             *pgxpool.Pool
	engine                                                                           *PaidEngine
	key                                                                              *repo.APIKey
	server                                                                           *httptest.Server
	mu                                                                               sync.Mutex
	jobID, sessionID, gatewayID                                                      uuid.UUID
	requestID, workID, workloadID, refillWorkID                                      string
	jobCreates, dispatches, settles, opens, keyIssues, refills, topups, ends, closes int
	evidence                                                                         bool
	mismatch                                                                         bool
	forged                                                                           bool
	loseABRResponse                                                                  bool
	exchangeAvailable                                                                bool
	runway, units                                                                    int64
	topupLostOnce                                                                    bool
	topupErrorCode                                                                   string
	locCloseStatus                                                                   int
	brokerState                                                                      string
	keyLostOnce                                                                      bool
	keyTTL                                                                           time.Duration
	keyRequests                                                                      []string
	keyExpiries                                                                      map[string]time.Time
	closeFail                                                                        bool
	locClosed                                                                        bool
	closeReason, outputState, failureCode                                            string
	topupRequests                                                                    []string
}

func newPaidFixture(t *testing.T) *paidFixture {
	t.Helper()
	f := &paidFixture{t: t, pool: paidTestPool(t), jobID: uuid.New(), sessionID: uuid.New(), gatewayID: uuid.New(), workID: strings.Repeat("a", 64), evidence: true, exchangeAvailable: true, runway: 120, units: 0, outputState: "ready"}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	ctx := context.Background()
	store, err := s3.New(ctx, "us-east-1", f.server.URL, f.server.URL, "test-vod", "test-access", "test-secret", 3600)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{CallerPrivateKey: strings.Repeat("0", 63) + "1", OperationSecretsKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), ABRCapability: "video:transcode.abr", ABROffering: "abr-default", ABRMaxTotalUnits: 1000000, ABRJobTimeoutSecs: 5, LiveCapability: "video:transcode.live", LiveGatewayIngestOffering: "gateway-ingest", LiveMaxTotalUnits: 6000, LiveRTMPPort: 1935, LiveExternalRTMPURL: "rtmp://gateway.invalid:1935/live", LiveOutputProfile: "live-standard", LiveMeteringRendition: "720p", LiveTopupRunwayThresholdSecs: 60, LiveTopupFundSecs: 60, LiveReconcileIntervalSecs: 1, IPHashPepper: "test-pepper"}
	deps := Deps{Cfg: cfg, Pool: f.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Usage: repo.NewReservationRepo(f.pool), Live: repo.NewLiveRepo(f.pool), Caps: repo.NewCapabilityRepo(f.pool), S3: store, LOC: loc.NewClient(f.server.URL, "test-loc-key", "test/v2/local", time.Second), HTTP: livepeer.NewHTTPClient(time.Second)}
	f.engine, err = NewPaidEngine(deps, repo.NewOperationRepo(f.pool))
	if err != nil {
		t.Fatal(err)
	}
	rows := []repo.UpsertCapability{{CapabilityID: "abr", Capability: cfg.ABRCapability, Offering: cfg.ABROffering, Protocol: "paid-job/v1", WorkUnit: "video-frame-megapixel", UnitsPerPrice: big.NewInt(1000000), JobJSON: []byte(`{"transports":["stream"]}`)}, {CapabilityID: "live", Capability: cfg.LiveCapability, Offering: cfg.LiveGatewayIngestOffering, Protocol: "paid-session/v1", WorkUnit: "output_seconds", UnitsPerPrice: big.NewInt(60), SessionJSON: []byte(`{"descriptor_schema":"rtmp-hls/v1","refill":"extensible"}`)}}
	if err = deps.Caps.ReplaceSnapshot(ctx, rows); err != nil {
		t.Fatal(err)
	}
	f.key = f.newKey()
	return f
}
func (f *paidFixture) newKey() *repo.APIKey {
	f.t.Helper()
	id, user := uuid.New(), uuid.New()
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO waitlist(id,name,email,status) VALUES($1,'Integration test',$2,'approved')`, user, user.String()+"@example.invalid"); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO api_keys(id,waitlist_id,key_prefix,key_hash) VALUES($1,$2,'sk-test',$3)`, id, user, id.String()); err != nil {
		f.t.Fatal(err)
	}
	return &repo.APIKey{ID: id}
}
func fixtureJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func fixtureError(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": code}})
}
func (f *paidFixture) claim(session bool) map[string]any {
	payload := map[string]any{"work_id": f.workID, "work_unit_name": "video-frame-megapixel", "request_id": f.requestID, "job_id": "broker-job", "actual_units": "321", "outcome": "succeeded"}
	if session {
		payload = map[string]any{"session_id": "broker-session", "gateway_session_id": f.gatewayID.String(), "work_id": f.workID, "work_unit_name": "output_seconds", "debited_units": fmt.Sprint(f.units), "state": "closed", "breakdown": map[string]any{"termination_reason": f.closeReason, "output_state": f.outputState, "last_failure_code": f.failureCode}}
	}
	if f.mismatch {
		payload["work_id"] = strings.Repeat("b", 64)
	}
	signature := fixtureSignature
	if f.forged {
		signature = "0x" + strings.Repeat("2", 130)
	}
	return map[string]any{"payload": payload, "signature": map[string]any{"algorithm": "secp256k1", "canonicalization": "jcs", "value": signature}}
}
func (f *paidFixture) claimHeader(session bool) string {
	b, _ := json.Marshal(f.claim(session))
	return base64.StdEncoding.EncodeToString(b)
}
func (f *paidFixture) sessionResponse() map[string]any {
	state := f.brokerState
	if state == "" {
		state = "active"
	}
	return map[string]any{"session_id": "broker-session", "gateway_session_id": f.gatewayID.String(), "work_id": f.workID, "state": state, "credential": "broker-credential-secret", "runtime": map[string]any{"schema": "rtmp-hls/v1", "public": map[string]any{"rtmp_url": "rtmp://runner.invalid:1935/live", "hls_url": f.server.URL + "/hls/master.m3u8", "key_issue_url": f.server.URL + "/keys"}, "grants": []any{map[string]any{"id": "grant-1", "operations": []string{"stream-key-issue"}, "secret": "grant-secret", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)}}}, "balance": map[string]any{"runway_seconds_estimate": f.runway, "claimed_units": f.units, "authorization_cap_remaining_units": 100, "unit": "output_seconds"}, "usage": map[string]any{"unit": "output_seconds", "claimed_total": f.units}, "output_state": f.outputState, "last_failure_code": f.failureCode, "close_reason": f.closeReason}
}
func (f *paidFixture) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/job" && f.blockABR != nil {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		f.abrStarted <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-f.blockABR:
		}
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	if r.Body != nil && r.Method != "GET" {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	path := r.URL.Path
	switch {
	case path == "/v1/jobs" && r.Method == "POST":
		f.jobCreates++
		f.jobEstimated, f.jobLimit = int64(body["estimated_units"].(float64)), int64(body["max_total_units"].(float64))
		f.requestID = r.Header.Get("Idempotency-Key")
		if f.requestID == "" || body["workload_request_digest"] == nil || body["caller_public_key"] == nil {
			fixtureError(w, 400, "invalid_scope")
			return
		}
		fixtureJSON(w, map[string]any{"job_id": f.jobID, "request_id": f.requestID, "work_id": f.workID, "broker_url": f.server.URL, "protocol": "paid-job/v1", "transport": "stream", "work_unit": "video-frame-megapixel", "spend_authorization": base64.StdEncoding.EncodeToString([]byte("job-authorization")), "accounting_mode": "wholesale_account", "funded_value_wei": "1000", "expected_value_wei": "321"})
	case path == "/v1/job":
		f.dispatches++
		if f.admissionRejected {
			fixtureError(w, 402, "insufficient_balance")
			return
		}
		if r.Header.Get("Livepeer-Protocol") != "paid-job/v1" || r.Header.Get("Livepeer-Caller-Proof") == "" {
			fixtureError(w, 401, "missing_proof")
			return
		}
		f.workloadID = fmt.Sprint(body["workload_id"])
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Livepeer-Job-Id", "broker-job")
		if f.evidence && !f.loseABRResponse {
			w.Header().Set("Trailer", "Livepeer-Settlement")
		}
		_, _ = fmt.Fprintf(w, "data: {\"schema\":\"video-transcode-abr-progress/v2\",\"workload_id\":%q,\"phase\":\"encoding\",\"overall_progress\":50}\n\n", f.workloadID)
		_, _ = fmt.Fprintf(w, "data: {\"schema\":\"video-transcode-abr-result/v2\",\"workload_id\":%q,\"outcome\":\"succeeded\"}\n\n", f.workloadID)
		if f.evidence && !f.loseABRResponse {
			w.Header().Set("Livepeer-Settlement", f.claimHeader(false))
		}
	case strings.HasPrefix(path, "/v1/exchange/"):
		if f.liveExchange != "" {
			fixtureJSON(w, map[string]any{"request_id": f.requestID, "outcome": f.liveExchange, "session_id": f.liveExchangeID, "gateway_session_id": f.gatewayID.String()})
			return
		}

		if f.admissionRejected {
			fixtureJSON(w, map[string]any{"request_id": f.requestID, "outcome": "ADMISSION_REJECTED"})
			return
		}
		if !f.evidence || !f.exchangeAvailable {
			fixtureError(w, 404, "not_ready")
			return
		}
		fixtureJSON(w, map[string]any{"outcome": "SETTLED", "request_id": f.requestID, "job_id": "broker-job", "unit": "video-frame-megapixel", "work_units": "321", "settlement": f.claimHeader(false)})
	case strings.HasPrefix(path, "/v1/jobs/") && strings.HasSuffix(path, "/settle"):
		if !f.validEvidence(body) {
			fixtureError(w, 422, "invalid_settlement")
			return
		}
		f.settles++
		fixtureJSON(w, map[string]any{"job_id": f.jobID, "work_id": f.workID, "closed_at": time.Now(), "billed_value_wei": "321", "actual_units": 321})
	case strings.HasPrefix(path, "/v1/jobs/"):
		if f.jobNotAdmitted {
			fixtureJSON(w, map[string]any{"job_id": f.jobID, "state": "closed", "accounting_outcome": "broker_settled", "broker_exchange_outcome": "NOT_ADMITTED", "actual_units": 0, "billed_value_wei": "0", "closed_at": time.Now()})
			return
		}
		fixtureJSON(w, map[string]any{"job_id": f.jobID, "state": "open", "work_id": f.workID})
	case path == "/v1/sessions/prepare":
		fixtureJSON(w, map[string]any{"gateway_session_id": f.gatewayID, "broker_url": f.server.URL, "preparation_token": "prepare-secret", "route_binding": map[string]any{}, "expires_at": time.Now().Add(time.Hour)})
	case path == "/v1/sessions":
		f.requestID = r.Header.Get("Idempotency-Key")
		fixtureJSON(w, map[string]any{"session_id": f.sessionID, "request_id": f.requestID, "work_id": f.workID, "broker_url": f.server.URL, "protocol": "paid-session/v1", "session": map[string]any{"descriptor_schema": "rtmp-hls/v1", "refill": "extensible"}, "spend_authorization": base64.StdEncoding.EncodeToString([]byte("session-authorization")), "accounting_mode": "wholesale_account", "funded_value_wei": "120", "expected_value_wei": "120"})
	case path == "/v1/session":
		f.opens++
		if f.openStatus != 0 {
			fixtureError(w, f.openStatus, "payment_invalid")
			return
		}
		if r.Header.Get("Livepeer-Protocol") != "paid-session/v1" || r.Header.Get("Livepeer-Caller-Proof") == "" {
			fixtureError(w, 401, "missing_proof")
			return
		}
		fixtureJSON(w, f.sessionResponse())
	case path == "/keys":
		f.keyIssues++
		if r.Header.Get("Authorization") != "Bearer grant-secret" || body["audience"] != "gateway-relay" {
			fixtureError(w, 401, "wrong_grant")
			return
		}
		requestID, _ := body["request_id"].(string)
		f.keyRequests = append(f.keyRequests, requestID)
		if f.keyExpiries == nil {
			f.keyExpiries = make(map[string]time.Time)
		}
		expires, found := f.keyExpiries[requestID]
		if !found {
			ttl := f.keyTTL
			if ttl <= 0 {
				ttl = time.Hour
			}
			expires = time.Now().Add(ttl)
			f.keyExpiries[requestID] = expires
		}
		if f.keyLostOnce {
			f.keyLostOnce = false
			fixtureError(w, 503, "response_lost")
			return
		}
		fixtureJSON(w, map[string]any{"request_id": requestID, "stream_key": "upstream-secret-key", "expires_at": expires.Format(time.RFC3339)})

	case strings.HasSuffix(path, "/refill"):
		f.refills++
		refillWork := f.workID
		if f.refillWorkID != "" {
			refillWork = f.refillWorkID
		}
		fixtureJSON(w, map[string]any{"work_id": refillWork, "request_id": r.Header.Get("Idempotency-Key"), "refill_seq": f.refills, "spend_authorization": base64.StdEncoding.EncodeToString([]byte("refill-authorization")), "accounting_mode": "wholesale_account", "funded_value_wei": "180", "expected_value_wei": "180"})
	case strings.HasSuffix(path, "/topup"):
		f.topups++
		if f.topupErrorCode != "" {
			fixtureError(w, 409, f.topupErrorCode)
			return
		}
		if f.refillWorkID != "" {
			f.workID = f.refillWorkID
		}
		f.topupRequests = append(f.topupRequests, r.Header.Get("Livepeer-Request-ID"))
		f.runway = 120
		if f.topupLostOnce {
			f.topupLostOnce = false
			fixtureError(w, 503, "response_lost")
			return
		}
		fixtureJSON(w, f.sessionResponse())
	case strings.HasSuffix(path, "/end"):
		f.ends++
		if f.closeFail {
			fixtureError(w, 503, "broker_unreachable")
			return
		}
		out := f.sessionResponse()
		out["state"] = "ended"
		fixtureJSON(w, out)
	case strings.HasPrefix(path, "/v1/settlement/"):
		if !f.evidence {
			fixtureError(w, 404, "not_ready")
			return
		}
		w.Header().Set("Livepeer-Settlement", f.claimHeader(true))
		fixtureJSON(w, map[string]any{"state": "closed"})
	case strings.HasSuffix(path, "/close"):
		if !f.validEvidence(body) {
			fixtureError(w, 422, "invalid_settlement")
			return
		}
		f.closes++
		if f.locCloseStatus != 0 {
			fixtureError(w, f.locCloseStatus, "settlement_rejected")
			return
		}
		fixtureJSON(w, map[string]any{"session_id": f.sessionID, "work_id": f.workID, "closed_at": time.Now(), "billed_value_wei": fmt.Sprint(f.units), "actual_units": f.units})
	case strings.HasPrefix(path, "/v1/session/"):
		if r.Header.Get("Authorization") != "Bearer broker-credential-secret" {
			fixtureError(w, 401, "wrong_credential")
			return
		}
		if f.closeFail {
			fixtureError(w, 503, "broker_unreachable")
			return
		}
		fixtureJSON(w, f.sessionResponse())
	case strings.HasPrefix(path, "/v1/sessions/"):
		out := map[string]any{"session_id": f.sessionID, "state": "open", "work_id": f.workID}
		if f.locClosed {
			out["closed_at"] = time.Now()
			out["actual_units"] = f.units
			out["billed_value_wei"] = fmt.Sprint(f.units)
			out["accounting_outcome"] = "broker_settled"
		}
		fixtureJSON(w, out)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, path)
		fixtureError(w, 404, "unexpected")
	}
}

// The fixture models LOC as the signature trust boundary: only the exact
// accepted signed envelope is admitted. Cryptographic proof tests live in
// the wire package; this suite verifies rejected evidence never finishes work.
func (f *paidFixture) validEvidence(body map[string]any) bool {
	env, ok := body["settlement"].(map[string]any)
	if !ok {
		return false
	}
	sig, ok := env["signature"].(map[string]any)
	return ok && sig["value"] == fixtureSignature
}
func (f *paidFixture) submitABR(key string) uuid.UUID {
	f.t.Helper()
	in := &ABRIn{IdempotencyKey: key}
	in.Body.InputURL = f.server.URL + "/input.mp4"
	in.Body.EstimatedSecs = 10
	out, err := f.engine.SubmitABR(context.Background(), f.key, in)
	if err != nil {
		f.t.Fatal(err)
	}
	return out.Body.Job.ID
}
func (f *paidFixture) submitLive() uuid.UUID {
	f.t.Helper()
	out, err := f.engine.SubmitLive(context.Background(), f.key, &LiveIn{IdempotencyKey: "live-key"})
	if err != nil {
		f.t.Fatal(err)
	}
	return out.Body.Session.ID
}
func (f *paidFixture) process(id uuid.UUID) error { return f.engine.Process(context.Background(), id) }
func (f *paidFixture) restart() {
	f.t.Helper()
	engine, err := NewPaidEngine(f.engine.deps, repo.NewOperationRepo(f.pool))
	if err != nil {
		f.t.Fatal(err)
	}
	f.engine = engine
}
func (f *paidFixture) operation(id uuid.UUID) *repo.PaidOperation {
	f.t.Helper()
	o, err := f.engine.ops.Get(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return o
}

func TestPaidABRDurableSettlementAndTenantIdempotency(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitABR("same-request")
	if got := f.submitABR("same-request"); got != id {
		t.Fatal("identical retry created another operation")
	}
	in := &ABRIn{IdempotencyKey: "same-request"}
	in.Body.InputURL = f.server.URL + "/other.mp4"
	if _, err := f.engine.SubmitABR(context.Background(), f.key, in); err == nil {
		t.Fatal("changed idempotent body accepted")
	}
	other := f.newKey()
	if _, err := f.engine.ViewABR(context.Background(), id, other.ID); err == nil {
		t.Fatal("cross-tenant operation visible")
	}
	f.restart()
	if err := f.process(id); err != nil {
		t.Fatal(err)
	}
	out, err := f.engine.ViewABR(context.Background(), id, f.key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Body.Job.Status != "succeeded" || out.Body.Job.ActualUnits == nil || *out.Body.Job.ActualUnits != 321 || out.Body.Job.MasterPlaylistURL == "" {
		t.Fatalf("wrong result %+v", out.Body.Job)
	}
	if err = f.process(id); err != nil {
		t.Fatal(err)
	}
	if f.jobCreates != 1 || f.dispatches != 1 || f.settles != 1 {
		t.Fatalf("duplicate network effects %d/%d/%d", f.jobCreates, f.dispatches, f.settles)
	}
	o := f.operation(id)
	if bytes.Contains(o.Secrets, []byte("job-authorization")) || bytes.Contains(o.Secrets, []byte(f.server.URL)) {
		t.Fatal("operation secrets persisted plaintext")
	}
	var units int64
	var state, settle string
	if err = f.pool.QueryRow(context.Background(), `SELECT committed_work_units,state,settle_state FROM usage_reservations WHERE id=$1`, o.ReservationID).Scan(&units, &state, &settle); err != nil {
		t.Fatal(err)
	}
	if units != 321 || state != "committed" || settle != "settled" {
		t.Fatalf("wrong usage projection %d %s %s", units, state, settle)
	}
}
func TestPaidABRBadEvidenceNeverCompletes(t *testing.T) {
	for _, mode := range []string{"missing", "mismatched", "forged"} {
		t.Run(mode, func(t *testing.T) {
			f := newPaidFixture(t)
			switch mode {
			case "missing":
				f.evidence = false
			case "mismatched":
				f.mismatch = true
			case "forged":
				f.forged = true
			}
			id := f.submitABR("bad-evidence")
			if err := f.process(id); err == nil {
				t.Fatal("bad evidence accepted")
			}
			o := f.operation(id)
			if o.FinishedAt != nil {
				t.Fatalf("bad evidence became terminal: %s", o.State)
			}
			out, err := f.engine.ViewABR(context.Background(), id, f.key.ID)
			if err != nil {
				t.Fatal(err)
			}
			if out.Body.Job.Status == "succeeded" || out.Body.Job.MasterPlaylistURL != "" {
				t.Fatal("unverified playback exposed")
			}
			if f.settles != 0 {
				t.Fatal("bad evidence was settled")
			}
		})
	}
}
func TestPaidABRLostResponseRecoversWithoutRedispatch(t *testing.T) {
	f := newPaidFixture(t)
	f.loseABRResponse = true
	f.exchangeAvailable = false
	id := f.submitABR("lost-response")
	if err := f.process(id); err == nil {
		t.Fatal("expected unresolved terminal accounting")
	}
	f.exchangeAvailable = true
	f.restart()
	if err := f.process(id); err != nil {
		t.Fatal(err)
	}
	out, err := f.engine.ViewABR(context.Background(), id, f.key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Body.Job.Status != "succeeded" || f.dispatches != 1 || f.jobCreates != 1 {
		t.Fatalf("recovery duplicated work or lost result %+v calls=%d/%d", out.Body.Job, f.dispatches, f.jobCreates)
	}
}
func TestPaidLiveGrantRefillAndFinalEvidence(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitLive()
	o := f.operation(id)
	if o.State != "active" || f.opens != 1 || f.keyIssues != 1 {
		t.Fatalf("live admission failed %s open/key=%d/%d", o.State, f.opens, f.keyIssues)
	}
	if bytes.Contains(o.Secrets, []byte("broker-credential-secret")) || bytes.Contains(o.PublicJSON, []byte("grant-secret")) {
		t.Fatal("live secret leaked")
	}
	if upstream, err := f.engine.UpstreamURL(context.Background(), id); err != nil || !strings.HasSuffix(upstream, "/upstream-secret-key") {
		t.Fatalf("relay credential missing %s %v", upstream, err)
	}
	f.units = 110
	f.runway = 10
	f.restart()
	if err := f.process(id); err != nil {
		t.Fatal(err)
	}
	if f.refills != 1 || f.topups != 1 {
		t.Fatalf("refill did not reach both sides %d/%d", f.refills, f.topups)
	}
	f.units = 130
	f.evidence = false
	if err := f.engine.StopLive(context.Background(), id, f.key.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.process(id); err == nil {
		t.Fatal("missing close evidence accepted")
	}
	if f.operation(id).FinishedAt != nil {
		t.Fatal("live finished without evidence")
	}
	if upstream, err := f.engine.UpstreamURL(context.Background(), id); err != nil || upstream != "" {
		t.Fatal("stopped relay still authorized")
	}
	f.evidence = true
	f.restart()
	if err := f.process(id); err != nil {
		t.Fatal(err)
	}
	var units int64
	var state, settle string
	if err := f.pool.QueryRow(context.Background(), `SELECT committed_work_units,state,settle_state FROM usage_reservations WHERE id=$1`, o.ReservationID).Scan(&units, &state, &settle); err != nil {
		t.Fatal(err)
	}
	if units != 130 || state != "committed" || settle != "settled" {
		t.Fatalf("final live units not projected: %d %s %s", units, state, settle)
	}
	if f.closes != 1 {
		t.Fatalf("close count %d", f.closes)
	}
}

func TestPaidConcurrentIntentAndCrossTenantKeys(t *testing.T) {
	f := newPaidFixture(t)
	input := func() *ABRIn {
		in := &ABRIn{IdempotencyKey: "parallel-key"}
		in.Body.InputURL = f.server.URL + "/input.mp4"
		in.Body.EstimatedSecs = 10
		return in
	}
	var wg sync.WaitGroup
	ids := make(chan uuid.UUID, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := f.engine.SubmitABR(context.Background(), f.key, input())
			if err != nil {
				errs <- err
				return
			}
			ids <- out.Body.Job.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var same uuid.UUID
	for id := range ids {
		if same != uuid.Nil && id != same {
			t.Fatal("concurrent identical requests produced different operations")
		}
		same = id
	}
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM usage_reservations WHERE api_key_id=$1`, f.key.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("idempotency conflict left orphan reservations: %d", n)
	}
	other := f.newKey()
	out, err := f.engine.SubmitABR(context.Background(), other, input())
	if err != nil {
		t.Fatal(err)
	}
	if out.Body.Job.ID == same {
		t.Fatal("different tenants shared an idempotency namespace")
	}
	if f.jobCreates != 0 || f.dispatches != 0 {
		t.Fatal("intent creation executed before durable worker")
	}
}

func TestPaidCatalogDenominatorAndOpaqueAxesRoundTrip(t *testing.T) {
	f := newPaidFixture(t)
	units, ok := new(big.Int).SetString("18446744073709551615", 10)
	if !ok {
		t.Fatal("invalid fixture denominator")
	}
	row := repo.UpsertCapability{CapabilityID: "future:custom", Capability: "future:custom", Offering: "name-with-live", Protocol: "paid-job/v1", WorkUnit: "video-frame-megapixel", UnitsPerPrice: units, PricePerWorkUnitWei: big.NewInt(17), WorkUnitEstimatorJSON: []byte(`{"kind":"future/v3","options":{"scale":"9007199254740993"}}`), JobJSON: []byte(`{"transports":["stream"],"unknown":true}`), ExtraJSON: []byte(`{"model":"future"}`)}
	if err := f.engine.deps.Caps.ReplaceSnapshot(context.Background(), []repo.UpsertCapability{row}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.engine.deps.Caps.ListActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].UnitsPerPrice.String() != units.String() || rows[0].Protocol != "paid-job/v1" || rows[0].InteractionMode != nil {
		t.Fatalf("catalog metadata lost %+v", rows)
	}
	if !bytes.Contains(rows[0].WorkUnitEstimatorJSON, []byte("9007199254740993")) || !bytes.Contains(rows[0].JobJSON, []byte("unknown")) {
		t.Fatal("future axes/estimator metadata was dropped")
	}
}

func TestPaidLongABRStreamsDoNotStarveLiveStop(t *testing.T) {
	f := newPaidFixture(t)
	blocked := make(chan struct{})
	f.blockABR = blocked
	f.abrStarted = make(chan struct{}, 2)
	_ = f.submitABR("long-one")
	_ = f.submitABR("long-two")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.engine.Run(ctx); close(done) }()
	defer func() { cancel(); close(blocked); <-done }()
	for i := 0; i < 2; i++ {
		select {
		case <-f.abrStarted:
		case <-time.After(3 * time.Second):
			t.Fatal("both ABR worker streams did not start")
		}
	}
	id := f.submitLive()
	if err := f.engine.StopLive(context.Background(), id, f.key.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if f.operation(id).FinishedAt != nil {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("two long ABR streams starved live stop/reconciliation")
		case <-tick.C:
		}
	}
}

func TestPaidABRAdmissionRejectionWaitsForLOCAndSurvivesRestart(t *testing.T) {
	f := newPaidFixture(t)
	f.admissionRejected = true
	id := f.submitABR("rejected")
	if err := f.process(id); err == nil {
		t.Fatal("expected broker refusal")
	}
	if err := f.process(id); err == nil {
		t.Fatal("expected pending recovery")
	}
	ctx := context.Background()
	view, err := f.engine.ViewABR(ctx, id, f.key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Body.Job.Status != "admission_rejected" || view.Body.Job.ErrorCode != "broker_admission_rejected" || view.Body.Job.Error == "" || view.Body.Job.MasterPlaylistURL != "" {
		t.Fatalf("pending view: %+v", view.Body.Job)
	}
	if f.operation(id).FinishedAt != nil || f.settles != 0 || f.dispatches != 1 {
		t.Fatal("rejection was finalized or redispatched")
	}
	public, err := f.engine.ops.PublicViews(ctx, []uuid.UUID{id})
	if err != nil || public[id] == nil || len(public[id].Secrets) != 0 {
		t.Fatalf("admin projection: %v %v", public, err)
	}
	_, api := humatest.New(t)
	registerAdminABRJobs(api, f.engine.deps)
	response := api.Get("/api/admin/abr-jobs")
	var listing struct {
		Items []AdminABRJobView `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(listing.Items) != 1 || listing.Items[0].Status != "admission_rejected" || listing.Items[0].AccountingState != "dispatching" || listing.Items[0].ErrorText == "" {
		t.Fatalf("admin rejection status: %s", response.Body.String())
	}
	f.restart()
	if err := f.process(id); err == nil {
		t.Fatal("expected pending recovery after restart")
	}
	if f.dispatches != 1 {
		t.Fatal("replayed rejected workload")
	}
	f.jobNotAdmitted = true
	if err := f.process(id); err != nil {
		t.Fatal(err)
	}
	view, err = f.engine.ViewABR(ctx, id, f.key.ID)
	if err != nil {
		t.Fatal(err)
	}
	job := view.Body.Job
	if job.Status != "failed" || job.ErrorCode != "not_admitted" || job.AccountingState != "broker_settled" || job.ActualUnits == nil || *job.ActualUnits != 0 || job.MasterPlaylistURL != "" || f.operation(id).FinishedAt == nil {
		t.Fatalf("terminal view: %+v", job)
	}
}

func TestPaidABRAuthorizationUsesPersistedWorkloadBound(t *testing.T) {
	f := newPaidFixture(t)
	id := f.submitABR("bounded")
	// Configuration changes must not enlarge an existing durable authorization.
	f.engine.deps.Cfg.ABRMaxTotalUnits = 2000000
	f.restart()
	if err := f.process(id); err != nil {
		t.Fatal(err)
	}
	if f.jobEstimated != 2182 || f.jobLimit != 2728 {
		t.Fatalf("estimate=%d limit=%d", f.jobEstimated, f.jobLimit)
	}
	if got := f.submitABR("bounded"); got != id {
		t.Fatal("idempotent retry replaced job")
	}
	in := &ABRIn{IdempotencyKey: "explicit-bound"}
	in.Body.InputURL = f.server.URL + "/input.mp4"
	in.Body.EstimatedSecs = 10
	in.Body.MaxTotalUnits = 5000
	out, err := f.engine.SubmitABR(context.Background(), f.key, in)
	if err != nil {
		t.Fatal(err)
	}
	o := f.operation(out.Body.Job.ID)
	s, _, err := f.engine.decode(o)
	if err != nil || s.LimitUnits != 5000 {
		t.Fatalf("explicit bound: %+v %v", s, err)
	}
}
