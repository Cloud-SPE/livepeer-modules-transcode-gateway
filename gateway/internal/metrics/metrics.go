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
	LiveTopupAttempts        *prometheus.CounterVec
	// Plan 0003: gateway-side RTMP ingest plane.
	RTMPActivePublishes      prometheus.GaugeFunc
	RTMPPublishesTotal       *prometheus.CounterVec // labels: outcome (accepted | rejected)
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
		// Outcomes: succeeded, mint_failed, broker_failed, resolver_failed.
		// Fires from the live reconciler's auto-topup path. Distinct from
		// SessionRotationRetries because a top-up triggered by runway
		// exhaustion is a different signal than a session-rand rotation.
		LiveTopupAttempts: f.NewCounterVec(prometheus.CounterOpts{
			Name: "livepeer_gateway_live_topup_attempts_total",
			Help: "Auto-topup attempts triggered by the live reconciler, by capability and outcome.",
		}, []string{"capability", "outcome"}),
		RTMPPublishesTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "livepeer_gateway_rtmp_publishes_total",
			Help: "RTMP publish authentication outcomes (plan 0003).",
		}, []string{"outcome"}),
	}
}

// AttachRTMPGauge wires the RTMP server's active-publishes counter to a
// Prometheus gauge. Called once after the RTMP server is constructed so
// the metric reflects live state without us pushing every change.
func (r *Registry) AttachRTMPGauge(reader func() int64) {
	if r == nil || reader == nil {
		return
	}
	r.RTMPActivePublishes = promauto.With(r.Reg).NewGaugeFunc(prometheus.GaugeOpts{
		Name: "livepeer_gateway_rtmp_active_publishes",
		Help: "Current number of authenticated RTMP publishes the gateway is relaying (plan 0003).",
	}, func() float64 { return float64(reader()) })
}
