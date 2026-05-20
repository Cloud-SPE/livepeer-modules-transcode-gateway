package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/crypto"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// New wires the chi router + a single huma API and returns an
// http.Handler. Auth gates are URL-prefix-aware so we keep one OpenAPI
// document for the whole surface.
func New(deps Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(RequestID())
	r.Use(AccessLog(deps.Log))
	r.Use(CORS(deps.Cfg.AllowedOrigins))
	r.Use(pathAwareAuth(deps))

	// Prometheus on /metrics with optional bearer gate.
	r.Get("/metrics", func(w http.ResponseWriter, req *http.Request) {
		if deps.Cfg.MetricsToken != "" {
			tok := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
			if !crypto.ConstantTimeEqual(tok, deps.Cfg.MetricsToken) {
				http.Error(w, "metrics token required", http.StatusUnauthorized)
				return
			}
		}
		promhttp.HandlerFor(deps.Metrics.Reg, promhttp.HandlerOpts{}).ServeHTTP(w, req)
	})

	humaConfig := huma.DefaultConfig("Livepeer Video Gateway", "0.1.0")
	humaConfig.Info.Description = "VOD ABR + RTMP→HLS live transcode gateway backed by the Livepeer network."
	api := humachi.New(r, humaConfig)

	RegisterHealth(api, deps)
	RegisterPublic(api, deps)
	RegisterPortal(api, deps)
	RegisterAdmin(api, deps)
	RegisterV1(api, deps)
	// Webhook receiver bypasses huma (raw-body HMAC verification needs
	// untouched bytes, and huma's body parsing would re-marshal).
	MountCallbacks(r, deps)

	return r
}

// pathAwareAuth dispatches to the right auth handler based on URL prefix.
// /portal/login + /portal/logout pass through; everything else under
// /portal/* needs a cookie session. /admin/* needs X-Admin-Token. /v1/*
// needs Bearer auth + rate limit.
func pathAwareAuth(deps Deps) func(http.Handler) http.Handler {
	rl := NewRateLimit(deps.Cfg.V1RateLimitPerMinute, deps.Cfg.V1RateLimitBurst)
	bearer := RequireAPIKey(deps)
	rate := EnforceRateLimit(rl)
	portal := RequirePortalSession(deps)
	admin := RequireAdmin(deps)

	return func(next http.Handler) http.Handler {
		bearerThenRate := bearer(rate(next))
		portalGated := portal(next)
		adminGated := admin(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := r.URL.Path
			switch {
			case strings.HasPrefix(p, "/v1/"):
				bearerThenRate.ServeHTTP(w, r)
			case p == "/portal/login" || p == "/portal/logout":
				next.ServeHTTP(w, r)
			case strings.HasPrefix(p, "/api/"):
				// /api/* (incl. /api/abr/callback) is public — auth is
				// in-handler (HMAC for webhooks, none for waitlist).
				next.ServeHTTP(w, r)
			case strings.HasPrefix(p, "/portal/"):
				portalGated.ServeHTTP(w, r)
			case strings.HasPrefix(p, "/admin/"):
				adminGated.ServeHTTP(w, r)
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}

// Defaults for the http.Server.
const (
	DefaultReadTimeout  = 30 * time.Second
	DefaultWriteTimeout = 60 * time.Second
)
