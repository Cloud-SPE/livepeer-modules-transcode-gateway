// Package loctest provides an httptest fake of the LOC API for gateway
// tests. Defaults simulate the happy path; individual endpoints can be
// overridden per-test for error injection (402 INSUFFICIENT_CREDIT,
// 404 NO_ROUTE_AVAILABLE, 429 + Retry-After, ...).
package loctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/google/uuid"
)

// Fake is one in-memory LOC. Zero value isn't usable — construct with New.
type Fake struct {
	Server *httptest.Server

	// BrokerURL is returned in create-job/open-session responses. Point
	// it at a second httptest server playing the broker.
	BrokerURL string

	// Overrides. Nil → default behavior.
	CreateJob   http.HandlerFunc
	SettleJob   http.HandlerFunc
	OpenSession http.HandlerFunc
	Refill      http.HandlerFunc
	Close       http.HandlerFunc
	ListCaps    http.HandlerFunc

	mu          sync.Mutex
	createCalls int
	settleCalls map[string]int // job id → settle attempts
	settled     map[string]int64
	refillSeq   int
}

func New() *Fake {
	f := &Fake{
		settleCalls: map[string]int{},
		settled:     map[string]int64{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.createCalls++
		f.mu.Unlock()
		if f.CreateJob != nil {
			f.CreateJob(w, r)
			return
		}
		f.defaultCreateJob(w, r)
	})
	mux.HandleFunc("POST /v1/jobs/{id}/settle", func(w http.ResponseWriter, r *http.Request) {
		if f.SettleJob != nil {
			f.SettleJob(w, r)
			return
		}
		f.defaultSettleJob(w, r)
	})
	mux.HandleFunc("GET /v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		if f.ListCaps != nil {
			f.ListCaps(w, r)
			return
		}
		WriteJSON(w, 200, map[string]any{"items": []map[string]any{{
			"name":      "video:transcode.abr",
			"work_unit": "video-frame-megapixel",
			"offerings": []map[string]any{{
				"id":                      "abr-default",
				"price_per_work_unit_wei": "1000",
				"work_unit":               "video-frame-megapixel",
			}},
		}}})
	})
	f.Server = httptest.NewServer(mux)
	return f
}

func (f *Fake) URL() string { return f.Server.URL }

func (f *Fake) CloseServer() { f.Server.Close() }

// CreateCalls reports how many create-job calls landed (override or not).
func (f *Fake) CreateCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createCalls
}

// SettledUnits returns the actual_units recorded for a job id, with ok
// reporting whether the job was settled at all.
func (f *Fake) SettledUnits(jobID string) (int64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.settled[jobID]
	return u, ok
}

func (f *Fake) defaultCreateJob(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, 201, map[string]any{"job_id": uuid.NewString(), "work_id": "authorization-id", "request_id": "broker-request-id", "broker_url": f.BrokerURL, "protocol": "paid-job/v1", "transport": "stream", "accounting_mode": "wholesale_account", "work_unit": "units", "spend_authorization": "fixture"})
}

func (f *Fake) defaultSettleJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	var in struct {
		ActualUnits int64  `json:"actual_units"`
		Outcome     string `json:"outcome"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	f.mu.Lock()
	f.settleCalls[jobID]++
	_, dup := f.settled[jobID]
	if !dup {
		f.settled[jobID] = in.ActualUnits
	}
	f.mu.Unlock()
	if dup {
		WriteError(w, 409, "job_already_settled", "job already settled")
		return
	}
	WriteJSON(w, 200, map[string]any{
		"job_id":           jobID,
		"work_id":          "deadbeef",
		"actual_units":     in.ActualUnits,
		"billed_value_wei": json.Number(fmt.Sprintf("%d", in.ActualUnits*500)),
		"refund_wei":       json.Number("100"),
		"outcome":          nonEmpty(in.Outcome, "EXACT"),
		"closed_at":        "2026-01-01T00:01:00Z",
		"cap_status":       defaultCapStatus(false),
	})
}

func defaultCapStatus(winddown bool) map[string]any {
	cs := map[string]any{
		"session_pct_used":        0.1,
		"spend_period_pct_used":   nil,
		"user_balance_pct_used":   0.2,
		"operator_pool_pct_used":  nil,
		"will_refuse_next_refill": winddown,
		"winddown_reason":         nil,
	}
	if winddown {
		cs["winddown_reason"] = "spend_period_cap_imminent"
	}
	return cs
}

// WriteJSON writes a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// WriteError writes LOC's canonical error envelope.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, map[string]any{"error": map[string]any{
		"code":    code,
		"message": message,
		"details": map[string]any{},
	}})
}

// WriteRateLimited writes a 429 with Retry-After.
func WriteRateLimited(w http.ResponseWriter, retryAfterSecs int) {
	w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSecs))
	WriteError(w, 429, "rate_limited", "slow down")
}

func nonEmpty(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
