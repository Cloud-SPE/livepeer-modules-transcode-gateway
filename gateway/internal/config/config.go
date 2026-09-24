package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	Port    int    `env:"PORT" envDefault:"4000"`
	Host    string `env:"HOST" envDefault:"0.0.0.0"`
	BaseURL string `env:"BASE_URL" envDefault:"http://localhost:4000"`
	// GatewayPublicURL is the external HTTP origin; also used to derive RTMP host.
	GatewayPublicURL string   `env:"GATEWAY_PUBLIC_URL"`
	PublicSiteURL    string   `env:"PUBLIC_SITE_URL" envDefault:"http://localhost:3000"`
	PublicPortalURL  string   `env:"PUBLIC_PORTAL_URL" envDefault:"http://localhost:3001"`
	AllowedOrigins   []string `env:"ALLOWED_ORIGINS" envSeparator:"," envDefault:"*"`
	LogLevel         string   `env:"LOG_LEVEL" envDefault:"info"`

	DatabaseURL   string `env:"DATABASE_URL,required"`
	MigrationsDir string `env:"MIGRATIONS_DIR" envDefault:"migrations"`

	AdminToken       string        `env:"ADMIN_TOKEN"`
	APIKeyHashPepper string        `env:"API_KEY_HASH_PEPPER"`
	IPHashPepper     string        `env:"IP_HASH_PEPPER"`
	MetricsToken     string        `env:"METRICS_TOKEN"`
	SessionTTLHours  int           `env:"SESSION_TTL_HOURS" envDefault:"24"`
	SessionTTL       time.Duration // derived

	ResendAPIKey string `env:"RESEND_API_KEY"`
	FromEmail    string `env:"FROM_EMAIL" envDefault:"Livepeer Video Gateway <noreply@example.com>"`

	S3Region          string `env:"S3_REGION" envDefault:"us-east-1"`
	S3Bucket          string `env:"S3_BUCKET" envDefault:"lvp-video-ingest"`
	S3Endpoint        string `env:"S3_ENDPOINT" envDefault:"http://minio:9000"`
	S3PublicEndpoint  string `env:"S3_PUBLIC_ENDPOINT" envDefault:"http://localhost:9000"`
	S3AccessKeyID     string `env:"S3_ACCESS_KEY_ID"`
	S3SecretAccessKey string `env:"S3_SECRET_ACCESS_KEY"`
	S3PresignTTLSecs  int    `env:"S3_PRESIGN_TTL_SECONDS" envDefault:"3600"`

	RefreshMS       int           `env:"REGISTRY_REFRESH_INTERVAL_MS" envDefault:"60000"`
	RefreshInterval time.Duration // derived

	// LOC owns scoped authorizations, routing and verified settlement.
	LOCBaseURL                string `env:"LOC_BASE_URL" envDefault:"https://loc.cloudspe.com"`
	LOCAPIKey                 string `env:"LOC_API_KEY"`
	CallerPrivateKey          string `env:"LOC_CALLER_PRIVATE_KEY"`
	OperationSecretsKey       string `env:"OPERATION_SECRETS_KEY"`
	ABROffering               string `env:"ABR_OFFERING" envDefault:"abr-default"`
	ABRMaxTotalUnits          int64  `env:"ABR_MAX_TOTAL_UNITS" envDefault:"1000000"`
	ABRJobTimeoutSecs         int    `env:"ABR_JOB_TIMEOUT_SECS" envDefault:"3600"`
	LiveOutputProfile         string `env:"LIVE_OUTPUT_PROFILE" envDefault:"live-standard"`
	LiveMeteringRendition     string `env:"LIVE_METERING_RENDITION" envDefault:"720p"`
	LiveExternalRTMPURL       string `env:"LIVE_EXTERNAL_RTMP_URL"`
	ABRCapability             string `env:"ABR_CAPABILITY" envDefault:"video:transcode.abr"`
	LiveCapability            string `env:"LIVE_CAPABILITY" envDefault:"video:transcode.live"`
	LiveGatewayIngestOffering string `env:"LIVE_GATEWAY_INGEST_OFFERING" envDefault:"gateway-ingest"`

	// Live control polling and whole output-second authorization increments.
	LiveReconcileIntervalSecs    int `env:"LIVE_RECONCILE_INTERVAL_SECS" envDefault:"30"`
	LiveTopupRunwayThresholdSecs int `env:"LIVE_TOPUP_RUNWAY_THRESHOLD_SECS" envDefault:"60"`
	LiveTopupFundSecs            int `env:"LIVE_TOPUP_FUND_SECS" envDefault:"60"`
	// Lifetime ceiling in whole output_seconds (6000 = 100 minutes).
	LiveMaxTotalUnits int64 `env:"LIVE_MAX_TOTAL_UNITS" envDefault:"6000"`

	LiveRTMPPort int `env:"LIVE_RTMP_PORT" envDefault:"0"`

	V1RateLimitPerMinute int `env:"V1_RATE_LIMIT_PER_MINUTE" envDefault:"60"`
	V1RateLimitBurst     int `env:"V1_RATE_LIMIT_BURST" envDefault:"30"`
}

func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return cfg, fmt.Errorf("config: parse env: %w", err)
	}
	cfg.SessionTTL = time.Duration(cfg.SessionTTLHours) * time.Hour
	cfg.RefreshInterval = time.Duration(cfg.RefreshMS) * time.Millisecond
	if cfg.RefreshMS <= 0 || cfg.ABRMaxTotalUnits <= 0 || cfg.ABRJobTimeoutSecs <= 0 || cfg.LiveMaxTotalUnits <= 0 || cfg.LiveTopupFundSecs <= 0 || cfg.LiveReconcileIntervalSecs <= 0 || cfg.LiveTopupRunwayThresholdSecs < 0 || cfg.LiveRTMPPort < 0 || cfg.LiveRTMPPort > 65535 {
		return cfg, fmt.Errorf("config: catalog interval and paid operation limits must be positive")
	}
	if cfg.LOCAPIKey != "" && (cfg.CallerPrivateKey == "" || cfg.OperationSecretsKey == "") {
		return cfg, fmt.Errorf("config: LOC_CALLER_PRIVATE_KEY and OPERATION_SECRETS_KEY are required for v2 paid operations")
	}
	if cfg.LiveExternalRTMPURL != "" {
		u, err := url.Parse(cfg.LiveExternalRTMPURL)
		if err != nil || (u.Scheme != "rtmp" && u.Scheme != "rtmps") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/live" {
			return cfg, fmt.Errorf("config: LIVE_EXTERNAL_RTMP_URL must be an rtmp(s) server URL ending in /live without a stream key")
		}
	}
	return cfg, nil
}

// Warnings returns human-readable startup warnings for missing
// recommended values. They don't block startup but should be logged.
func (c Config) Warnings() []string {
	var w []string
	if c.AdminToken == "" {
		w = append(w, "ADMIN_TOKEN unset — /api/admin/* will return 503 admin_disabled")
	}
	if c.APIKeyHashPepper == "" {
		w = append(w, "API_KEY_HASH_PEPPER unset — API keys hash without pepper (dev only)")
	}
	if c.IPHashPepper == "" {
		w = append(w, "IP_HASH_PEPPER unset — IP / token / session / stream-key hashes are unpeppered (dev only)")
	}
	if c.ResendAPIKey == "" {
		w = append(w, "RESEND_API_KEY unset — verification + key delivery emails will log to stdout instead of send")
	}
	if c.S3AccessKeyID == "" || c.S3SecretAccessKey == "" {
		w = append(w, "S3 credentials unset — /api/v1/abr/upload-url will return 503")
	}
	if c.LOCAPIKey == "" {
		w = append(w, "LOC_API_KEY unset — /api/v1/abr and /api/v1/live will return 503 loc_unavailable")
	}
	return w
}

// AllowAllOrigins reports whether ALLOWED_ORIGINS is the wildcard.
func (c Config) AllowAllOrigins() bool {
	for _, o := range c.AllowedOrigins {
		if strings.TrimSpace(o) == "*" {
			return true
		}
	}
	return false
}
