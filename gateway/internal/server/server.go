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

	// Embedded SPAs — site at /, portal at /portal/, admin at /admin/.
	// Falls back to 404 for /api/* paths chi didn't otherwise match so
	// callers see proper "endpoint not found" instead of an HTML page.
	mountSPAs(r)

	return r
}

// pathAwareAuth dispatches to the right auth handler based on URL prefix.
// All API routes now live under /api/*. Auth dispatch by prefix:
//   /api/v1/*        Bearer API-key + rate-limit (customer-facing API)
//   /api/admin/*     X-Admin-Token
//   /api/portal/*    cookie session (login + logout pass through)
//   /api/public/*    no auth (waitlist signup, email verification)
//   /api/webhooks/*  no auth (HMAC verified in-handler)
//   everything else  no auth (static SPA serving, /health, /metrics)
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
			case strings.HasPrefix(p, "/api/v1/"):
				bearerThenRate.ServeHTTP(w, r)
			case p == "/api/portal/login" || p == "/api/portal/logout":
				next.ServeHTTP(w, r)
			case strings.HasPrefix(p, "/api/portal/"):
				portalGated.ServeHTTP(w, r)
			case strings.HasPrefix(p, "/api/admin/"):
				adminGated.ServeHTTP(w, r)
			case strings.HasPrefix(p, "/api/public/"),
				strings.HasPrefix(p, "/api/webhooks/"):
				// public + webhook surfaces — in-handler auth (HMAC for
				// webhooks, none for public signup/verify).
				next.ServeHTTP(w, r)
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
