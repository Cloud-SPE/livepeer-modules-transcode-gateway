package server

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

type HealthCheck struct {
	Status    string `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

type HealthOut struct {
	Body struct {
		Status string                 `json:"status"`
		Checks map[string]HealthCheck `json:"checks"`
	}
}

func RegisterHealth(api huma.API, deps Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "health",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "Load-balancer health check",
		Tags:        []string{"ops"},
	}, func(ctx context.Context, _ *struct{}) (*HealthOut, error) {
		out := &HealthOut{}
		out.Body.Checks = map[string]HealthCheck{}

		// DB
		dbState := HealthCheck{Status: "ok"}
		t := time.Now()
		if err := deps.Pool.Ping(ctx); err != nil {
			dbState = HealthCheck{Status: "error", Error: err.Error()}
		}
		dbState.LatencyMs = time.Since(t).Milliseconds()
		out.Body.Checks["db"] = dbState

		// RustFS (S3)
		s3State := HealthCheck{Status: "skipped"}
		t = time.Now()
		if deps.S3 != nil {
			if err := deps.S3.HeadBucket(ctx); err != nil {
				s3State = HealthCheck{Status: "error", Error: err.Error()}
			} else {
				s3State = HealthCheck{Status: "ok"}
			}
		}
		s3State.LatencyMs = time.Since(t).Milliseconds()
		out.Body.Checks["rustfs"] = s3State

		// payer
		pState := HealthCheck{Status: "skipped"}
		t = time.Now()
		if deps.Payer != nil {
			if err := deps.Payer.Health(ctx); err != nil {
				pState = HealthCheck{Status: "error", Error: err.Error()}
			} else {
				pState = HealthCheck{Status: "ok"}
			}
		}
		pState.LatencyMs = time.Since(t).Milliseconds()
		out.Body.Checks["payer"] = pState

		// resolver
		rState := HealthCheck{Status: "skipped"}
		t = time.Now()
		if deps.Resolver != nil {
			if err := deps.Resolver.Health(ctx); err != nil {
				rState = HealthCheck{Status: "error", Error: err.Error()}
			} else {
				rState = HealthCheck{Status: "ok"}
			}
		}
		rState.LatencyMs = time.Since(t).Milliseconds()
		out.Body.Checks["registry"] = rState

		switch {
		case dbState.Status == "error":
			out.Body.Status = "down"
			return out, huma.Error503ServiceUnavailable("db_down")
		case s3State.Status == "error" || pState.Status == "error" || rState.Status == "error":
			out.Body.Status = "degraded"
		default:
			out.Body.Status = "ok"
		}
		return out, nil
	})
}
