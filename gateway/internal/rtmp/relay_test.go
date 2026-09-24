package rtmp

import (
	"context"
	rtmplib "github.com/yutopp/go-rtmp"
	rtmpmsg "github.com/yutopp/go-rtmp/message"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRuntimeGrantRTMPURL(t *testing.T) {
	for _, tt := range []struct{ raw, host, app, key string }{
		{"rtmp://runner:1935/live/session?token=signed%2Btoken", "runner:1935", "live", "session?token=signed%2Btoken"},
		{"rtmps://runner/live/session?token=abc", "runner:443", "live", "session?token=abc"},
		{"rtmp://[::1]/live/session", "[::1]:1935", "live", "session"},
	} {
		host, app, key, err := parseRTMPURL(tt.raw)
		if err != nil || host != tt.host || app != tt.app || key != tt.key {
			t.Fatalf("parse got %s %s %s %v", host, app, key, err)
		}
	}
	for _, raw := range []string{"https://runner/live/SECRET", "rtmp://runner/live", "rtmp://user:SECRET@runner/live/session", "rtmp://runner/live/SECRET#fragment", "rtmp://runner:invalid/live/SECRET"} {
		_, _, _, err := parseRTMPURL(raw)
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("invalid URL leaked or accepted: %v", err)
		}
	}
}

// Exercise the RTMP handshake and publish command, not only URL parsing.
func TestRelayForwardsRuntimeTokenToPublish(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	published := make(chan string, 1)
	srv := rtmplib.NewServer(&rtmplib.ServerConfig{OnConnect: func(c net.Conn) (io.ReadWriteCloser, *rtmplib.ConnConfig) {
		return c, &rtmplib.ConnConfig{Handler: &capturePublish{published: published}}
	}})
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close(); <-done }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	relay, err := DialAndPublish(ctx, "rtmp://"+listener.Addr().String()+"/live/session?token=signed%2Bsecret", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	select {
	case key := <-published:
		if key != "session?token=signed%2Bsecret" {
			t.Fatalf("runtime token lost: %q", key)
		}
	case <-ctx.Done():
		t.Fatal("upstream publish never arrived")
	}
}

type capturePublish struct {
	rtmplib.DefaultHandler
	published chan string
}

func (h *capturePublish) OnPublish(_ *rtmplib.StreamContext, _ uint32, cmd *rtmpmsg.NetStreamPublish) error {
	h.published <- cmd.PublishingName
	return nil
}

func TestIngestListenerStopsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := New(Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, "127.0.0.1", 0)
	srv.addr = "127.0.0.1:0"
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for !srv.Listening() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !srv.Listening() {
		t.Fatal("listener not ready")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("RTMP listener did not stop")
	}
	if srv.Listening() {
		t.Fatal("closed listener advertised ready")
	}
}
