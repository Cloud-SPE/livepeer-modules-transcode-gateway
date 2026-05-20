package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/config"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/db"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/email"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/metrics"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/livepeer"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/service"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/registry"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/s3"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	for _, w := range cfg.Warnings() {
		log.Warn(w)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Migrations
	if err := db.Migrate(cfg.DatabaseURL, cfg.MigrationsDir, log); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// Postgres pool
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Repos
	waitlist := repo.NewWaitlistRepo(pool)
	apiKeys := repo.NewAPIKeyRepo(pool)
	sessions := repo.NewSessionRepo(pool)
	usage := repo.NewReservationRepo(pool)
	live := repo.NewLiveRepo(pool)
	caps := repo.NewCapabilityRepo(pool)

	// Email
	mailer := email.New(cfg.ResendAPIKey, cfg.FromEmail, log)

	// S3 / RustFS
	s3c, err := s3.New(ctx, cfg.S3Region, cfg.S3Endpoint, cfg.S3PublicEndpoint, cfg.S3Bucket,
		cfg.S3AccessKeyID, cfg.S3SecretAccessKey, cfg.S3PresignTTLSecs)
	if err != nil {
		log.Warn("s3: configuration error; /v1/abr/upload-url disabled", "err", err)
	}
	if s3c != nil {
		if err := s3c.HeadBucket(ctx); err != nil {
			log.Warn("s3: bucket head failed at boot (may still recover at runtime)", "err", err)
		}
	}

	// gRPC clients (best-effort dial)
	payer, err := livepeer.DialPayer(ctx, cfg.PayerSocket)
	if err != nil {
		log.Warn("payer dial failed (gateway will return 503 on /v1/abr|/v1/live)", "err", err)
	}
	resolver, err := service.DialResolver(ctx, cfg.ResolverSocket)
	if err != nil {
		log.Warn("resolver dial failed (gateway will return 503 on /v1/abr|/v1/live)", "err", err)
	}

	// Metrics
	met := metrics.New()

	// Capability catalog refresh (best-effort)
	refresher := registry.NewRefresher(resolver, caps, cfg.RefreshInterval,
		[]string{cfg.ABRCapability, cfg.LiveCapability}, log)
	go refresher.Start(ctx)

	deps := server.Deps{
		Cfg:      cfg,
		Log:      log,
		Pool:     pool,
		Waitlist: waitlist,
		APIKeys:  apiKeys,
		Sessions: sessions,
		Usage:    usage,
		Live:     live,
		Caps:     caps,
		Email:    mailer,
		S3:       s3c,
		Payer:    payer,
		Resolver: resolver,
		Health:   service.NewHealth(2, 30*time.Second),
		HTTP:     livepeer.NewHTTPClient(30 * time.Second),
		CapMap:   livepeer.NewDefault(cfg.ABRCapability, cfg.LiveCapability),
		Metrics:  met,
	}

	srv := &http.Server{
		Addr:         net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Handler:      server.New(deps),
		ReadTimeout:  server.DefaultReadTimeout,
		WriteTimeout: server.DefaultWriteTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-errCh:
		stop()
		return fmt.Errorf("server: %w", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("shutdown returned an error", "err", err)
	}
	if payer != nil {
		_ = payer.Close()
	}
	if resolver != nil {
		_ = resolver.Close()
	}
	log.Info("server stopped")
	return nil
}

func newLogger(level string) *slog.Logger {
	lv := slog.LevelInfo
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lv})
	return slog.New(h)
}
