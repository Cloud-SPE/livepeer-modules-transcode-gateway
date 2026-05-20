// Package rtmp hosts the gateway-owned RTMP ingest plane for the
// live-session-gateway-ingest@v0 protocol mode. Customers push RTMP
// to `rtmp://<gateway>:1935/live/<stream_key>`; this server authenticates
// the stream key against the live_streams table, looks up which orch
// the session is bound to, and (in Phase 6) relays bytes upstream.
//
// Phase 2 scope (this file): TCP listener bootstrap + handler skeleton
// + stream-key auth. The relay step is wired separately so the auth
// path can land + be tested independently.
package rtmp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	rtmp "github.com/yutopp/go-rtmp"
)

// Deps captures the dependencies the RTMP server needs without coupling
// to the rest of the server package. Mirrors the pattern used elsewhere
// (handlers' Deps struct).
type Deps struct {
	Log      *slog.Logger
	Auth     Authenticator
	Pepper   string  // IP_HASH_PEPPER, reused for stream_key hashing
	// Future: Relay, Metrics, CredentialMinter
}

// Authenticator is what the RTMP handler calls to verify a stream key.
// Returns the session row when valid + active; nil when unknown or
// terminal. Pure interface so tests can stub it without spinning up
// postgres.
type Authenticator interface {
	AuthenticateStreamKey(ctx context.Context, peppered string) (*AuthResult, error)
}

// AuthResult is the minimum the handler needs to proceed. We don't
// expose the entire LiveStream row here — keeps the package boundary
// clean.
type AuthResult struct {
	LiveStreamID     string // live_streams.id (UUID string)
	APIKeyID         string
	PrivateIngestURL string // empty until broker session is opened
	BrokerSessionID  string // empty until broker session is opened
}

// Server is the RTMP TCP listener + accept loop. Cheap to construct;
// expensive only when Run is invoked.
type Server struct {
	deps     Deps
	addr     string
	listener net.Listener
	rtmpSrv  *rtmp.Server
	wg       sync.WaitGroup
	closing  atomic.Bool
	stats    serverStats
}

type serverStats struct {
	activePublishes atomic.Int64
	totalAccepted   atomic.Int64
	totalRejected   atomic.Int64
}

// New constructs an RTMP server bound to host:port. Doesn't bind the
// socket — Run does that. Returns a Server even when port=0 (disabled)
// so callers can treat it uniformly; Run is a no-op in that case.
func New(deps Deps, host string, port int) *Server {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	srv := &Server{deps: deps}
	if port > 0 {
		srv.addr = net.JoinHostPort(host, strconv.Itoa(port))
	}
	return srv
}

// Run binds the TCP listener and serves until ctx is canceled. Blocks
// in the caller's goroutine until shutdown completes. Caller is
// expected to invoke on its own goroutine.
func (s *Server) Run(ctx context.Context) error {
	if s.addr == "" {
		s.deps.Log.Info("rtmp server disabled (LIVE_RTMP_PORT=0)")
		<-ctx.Done()
		return nil
	}

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.listener = ln
	s.deps.Log.Info("rtmp server listening", "addr", s.addr)

	// yutopp/go-rtmp's Server wraps an accept loop and invokes our
	// Handler per connection. The OnConnHandler closure is where we
	// inject per-connection state.
	srv := rtmp.NewServer(&rtmp.ServerConfig{
		OnConnect: func(conn net.Conn) (io.ReadWriteCloser, *rtmp.ConnConfig) {
			s.stats.totalAccepted.Add(1)
			return conn, &rtmp.ConnConfig{
				Handler: newConnHandler(s.deps, &s.stats),
				ControlState: rtmp.StreamControlStateConfig{
					DefaultBandwidthWindowSize: 6 * 1024 * 1024 / 8,
				},
			}
		},
	})
	s.rtmpSrv = srv

	// Trigger shutdown when ctx cancels.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		<-ctx.Done()
		s.closing.Store(true)
		s.deps.Log.Info("rtmp server shutting down",
			"active_publishes", s.stats.activePublishes.Load())
		_ = ln.Close()
	}()

	if err := srv.Serve(ln); err != nil && !errors.Is(err, net.ErrClosed) {
		s.deps.Log.Error("rtmp server serve failed", "err", err)
		return err
	}

	// Wait for in-flight connections to finish draining (best-effort
	// 30s budget; production deployments would use a longer timeout).
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		s.deps.Log.Warn("rtmp server drain timeout; forcing exit",
			"active_publishes", s.stats.activePublishes.Load())
	}
	return nil
}

// ActivePublishes returns the current count of authenticated publishes.
// Useful for /health and metrics.
func (s *Server) ActivePublishes() int64 {
	return s.stats.activePublishes.Load()
}

// Listening reports whether Run has bound a TCP listener. Returns false
// when the server is disabled (LIVE_RTMP_PORT=0) or hasn't started yet.
func (s *Server) Listening() bool {
	return s.listener != nil && !s.closing.Load()
}
