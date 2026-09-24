package loc

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestV2IssuanceIdempotencyAndGeneratedBrokerIdentity(t *testing.T) {
	id := uuid.New()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Idempotency-Key") != "persistent-operation" {
			t.Error("missing stable issuance key")
		}
		if r.Header.Get("X-API-Key") != "test-api-key" {
			t.Error("missing LOC authentication")
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["transport"] != "stream" || body["caller_public_key"] != "caller" || body["workload_request_digest"] != "digest" {
			t.Errorf("incorrect issuance request %#v", body)
		}
		json.NewEncoder(w).Encode(map[string]any{"job_id": id, "request_id": "loc-generated-broker-id", "work_id": "authorization-id", "protocol": "paid-job/v1", "transport": "stream", "work_unit": "units", "accounting_mode": "wholesale_account", "spend_authorization": "encoded"})
	}))
	defer server.Close()
	client := NewClient(server.URL, "test-api-key", "test/v2", time.Second)
	request := CreateJobRequestV2{RequestID: "persistent-operation", Capability: "cap", Offering: "offer", Transport: "stream", EstimatedUnits: 1, MaxTotalUnits: 10, CallerPublicKey: "caller", WorkloadRequestDigest: "digest"}
	for range 2 {
		out, err := client.CreateJobV2(context.Background(), request)
		if err != nil || out.RequestID != "loc-generated-broker-id" {
			t.Fatalf("generated broker identity rejected: %v %v", out, err)
		}
	}
	request.RequestID = ""
	if _, err := client.CreateJobV2(context.Background(), request); err == nil {
		t.Fatal("unpersisted issuance allowed")
	}
	if calls != 2 {
		t.Fatalf("unexpected issuance calls %d", calls)
	}
}
func TestV2RejectsUnprovedSettlementAndRefillRefusal(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Idempotency-Key") != "refill-1" {
			t.Error("missing refill idempotency key")
		}
		w.WriteHeader(http.StatusPaymentRequired)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "cap_reached", "message": "cumulative cap refused"}})
	}))
	defer server.Close()
	client := NewClient(server.URL, "key", "test/v2", time.Second)
	if _, err := client.SettleJobV2(context.Background(), uuid.New(), SettleJobRequestV2{ActualUnits: 0}); err == nil {
		t.Fatal("synthetic zero settlement allowed")
	}
	if _, err := client.CloseSessionV2(context.Background(), uuid.New(), CloseSessionRequestV2{ActualUnits: 0}); err == nil {
		t.Fatal("synthetic close allowed")
	}
	if calls != 0 {
		t.Fatal("unproved accounting reached LOC")
	}
	if _, err := client.RefillSessionV2(context.Background(), uuid.New(), RefillSessionRequestV2{RequestID: "refill-1", MaxTotalUnits: 60, WorkloadRequestDigest: "digest"}); !IsInsufficientCredit(err) {
		t.Fatalf("refill refusal lost: %v", err)
	}
	if calls != 1 {
		t.Fatal("refill refusal retried")
	}
}
