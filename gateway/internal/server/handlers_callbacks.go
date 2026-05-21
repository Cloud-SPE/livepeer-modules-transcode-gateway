package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/crypto"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// MountCallbacks attaches the runner webhook receiver to the chi router
// directly — bypassing huma because we need raw request bytes for HMAC
// verification (huma's body parsing would re-marshal and break the
// signature).
//
// Path: POST /api/webhooks/abr?work_id=<uuid>
// Auth: HMAC-SHA256 over (timestamp + "." + body) using the per-job
//       secret stored on the reservation row.
func MountCallbacks(r chi.Router, deps Deps) {
	r.Post("/api/webhooks/abr", func(w http.ResponseWriter, req *http.Request) {
		workIDStr := req.URL.Query().Get("work_id")
		workID, err := uuid.Parse(workIDStr)
		if err != nil {
			http.Error(w, `{"error":"invalid work_id"}`, http.StatusBadRequest)
			return
		}

		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, `{"error":"read body"}`, http.StatusBadRequest)
			return
		}

		row, err := deps.Usage.GetByWorkID(req.Context(), workID)
		if err != nil || row == nil {
			http.Error(w, `{"error":"reservation not found"}`, http.StatusNotFound)
			return
		}
		if row.WebhookSecret == nil || *row.WebhookSecret == "" {
			http.Error(w, `{"error":"no webhook secret on file"}`, http.StatusBadRequest)
			return
		}

		ts := req.Header.Get("X-Webhook-Timestamp")
		sig := req.Header.Get("X-Webhook-Signature")
		if !verifyHMAC(*row.WebhookSecret, ts, body, sig) {
			deps.Log.Warn("abr webhook: signature mismatch",
				"work_id", workID, "event", req.Header.Get("X-Webhook-Event"))
			http.Error(w, `{"error":"signature mismatch"}`, http.StatusUnauthorized)
			return
		}

		var payload struct {
			Event string          `json:"event"`
			JobID string          `json:"job_id"`
			Data  json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}
		var data struct {
			Status          string  `json:"status"`
			Phase           string  `json:"phase"`
			OverallProgress float64 `json:"overall_progress"`
			Error           string  `json:"error"`
			ErrorCode       string  `json:"error_code"`
			CompletedAt     string  `json:"completed_at"`
		}
		_ = json.Unmarshal(payload.Data, &data)

		var completed *time.Time
		if data.CompletedAt != "" {
			if t, err := time.Parse(time.RFC3339, data.CompletedAt); err == nil {
				completed = &t
			}
		}
		if err := deps.Usage.RecordRunnerWebhook(req.Context(), workID, repo.RunnerStateUpdate{
			Status:      data.Status,
			Phase:       data.Phase,
			Progress:    data.OverallProgress,
			ErrorCode:   data.ErrorCode,
			ErrorText:   data.Error,
			StateJSON:   payload.Data,
			CompletedAt: completed,
		}); err != nil {
			deps.Log.Error("abr webhook: db write failed", "err", err)
			http.Error(w, `{"error":"persist failed"}`, http.StatusInternalServerError)
			return
		}
		deps.Log.Info("abr webhook",
			"work_id", workID, "event", payload.Event,
			"status", data.Status, "phase", data.Phase,
			"progress", data.OverallProgress, "err", data.ErrorCode)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
}

func verifyHMAC(secret, timestamp string, body []byte, want string) bool {
	if secret == "" || want == "" || timestamp == "" {
		return false
	}
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(timestamp + "." + string(body)))
	got := hex.EncodeToString(h.Sum(nil))
	return crypto.ConstantTimeEqual(strings.ToLower(want), strings.ToLower(got))
}
