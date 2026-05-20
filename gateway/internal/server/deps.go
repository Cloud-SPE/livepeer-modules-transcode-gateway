package server

import (
	"log/slog"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/config"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/email"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/metrics"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/livepeer"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/service"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/s3"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Deps is the bundle threaded into every handler. Owned by main.go;
// handlers consume by value (deps is small — a few pointers).
type Deps struct {
	Cfg      config.Config
	Log      *slog.Logger
	Pool     *pgxpool.Pool
	Waitlist *repo.WaitlistRepo
	APIKeys  *repo.APIKeyRepo
	Sessions *repo.SessionRepo
	Usage    *repo.ReservationRepo
	Live     *repo.LiveRepo
	Caps     *repo.CapabilityRepo
	Email    *email.Mailer
	S3       *s3.Client
	Payer    *livepeer.PayerClient
	Resolver *service.RouteSelector
	Health   *service.Health
	HTTP     *livepeer.HTTPClient
	CapMap   livepeer.CapabilityMap
	Metrics  *metrics.Registry
	// RTMPProbe is a cheap readiness check the /health handler calls
	// for the gateway-side RTMP ingest. Implemented in the rtmp package;
	// nil when LIVE_RTMP_PORT is unset.
	RTMPProbe RTMPProbe
}

// RTMPProbe is the interface /health uses to query the RTMP server's
// current state. Decoupled so the server package doesn't import the
// rtmp package directly (avoids the circular-import risk if rtmp ever
// needs anything from server).
type RTMPProbe interface {
	// ActivePublishes returns the current count of authenticated
	// publishes. Cheap; safe to call from a hot path.
	ActivePublishes() int64
	// Listening reports whether the listener socket is bound.
	Listening() bool
}
