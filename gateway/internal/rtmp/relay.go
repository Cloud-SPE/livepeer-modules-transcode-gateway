package rtmp

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	rtmp "github.com/yutopp/go-rtmp"
	rtmpmsg "github.com/yutopp/go-rtmp/message"
)

// Relay is one outbound RTMP push connection. The gateway opens one of
// these per authenticated incoming publish: customer's `OnAudio` /
// `OnVideo` callbacks fan into `WriteAudio` / `WriteVideo` here, which
// forward to the orchestrator's runner.
//
// Concurrency: per-publish goroutine, single writer to the underlying
// stream. The yutopp/go-rtmp Stream isn't safe for concurrent writes;
// we serialize via a mutex held briefly per frame.
type Relay struct {
	upstreamURL string
	logger      logger

	mu     sync.Mutex
	conn   *rtmp.ClientConn
	stream *rtmp.Stream
	closed bool
}

type logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// DialAndPublish opens an RTMP TCP connection to `upstreamURL`, performs
// the handshake, sends connect+createStream+publish, and returns a
// Relay ready to forward frames.
//
// upstreamURL shape: rtmp://host:port/app/streamKey
// We parse to (host:port, app, streamKey) since yutopp/go-rtmp's Dial
// takes only the host:port and we send `app` + `streamKey` via the
// connect / publish commands.
func DialAndPublish(ctx context.Context, upstreamURL string, log logger) (*Relay, error) {
	host, app, streamKey, err := parseRTMPURL(upstreamURL)
	if err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scheme := "rtmp"
	if strings.HasPrefix(upstreamURL, "rtmps://") {
		scheme = "rtmps"
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if deadline, ok := ctx.Deadline(); ok {
		dialer.Deadline = deadline
	}
	var conn *rtmp.ClientConn
	if scheme == "rtmps" {
		conn, err = rtmp.DialWithTLSDialer(&tls.Dialer{NetDialer: dialer, Config: &tls.Config{MinVersion: tls.VersionTLS12}}, scheme, host, &rtmp.ConnConfig{})
	} else {
		conn, err = rtmp.DialWithDialer(dialer, scheme, host, &rtmp.ConnConfig{})
	}
	if err != nil {
		return nil, fmt.Errorf("rtmp dial %s: %w", host, err)
	}

	// connect → createStream → publish, mirroring what OBS does.
	if err := conn.Connect(&rtmpmsg.NetConnectionConnect{
		Command: rtmpmsg.NetConnectionConnectCommand{
			App:      app,
			Type:     "nonprivate",
			FlashVer: "FMLE/3.0 (compatible; gateway-relay)",
			TCURL:    fmt.Sprintf("%s://%s/%s", scheme, host, app),
		},
	}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("rtmp connect (app=%s): %w", app, err)
	}

	stream, err := conn.CreateStream(&rtmpmsg.NetConnectionCreateStream{}, 1024)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("rtmp createStream: %w", err)
	}

	if err := stream.Publish(&rtmpmsg.NetStreamPublish{
		CommandObject:  nil,
		PublishingName: streamKey,
		PublishingType: "live",
	}); err != nil {
		_ = conn.Close()
		return nil, errors.New("rtmp upstream publish rejected")
	}

	log.Info("rtmp relay: upstream publish opened",
		"upstream_host", host)

	return &Relay{
		upstreamURL: upstreamURL,
		logger:      log,
		conn:        conn,
		stream:      stream,
	}, nil
}

// WriteAudio forwards a customer-side audio FLV tag to the upstream.
// payload is the io.Reader passed to the customer's OnAudio callback.
func (r *Relay) WriteAudio(timestamp uint32, payload io.Reader) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.stream == nil {
		return errors.New("relay closed")
	}
	// The yutopp/go-rtmp message-write API uses chunk stream id 6 by
	// convention for audio frames published from a client.
	buf, err := io.ReadAll(payload)
	if err != nil {
		return err
	}
	return r.stream.Write(6, timestamp, &rtmpmsg.AudioMessage{Payload: bytes.NewReader(buf)})
}

// WriteVideo forwards a customer-side video FLV tag to the upstream.
func (r *Relay) WriteVideo(timestamp uint32, payload io.Reader) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.stream == nil {
		return errors.New("relay closed")
	}
	buf, err := io.ReadAll(payload)
	if err != nil {
		return err
	}
	// Chunk stream id 7 for video by convention.
	return r.stream.Write(7, timestamp, &rtmpmsg.VideoMessage{Payload: bytes.NewReader(buf)})
}

// Close tears down the upstream connection. Idempotent.
func (r *Relay) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.conn != nil {
		err := r.conn.Close()
		r.conn = nil
		r.stream = nil
		return err
	}
	return nil
}

// parseRTMPURL splits `rtmp://host:port/app/streamKey` into its three
// parts. yutopp/go-rtmp's Dial needs host:port; the publish command
// needs `app` and `streamKey` separately. We don't use net/url alone
// because RTMP URLs have a specific app/key path convention.
func parseRTMPURL(raw string) (host, app, streamKey string, err error) {
	u, perr := url.Parse(raw)
	if perr != nil || (u.Scheme != "rtmp" && u.Scheme != "rtmps") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", "", "", errors.New("invalid upstream RTMP URL")
	}
	port := u.Port()
	if port == "" {
		port = "1935"
		if u.Scheme == "rtmps" {
			port = "443"
		}
	}
	host = net.JoinHostPort(u.Hostname(), port)
	path := strings.TrimPrefix(u.Path, "/")
	app, streamKey, ok := strings.Cut(path, "/")
	if !ok || app == "" || streamKey == "" {
		return "", "", "", errors.New("upstream RTMP URL requires app and stream key")
	}
	// v2 runner keys contain a signed query token. It belongs to the
	// publish name, not the connection URL, and must survive relaying.
	if u.RawQuery != "" {
		streamKey += "?" + u.RawQuery
	}
	return host, app, streamKey, nil
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
