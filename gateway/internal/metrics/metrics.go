package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Registry holds the gateway's prom registry + the metric handles we
// reference from handlers and the proxy layer.
type Registry struct {
	Reg *prometheus.Registry

	HTTPRequests             *prometheus.CounterVec
	HTTPDuration             *prometheus.HistogramVec
	ProxyReservationsTotal   *prometheus.CounterVec
	LiveStreamsActive        prometheus.Gauge
	WaitlistSignupsTotal     prometheus.Counter
	RouteHealthCooldowns     *prometheus.CounterVec
	SessionRotationRetries   *prometheus.CounterVec
}

func New() *Registry {
	r := prometheus.NewRegistry()
	f := promauto.With(r)
	return &Registry{
		Reg: r,
		HTTPRequests: f.NewCounterVec(prometheus.CounterOpts{
			Name: "video_gateway_http_requests_total",
			Help: "Total HTTP requests handled by the gateway, by method/route/status.",
		}, []string{"method", "route", "status"}),
		HTTPDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "video_gateway_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds, by method/route/status.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route", "status"}),
		ProxyReservationsTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "video_gateway_proxy_reservations_total",
			Help: "Reservation outcomes by capability and outcome.",
		}, []string{"capability", "outcome"}),
		LiveStreamsActive: f.NewGauge(prometheus.GaugeOpts{
			Name: "video_gateway_live_streams_active",
			Help: "Current count of live_streams in 'live' status.",
		}),
		WaitlistSignupsTotal: f.NewCounter(prometheus.CounterOpts{
			Name: "video_gateway_waitlist_signups_total",
			Help: "Total waitlist signup attempts that produced a new row.",
		}),
		RouteHealthCooldowns: f.NewCounterVec(prometheus.CounterOpts{
			Name: "livepeer_gateway_route_health_cooldowns_opened_total",
			Help: "Number of cooldown windows opened by route health, by capability.",
		}, []string{"capability"}),
		// Tracks the v1.3.1 payment-daemon session-rotation retry path.
		// Outcomes:
		//   - succeeded:    rotation detected, ReportPaymentResult + re-mint + retry returned 2xx
		//   - report_failed: ReportPaymentResult RPC failed (daemon unreachable, etc.)
		//   - retry_failed:  re-mint succeeded but the second broker call still failed
		//   - mint_failed:   re-mint itself failed (daemon evicted but couldn't fetch fresh params)
		// Healthy production: very low rate; spike = orchestrator-side rotation activity worth investigating.
		SessionRotationRetries: f.NewCounterVec(prometheus.CounterOpts{
			Name: "livepeer_gateway_session_rotation_retries_total",
			Help: "INVALID_RECIPIENT_RAND retries observed by the dispatcher, by capability and outcome.",
		}, []string{"capability", "outcome"}),
	}
}
