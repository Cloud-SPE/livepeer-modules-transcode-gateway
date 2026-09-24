package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/livepeer"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/loc"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"
)

// Extract only the documented numeric refusal, never the surrounding upstream
// message (which may include URLs, input values, or credentials).
var authorizationLimit = regexp.MustCompile(`\bmax_debit ([0-9]{1,78}) wei exceeds max-authorization-wei ([0-9]{1,78})\b`)

func safePaidError(err error) string {
	var ae *loc.APIError
	if errors.As(err, &ae) {
		return fmt.Sprintf("loc_http_%d", ae.StatusCode)
	}
	var be *livepeer.BrokerError
	if errors.As(err, &be) {
		return fmt.Sprintf("broker_http_%d", be.StatusCode)
	}
	var exchange *livepeer.ExchangePendingError
	if errors.As(err, &exchange) {
		return "broker_" + strings.ToLower(exchange.Outcome)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "upstream_timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "upstream_timeout"
	}
	return "upstream_or_accounting_pending"
}

func paidErrorFields(err error) []any {
	fields := []any{"code", safePaidError(err)}
	var ae *loc.APIError
	if errors.As(err, &ae) {
		fields = append(fields, "upstream", "loc", "http_status", ae.StatusCode)
		// A syntactically valid arbitrary code could still be a secret. Allowlist
		// known protocol codes rather than logging arbitrary upstream strings.
		switch code := strings.ToUpper(ae.Code); code {
		case "AUTHORIZATION_REFUSED", "INSUFFICIENT_CREDIT", "SPEND_CAP_EXCEEDED", "DAEMON_UNAVAILABLE", "WHOLESALE_FUNDING_UNVERIFIED", "NO_ROUTE_AVAILABLE", "IDEMPOTENCY_IN_PROGRESS", "VALIDATION_ERROR":
			fields = append(fields, "loc_code", code)
		}
		if strings.EqualFold(ae.Code, "AUTHORIZATION_REFUSED") {
			reason, _ := ae.Details["reason"].(string)
			if match := authorizationLimit.FindStringSubmatch(reason); match != nil {
				fields = append(fields, "reason_code", "authorization_limit_exceeded", "requested_max_debit_wei", match[1], "payer_max_authorization_wei", match[2])
			}
		}
	}
	var be *livepeer.BrokerError
	if errors.As(err, &be) {
		fields = append(fields, "upstream", "broker", "http_status", be.StatusCode)
		// Classify a route without exposing hostnames, IDs, or query tokens.
		if u, parseErr := url.Parse(be.URL); parseErr == nil {
			endpoint := "unknown"
			switch {
			case u.Path == "/v1/job":
				endpoint = "job_submit"
			case strings.HasPrefix(u.Path, "/v1/exchange/"):
				endpoint = "job_exchange"
			case u.Path == "/v1/session":
				endpoint = "session_open"
			case strings.HasPrefix(u.Path, "/v1/session/") && strings.HasSuffix(u.Path, "/end"):
				endpoint = "session_end"
			case strings.HasPrefix(u.Path, "/v1/session/") && strings.HasSuffix(u.Path, "/topup"):
				endpoint = "session_topup"
			case strings.HasPrefix(u.Path, "/v1/session/"):
				endpoint = "session_status"
			case strings.HasPrefix(u.Path, "/v1/settlement/"):
				endpoint = "session_settlement"
			}
			fields = append(fields, "broker_endpoint", endpoint)
		}
	}
	var exchange *livepeer.ExchangePendingError
	if errors.As(err, &exchange) {
		fields = append(fields, "upstream", "broker", "broker_outcome", exchange.Outcome)
		if exchange.Outcome == "ADMISSION_REJECTED" {
			fields = append(fields, "recovery_action", "await_loc_signed_non_admission")
		}
	}
	return fields
}

func (e *PaidEngine) logPaidError(o *repo.PaidOperation, s *operationSecrets, stage string, err error) {
	terminal := o.FinishedAt != nil
	message := "paid operation pending retry"
	if stage == "broker_exchange_lookup" {
		message = "paid operation broker recovery pending"
	}
	if terminal {
		message = "paid operation terminal refusal"
	}
	fields := []any{"operation_id", o.ID, "kind", o.Kind, "state", o.State, "stage", stage, "attempt", o.Attempts, "stop_requested", o.StopRequested, "retryable", !terminal}
	if !terminal && stage == "operation" {
		fields = append(fields, "next_attempt_at", o.NextAttemptAt)
	}
	if s.Job != nil {
		fields = append(fields, "loc_job_id", s.Job.JobID, "broker_request_id", s.Job.RequestID)
	}
	if s.Session != nil {
		fields = append(fields, "loc_session_id", s.Session.SessionID, "broker_request_id", s.Session.RequestID)
	}
	fields = append(fields, paidErrorFields(err)...)
	e.deps.Log.Warn(message, fields...)
}
