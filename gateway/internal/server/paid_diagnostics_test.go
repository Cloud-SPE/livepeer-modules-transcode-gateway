package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/livepeer"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/loc"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"
	"github.com/google/uuid"
)

func TestPaidDiagnosticsTerminalRefusal(t *testing.T) {
	var buf bytes.Buffer
	e := &PaidEngine{deps: Deps{Log: slog.New(slog.NewJSONHandler(&buf, nil))}}
	now := time.Now()
	o := &repo.PaidOperation{ID: uuid.New(), Kind: "abr", State: "issuance_refused", Attempts: 1, FinishedAt: &now}
	err := &loc.APIError{StatusCode: 422, Code: "AUTHORIZATION_REFUSED", Message: "secret-message", Details: map[string]any{"reason": "secret-prefix max_debit 1629000000000000 wei exceeds max-authorization-wei 1500000000000000 secret-suffix"}}
	e.logPaidError(o, &operationSecrets{}, "operation", err)
	var log map[string]any
	if json.Unmarshal(buf.Bytes(), &log) != nil {
		t.Fatal("invalid log")
	}
	if log["msg"] != "paid operation terminal refusal" || log["retryable"] != false || log["next_attempt_at"] != nil {
		t.Fatalf("wrong terminal fields: %v", log)
	}
	if log["loc_code"] != "AUTHORIZATION_REFUSED" || log["requested_max_debit_wei"] != "1629000000000000" || log["payer_max_authorization_wei"] != "1500000000000000" {
		t.Fatalf("missing refusal diagnostics: %v", log)
	}
	if strings.Contains(buf.String(), "secret-") {
		t.Fatal("upstream message leaked")
	}
}

func TestPaidDiagnosticsRecoveryAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"broker", &livepeer.BrokerError{StatusCode: 402, URL: "https://secret-host/path?token=secret-token", Body: "secret-body"}, "broker_http_402"},
		{"unknown loc", &loc.APIError{StatusCode: 422, Code: "secret-code", Message: "secret-message", Details: map[string]any{"input": "secret-input"}}, "loc_http_422"},
		{"unknown", errors.New("secret-error"), "upstream_or_accounting_pending"},
		{"admission", &livepeer.ExchangePendingError{Outcome: "ADMISSION_REJECTED"}, "broker_admission_rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			e := &PaidEngine{deps: Deps{Log: slog.New(slog.NewJSONHandler(&buf, nil))}}
			id := uuid.New()
			o := &repo.PaidOperation{ID: id, Kind: "abr", State: "dispatching", Attempts: 23, NextAttemptAt: time.Now().Add(time.Minute)}
			s := &operationSecrets{Job: &loc.CreateJobResponseV2{JobID: id, RequestID: "request-1", SpendAuthorization: "secret-authorization"}}
			e.logPaidError(o, s, "operation", tc.err)
			var log map[string]any
			if json.Unmarshal(buf.Bytes(), &log) != nil {
				t.Fatal("invalid log")
			}
			if log["code"] != tc.code || log["retryable"] != true || log["next_attempt_at"] == nil || log["broker_request_id"] != "request-1" || log["loc_job_id"] != id.String() {
				t.Fatalf("wrong diagnostics: %v", log)
			}
			if strings.Contains(buf.String(), "secret-") {
				t.Fatal("secret leaked")
			}
			if tc.name == "admission" && log["recovery_action"] != "await_loc_signed_non_admission" {
				t.Fatal("missing recovery guidance")
			}
			if o.FinishedAt != nil || o.State != "dispatching" {
				t.Fatal("diagnostics changed recovery state")
			}
		})
	}
}
