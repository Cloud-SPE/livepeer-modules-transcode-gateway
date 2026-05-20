package rtmp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = dialCtx // yutopp/go-rtmp's Dial doesn't take a ctx; we rely on default Dial timeout

	conn, err := rtmp.Dial("rtmp", host, &rtmp.ConnConfig{})
	if err != nil {
		return nil, fmt.Errorf("rtmp dial %s: %w", host, err)
	}

	// connect → createStream → publish, mirroring what OBS does.
	if err := conn.Connect(&rtmpmsg.NetConnectionConnect{
		Command: rtmpmsg.NetConnectionConnectCommand{
			App:      app,
			Type:     "nonprivate",
			FlashVer: "FMLE/3.0 (compatible; gateway-relay)",
			TCURL:    fmt.Sprintf("rtmp://%s/%s", host, app),
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
		return nil, fmt.Errorf("rtmp publish (key=...%s): %w", lastN(streamKey, 4), err)
	}

	log.Info("rtmp relay: upstream publish opened",
		"upstream_host", host, "app", app, "stream_key_hint", lastN(streamKey, 4))

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
	if !strings.HasPrefix(raw, "rtmp://") && !strings.HasPrefix(raw, "rtmps://") {
		return "", "", "", fmt.Errorf("rtmp url must start with rtmp:// or rtmps://, got %q", raw)
	}
	u, perr := url.Parse(raw)
	if perr != nil {
		return "", "", "", fmt.Errorf("parse %s: %w", raw, perr)
	}
	host = u.Host
	if host == "" {
		return "", "", "", fmt.Errorf("rtmp url missing host: %q", raw)
	}
	// Path like `/live/lvk_xyz` — split on the first segment for `app`,
	// rest is the stream key.
	path := strings.TrimPrefix(u.Path, "/")
	if path == "" {
		return "", "", "", fmt.Errorf("rtmp url missing path: %q", raw)
	}
	if i := strings.Index(path, "/"); i > 0 {
		app = path[:i]
		streamKey = path[i+1:]
	} else {
		app = path
		streamKey = ""
	}
	return host, app, streamKey, nil
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
