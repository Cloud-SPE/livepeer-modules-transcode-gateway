package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	Port            int      `env:"PORT" envDefault:"4000"`
	Host            string   `env:"HOST" envDefault:"0.0.0.0"`
	BaseURL         string   `env:"BASE_URL" envDefault:"http://localhost:4000"`
	// GatewayPublicURL is the externally-reachable URL of THIS gateway,
	// passed to the runner as webhook_url so it can POST status updates
	// back. When empty, no webhook_url is sent → gateway stays oblivious
	// to runner outcomes (the previous behavior).
	GatewayPublicURL string  `env:"GATEWAY_PUBLIC_URL"`
	PublicSiteURL   string   `env:"PUBLIC_SITE_URL" envDefault:"http://localhost:3000"`
	PublicPortalURL string   `env:"PUBLIC_PORTAL_URL" envDefault:"http://localhost:3001"`
	AllowedOrigins  []string `env:"ALLOWED_ORIGINS" envSeparator:"," envDefault:"*"`
	LogLevel        string   `env:"LOG_LEVEL" envDefault:"info"`

	DatabaseURL   string `env:"DATABASE_URL,required"`
	MigrationsDir string `env:"MIGRATIONS_DIR" envDefault:"migrations"`

	AdminToken        string        `env:"ADMIN_TOKEN"`
	APIKeyHashPepper  string        `env:"API_KEY_HASH_PEPPER"`
	IPHashPepper      string        `env:"IP_HASH_PEPPER"`
	MetricsToken      string        `env:"METRICS_TOKEN"`
	SessionTTLHours   int           `env:"SESSION_TTL_HOURS" envDefault:"24"`
	SessionTTL        time.Duration // derived

	ResendAPIKey string `env:"RESEND_API_KEY"`
	FromEmail    string `env:"FROM_EMAIL" envDefault:"Livepeer Video Gateway <noreply@example.com>"`

	S3Region          string `env:"S3_REGION" envDefault:"us-east-1"`
	S3Bucket          string `env:"S3_BUCKET" envDefault:"lvp-video-ingest"`
	S3Endpoint        string `env:"S3_ENDPOINT" envDefault:"http://rustfs:9000"`
	S3PublicEndpoint  string `env:"S3_PUBLIC_ENDPOINT" envDefault:"http://localhost:9000"`
	S3AccessKeyID     string `env:"S3_ACCESS_KEY_ID"`
	S3SecretAccessKey string `env:"S3_SECRET_ACCESS_KEY"`
	S3PresignTTLSecs  int    `env:"S3_PRESIGN_TTL_SECONDS" envDefault:"3600"`

	PayerSocket    string        `env:"LIVEPEER_PAYER_DAEMON_SOCKET" envDefault:"/var/run/livepeer/payer-daemon.sock"`
	ResolverSocket string        `env:"LIVEPEER_RESOLVER_SOCKET" envDefault:"/var/run/livepeer/service-registry.sock"`
	RefreshMS      int           `env:"REGISTRY_REFRESH_INTERVAL_MS" envDefault:"60000"`
	RefreshInterval time.Duration // derived

	ABRCapability  string `env:"ABR_CAPABILITY" envDefault:"video:transcode.abr"`
	LiveCapability string `env:"LIVE_CAPABILITY" envDefault:"video:live.rtmp"`

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
	return cfg, nil
}

// Warnings returns human-readable startup warnings for missing
// recommended values. They don't block startup but should be logged.
func (c Config) Warnings() []string {
	var w []string
	if c.AdminToken == "" {
		w = append(w, "ADMIN_TOKEN unset — /admin/* will return 503 admin_disabled")
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
		w = append(w, "S3 credentials unset — /v1/abr/upload-url will return 503")
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
