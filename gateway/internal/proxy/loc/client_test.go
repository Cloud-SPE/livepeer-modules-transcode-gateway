package loc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/loc/loctest"
	"github.com/google/uuid"
)

func testClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c := NewClient(baseURL, "pymth_test", "transcode-gateway/test/dev", 5*time.Second)
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
	return c
}

func TestWeiUnmarshal(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`123`, "123"},
		{`"456"`, "456"}, // catalog prices are quoted decimals
		{`123456789012345678901234567890`, "123456789012345678901234567890"}, // > int64
		{`null`, "0"},
	}
	for _, tc := range cases {
		var w Wei
		if err := json.Unmarshal([]byte(tc.in), &w); err != nil {
			t.Fatalf("unmarshal %s: %v", tc.in, err)
		}
		if got := w.String(); got != tc.want {
			t.Errorf("unmarshal %s: got %s want %s", tc.in, got, tc.want)
		}
	}
	var w Wei
	if err := json.Unmarshal([]byte(`"not-a-number"`), &w); err == nil {
		t.Error("expected error for non-numeric wei")
	}
}

func TestCreateJobDecodesBigWei(t *testing.T) {
	fake := loctest.New()
	defer fake.CloseServer()
	fake.CreateJob = func(w http.ResponseWriter, r *http.Request) {
		// SDK identity + api key must be present on every request.
		if r.Header.Get("X-API-Key") != "pymth_test" {
			t.Errorf("missing X-API-Key, got %q", r.Header.Get("X-API-Key"))
		}
		if r.Header.Get("Livepeer-Open-Clearinghouse-SDK") == "" {
			t.Error("missing SDK identity header")
		}
		loctest.WriteJSON(w, 201, map[string]any{
			"job_id":     uuid.NewString(),
			"work_id":    "abcd1234",
			"broker_url": "http://broker.example",
			"protocol":   "paid-job/v1", "transport": "stream", "request_id": "loc-generated", "accounting_mode": "wholesale_account", "work_unit": "units",
			"spend_authorization": "ZmFrZQ==",                           // "fake"
			"expected_value_wei":  json.Number("2000000000000000000"),   // 2 ETH, > int32
			"funded_value_wei":    json.Number("123456789012345678901"), // > int64
			"settle_endpoint":     "/v1/jobs/x/settle",
			"opened_at":           "2026-01-01T00:00:00Z",
		})
	}
	resp, err := testClient(t, fake.URL()).CreateJobV2(context.Background(), CreateJobRequestV2{RequestID: "stable", Transport: "stream",
		Capability: "video:transcode.abr", Offering: "default", EstimatedUnits: 600,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if got := resp.FundedValueWei.String(); got != "123456789012345678901" {
		t.Errorf("funded wei: got %s", got)
	}
	if resp.SpendAuthorization != "ZmFrZQ==" {
		t.Error("spend authorization missing")
	}

}

func TestErrorEnvelopeAndMatchers(t *testing.T) {
	fake := loctest.New()
	defer fake.CloseServer()
	fake.CreateJob = func(w http.ResponseWriter, r *http.Request) {
		loctest.WriteError(w, 402, "INSUFFICIENT_CREDIT", "balance too low")
	}
	c := testClient(t, fake.URL())
	_, err := c.CreateJobV2(context.Background(), CreateJobRequestV2{RequestID: "stable", Transport: "stream", Capability: "x", Offering: "y", EstimatedUnits: 1})
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsInsufficientCredit(err) {
		t.Errorf("IsInsufficientCredit=false for %v", err)
	}
	if IsNoRoute(err) || IsAlreadySettled(err) {
		t.Error("unexpected matcher hit")
	}

	// Case-insensitive: domain errors use lower-case codes.
	fake.CreateJob = func(w http.ResponseWriter, r *http.Request) {
		loctest.WriteError(w, 402, "cap_reached", "session cap reached")
	}
	_, err = c.CreateJobV2(context.Background(), CreateJobRequestV2{RequestID: "stable", Transport: "stream", Capability: "x", Offering: "y", EstimatedUnits: 1})
	if !IsInsufficientCredit(err) {
		t.Errorf("cap_reached should match IsInsufficientCredit: %v", err)
	}

	fake.CreateJob = func(w http.ResponseWriter, r *http.Request) {
		loctest.WriteError(w, 404, "NO_ROUTE_AVAILABLE", "nobody advertises this")
	}
	_, err = c.CreateJobV2(context.Background(), CreateJobRequestV2{RequestID: "stable", Transport: "stream", Capability: "x", Offering: "y", EstimatedUnits: 1})
	if !IsNoRoute(err) {
		t.Errorf("IsNoRoute=false for %v", err)
	}

	// Non-envelope body falls back to raw text.
	fake.CreateJob = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("upstream blew up"))
	}
	_, err = c.CreateJobV2(context.Background(), CreateJobRequestV2{RequestID: "stable", Transport: "stream", Capability: "x", Offering: "y", EstimatedUnits: 1})
	ae, ok := err.(*APIError)
	if !ok || ae.Message != "upstream blew up" || ae.StatusCode != 500 {
		t.Errorf("raw-body fallback: %#v", err)
	}
	if !IsRetryable(err) {
		t.Error("500 should be retryable")
	}
}

func TestSettleRetriesThenSucceeds(t *testing.T) {
	fake := loctest.New()
	defer fake.CloseServer()
	var calls atomic.Int32
	fake.SettleJob = func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			loctest.WriteError(w, 503, "DAEMON_UNAVAILABLE", "try again")
			return
		}
		loctest.WriteJSON(w, 200, map[string]any{
			"job_id": uuid.NewString(), "work_id": "abcd", "actual_units": 600,
			"billed_value_wei": json.Number("300000"), "refund_wei": json.Number("100"),
			"outcome": "EXACT", "closed_at": "2026-01-01T00:01:00Z",
			"cap_status": map[string]any{"session_pct_used": 0.5, "will_refuse_next_refill": false},
		})
	}
	resp, err := testClient(t, fake.URL()).SettleJobV2(context.Background(), uuid.New(),
		SettleJobRequestV2{BrokerJobID: "broker-job", WorkUnit: "units", Settlement: map[string]any{"payload": map[string]any{}, "signature": map[string]any{}}, ActualUnits: 600})
	if err != nil {
		t.Fatalf("SettleJob: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 attempts, got %d", calls.Load())
	}
	if resp.BilledValueWei.String() != "300000" {
		t.Errorf("billed wei: %s", resp.BilledValueWei)
	}
}

func TestDoubleSettleIsAlreadySettled(t *testing.T) {
	fake := loctest.New()
	defer fake.CloseServer()
	c := testClient(t, fake.URL())
	jobID := uuid.New()
	if _, err := c.SettleJobV2(context.Background(), jobID, SettleJobRequestV2{BrokerJobID: "broker-job", WorkUnit: "units", Settlement: map[string]any{"payload": map[string]any{}, "signature": map[string]any{}}, ActualUnits: 10}); err != nil {
		t.Fatalf("first settle: %v", err)
	}
	_, err := c.SettleJobV2(context.Background(), jobID, SettleJobRequestV2{BrokerJobID: "broker-job", WorkUnit: "units", Settlement: map[string]any{"payload": map[string]any{}, "signature": map[string]any{}}, ActualUnits: 10})
	if !IsAlreadySettled(err) {
		t.Errorf("second settle should be IsAlreadySettled, got %v", err)
	}
}

func TestRateLimitedCarriesRetryAfter(t *testing.T) {
	fake := loctest.New()
	defer fake.CloseServer()
	fake.CreateJob = func(w http.ResponseWriter, r *http.Request) {
		loctest.WriteRateLimited(w, 7)
	}
	_, err := testClient(t, fake.URL()).CreateJobV2(context.Background(),
		CreateJobRequestV2{RequestID: "stable", Transport: "stream", Capability: "x", Offering: "y", EstimatedUnits: 1})
	ae, ok := err.(*APIError)
	if !ok || ae.StatusCode != 429 || ae.RetryAfter != 7*time.Second {
		t.Errorf("rate-limit parse: %#v", err)
	}
	// Creates are NOT auto-retried — the single call must surface the 429.
	if fake.CreateCalls() != 1 {
		t.Errorf("create should not retry, got %d calls", fake.CreateCalls())
	}
}

func TestNilClientForMissingConfig(t *testing.T) {
	if NewClient("", "pymth_x", "", 0) != nil || NewClient("http://x", "", "", 0) != nil {
		t.Error("missing base URL or API key should yield nil client")
	}
}

func TestLOCDoesNotFollowRedirects(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(200) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client := NewClient(redirect.URL, "private-api-key", "test/v2/local", time.Second)
	err := client.doJSON(context.Background(), http.MethodGet, "/v1/jobs", nil, nil)
	if err == nil || called {
		t.Fatalf("redirect must fail without forwarding LOC credentials: %v %v", err, called)
	}
}
