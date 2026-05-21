package rtmp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/crypto"

	rtmp "github.com/yutopp/go-rtmp"
	rtmpmsg "github.com/yutopp/go-rtmp/message"
)

// connHandler is the per-connection state. yutopp/go-rtmp instantiates
// one per accepted RTMP connection; we keep all auth + relay state here.
type connHandler struct {
	rtmp.DefaultHandler
	deps    Deps
	stats   *serverStats
	server  *Server  // back-reference for relay registry on auth/close
	tcpConn net.Conn // the customer's underlying TCP socket, captured at accept

	mu               sync.Mutex
	logger           *slog.Logger // bound with conn-scoped attrs once known
	authed           bool
	auth             *AuthResult
	streamKeyHint    string // last 4 chars, log-safe
	publishStartedAt time.Time
	relay            *Relay
	closed           bool // OnClose / shutdown idempotency
}

func newConnHandler(deps Deps, stats *serverStats, server *Server, tcpConn net.Conn) *connHandler {
	return &connHandler{
		deps:    deps,
		stats:   stats,
		server:  server,
		tcpConn: tcpConn,
		logger:  deps.Log,
	}
}

// OnConnect runs after the RTMP handshake completes. We accept the
// connection unconditionally and defer authentication to OnPublish
// (the customer's stream key arrives on the publish command, not on
// connect).
func (h *connHandler) OnConnect(_ uint32, cmd *rtmpmsg.NetConnectionConnect) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logger = h.deps.Log.With(
		"rtmp_app", cmd.Command.App,
		"rtmp_tcurl", cmd.Command.TCURL,
	)
	h.logger.Debug("rtmp: connect")
	return nil
}

// OnPublish is the auth gate. The customer's stream key lives in
// `cmd.PublishingName` — Twitch / Mux convention: server URL is
// `rtmp://host/<app>` (we use `live`) and stream key is the second
// argument to OBS's "Stream Key" field. The RTMP `publish` command
// carries the stream key as PublishingName.
func (h *connHandler) OnPublish(_ *rtmp.StreamContext, _ uint32, cmd *rtmpmsg.NetStreamPublish) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	rawKey := strings.TrimSpace(cmd.PublishingName)
	if rawKey == "" {
		h.stats.totalRejected.Add(1)
		h.logger.Warn("rtmp: publish without stream key; rejecting")
		return errors.New("missing stream key")
	}
	h.streamKeyHint = lastFour(rawKey)
	h.logger = h.logger.With("stream_key_hint", h.streamKeyHint)

	peppered := crypto.HashWithPepper(rawKey, h.deps.Pepper)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := h.deps.Auth.AuthenticateStreamKey(ctx, peppered)
	if err != nil {
		h.stats.totalRejected.Add(1)
		h.logger.Warn("rtmp: auth lookup failed", "err", err)
		return errors.New("auth error")
	}
	if result == nil {
		h.stats.totalRejected.Add(1)
		h.logger.Warn("rtmp: unknown stream key")
		return errors.New("unknown stream key")
	}

	h.authed = true
	h.auth = result
	h.publishStartedAt = time.Now()
	h.stats.activePublishes.Add(1)
	h.logger = h.logger.With(
		"live_id", result.LiveStreamID,
		"api_key_id", result.APIKeyID,
	)
	h.logger.Info("rtmp: publish authenticated")

	// Open upstream RTMP push to the orchestrator's private endpoint.
	// PrivateIngestURL is populated when the gateway-ingest broker call
	// succeeded — if it's empty here we're either in a half-initialised
	// state (broker call failed but row stayed provisioning) or the
	// upstream wire isn't ready yet. Reject in either case.
	if result.PrivateIngestURL == "" {
		h.stats.activePublishes.Add(-1)
		h.authed = false
		h.logger.Warn("rtmp: no upstream ingest URL on file; rejecting publish")
		return errors.New("session not ready (no upstream)")
	}

	dialCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	relay, err := DialAndPublish(dialCtx, result.PrivateIngestURL, h.logger)
	if err != nil {
		h.stats.activePublishes.Add(-1)
		h.authed = false
		h.logger.Error("rtmp: upstream relay open failed", "err", err)
		return errors.New("upstream open failed")
	}
	h.relay = relay
	// Register so DELETE /v1/live can synchronously tear us down.
	if h.server != nil {
		h.server.registerRelay(result.LiveStreamID, h)
	}
	return nil
}

// OnAudio / OnVideo fire for every FLV tag the customer pushes. We
// forward each tag synchronously to the upstream relay. If the
// relay's not initialised (auth failed earlier) we drain to keep the
// customer's encoder from backpressuring while the connection ends.
func (h *connHandler) OnAudio(timestamp uint32, payload io.Reader) error {
	h.mu.Lock()
	relay := h.relay
	h.mu.Unlock()
	if relay == nil {
		_, _ = io.Copy(io.Discard, payload)
		return nil
	}
	return relay.WriteAudio(timestamp, payload)
}

func (h *connHandler) OnVideo(timestamp uint32, payload io.Reader) error {
	h.mu.Lock()
	relay := h.relay
	h.mu.Unlock()
	if relay == nil {
		_, _ = io.Copy(io.Discard, payload)
		return nil
	}
	return relay.WriteVideo(timestamp, payload)
}

// OnClose drops the connection — gracefully if the customer hung up,
// or because we returned an error from OnPublish.
func (h *connHandler) OnClose() {
	h.teardown("customer_disconnect")
}

// shutdown is the explicit-teardown entry point used by the HTTP layer
// (DELETE /v1/live → Server.CloseSession). It closes both the upstream
// relay and the customer's TCP socket so the customer's encoder sees a
// disconnection. Safe to call concurrently with OnClose (idempotent).
func (h *connHandler) shutdown(reason string) {
	h.teardown(reason)
	// Force-close the customer's TCP socket so their encoder reports a
	// disconnect immediately, instead of waiting for TCP keepalive.
	// teardown() above only closes the upstream side; the listener-side
	// socket is owned by yutopp/go-rtmp and we close it directly here.
	h.mu.Lock()
	tcp := h.tcpConn
	h.mu.Unlock()
	if tcp != nil {
		_ = tcp.Close()
	}
}

func (h *connHandler) teardown(reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	if h.relay != nil {
		_ = h.relay.Close()
		h.relay = nil
	}
	if h.server != nil && h.auth != nil {
		h.server.deregisterRelay(h.auth.LiveStreamID, h)
	}
	if h.authed {
		h.stats.activePublishes.Add(-1)
		h.logger.Info("rtmp: publish ended",
			"reason", reason,
			"duration_seconds", int(time.Since(h.publishStartedAt).Seconds()))
	}
}

// lastFour returns the last 4 chars of a stream key — log-safe identifier
// that doesn't leak the secret.
func lastFour(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[len(s)-4:]
}
