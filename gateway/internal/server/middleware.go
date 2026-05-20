package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/crypto"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/livepeer"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"

	"github.com/google/uuid"
)

type ctxKey int

const (
	ctxKeyAPIKey ctxKey = iota
	ctxKeyWaitlist
	ctxKeySession
	ctxKeyRequestID
)

const SessionCookieName = "lvp_video_session"

// RequestID assigns or echoes Livepeer-Request-Id on every request.
func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rid := r.Header.Get(livepeer.HeaderRequestID)
			if rid == "" {
				rid = newRequestID()
			}
			w.Header().Set(livepeer.HeaderRequestID, rid)
			ctx := context.WithValue(r.Context(), ctxKeyRequestID, rid)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func RequestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyRequestID).(string)
	return v
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// AccessLog logs each request with structured fields. Cheap; no buffering.
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: 200}
			next.ServeHTTP(sw, r)
			log.Info("http",
				"req_id", RequestIDFrom(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"dur_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(s int) {
	w.status = s
	w.ResponseWriter.WriteHeader(s)
}

// CORS returns a tiny same-origin-friendly CORS middleware. With "*" in
// allowed origins, allowed origins becomes the request origin (so credentials
// can flow).
func CORS(allowed []string) func(http.Handler) http.Handler {
	allowAll := false
	set := map[string]struct{}{}
	for _, o := range allowed {
		o = strings.TrimSpace(o)
		if o == "*" {
			allowAll = true
		}
		set[o] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				_, ok := set[origin]
				if allowAll || ok {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Credentials", "true")
					w.Header().Set("Vary", "Origin")
				}
			}
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Admin-Token, Livepeer-Request-Id")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ExtractBearer returns the API key from `Authorization: Bearer …` (or "" if absent).
func ExtractBearer(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if len(v) > 7 && strings.EqualFold(v[:7], "Bearer ") {
		return v[7:]
	}
	return ""
}

// RequireAPIKey is the /v1/* gate. Verifies the bearer key, attaches
// the api_keys row to ctx, and enforces approval status via a waitlist
// lookup.
func RequireAPIKey(deps Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := ExtractBearer(r)
			if key == "" {
				writeAuthError(w, "missing bearer token")
				return
			}
			hash := crypto.HashWithPepper(key, deps.Cfg.APIKeyHashPepper)
			apiKey, err := deps.APIKeys.GetByHash(r.Context(), hash)
			if err != nil || apiKey == nil {
				writeAuthError(w, "invalid api key")
				return
			}
			wl, err := deps.Waitlist.GetByID(r.Context(), apiKey.WaitlistID)
			if err != nil || wl == nil || wl.Status != repo.WaitlistApproved {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, `{"error":"key_not_approved"}`, http.StatusForbidden)
				return
			}
			deps.APIKeys.TouchLastUsed(r.Context(), apiKey.ID)
			ctx := context.WithValue(r.Context(), ctxKeyAPIKey, apiKey)
			ctx = context.WithValue(ctx, ctxKeyWaitlist, wl)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// APIKeyFromCtx returns the authenticated api_key row (or nil if absent).
func APIKeyFromCtx(ctx context.Context) *repo.APIKey {
	v, _ := ctx.Value(ctxKeyAPIKey).(*repo.APIKey)
	return v
}

func WaitlistFromCtx(ctx context.Context) *repo.Waitlist {
	v, _ := ctx.Value(ctxKeyWaitlist).(*repo.Waitlist)
	return v
}

// RequirePortalSession authenticates a portal session cookie and
// attaches the api_keys row + waitlist row to ctx.
func RequirePortalSession(deps Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(SessionCookieName)
			if err != nil || cookie.Value == "" {
				http.Error(w, `{"error":"not_authenticated"}`, http.StatusUnauthorized)
				return
			}
			hash := crypto.HashWithPepper(cookie.Value, deps.Cfg.IPHashPepper)
			sess, err := deps.Sessions.GetByHash(r.Context(), hash)
			if err != nil || sess == nil {
				clearSessionCookie(w, deps)
				http.Error(w, `{"error":"not_authenticated"}`, http.StatusUnauthorized)
				return
			}
			ak, err := getAPIKeyByID(r.Context(), deps, sess.APIKeyID)
			if err != nil || ak == nil {
				clearSessionCookie(w, deps)
				http.Error(w, `{"error":"not_authenticated"}`, http.StatusUnauthorized)
				return
			}
			wl, err := deps.Waitlist.GetByID(r.Context(), ak.WaitlistID)
			if err != nil || wl == nil {
				clearSessionCookie(w, deps)
				http.Error(w, `{"error":"not_authenticated"}`, http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyAPIKey, ak)
			ctx = context.WithValue(ctx, ctxKeyWaitlist, wl)
			ctx = context.WithValue(ctx, ctxKeySession, sess)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// SetSessionCookie issues an HttpOnly session cookie.
func SetSessionCookie(w http.ResponseWriter, deps Deps, value string) {
	c := &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(deps.Cfg.BaseURL, "https://"),
		MaxAge:   int(deps.Cfg.SessionTTL.Seconds()),
	}
	http.SetCookie(w, c)
}

func clearSessionCookie(w http.ResponseWriter, deps Deps) {
	c := &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(deps.Cfg.BaseURL, "https://"),
		MaxAge:   -1,
	}
	http.SetCookie(w, c)
}

// RequireAdmin gates /admin/* on the X-Admin-Token header.
func RequireAdmin(deps Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if deps.Cfg.AdminToken == "" {
				http.Error(w, `{"error":"admin_disabled"}`, http.StatusServiceUnavailable)
				return
			}
			tok := r.Header.Get("X-Admin-Token")
			if !crypto.ConstantTimeEqual(tok, deps.Cfg.AdminToken) {
				http.Error(w, `{"error":"invalid_admin_token"}`, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimit is a simple per-API-key token bucket for /v1/*.
type RateLimit struct {
	perMinute int
	burst     int
	mu        sync.Mutex
	buckets   map[uuid.UUID]*bucket
}

type bucket struct {
	tokens   float64
	updated  time.Time
}

func NewRateLimit(perMinute, burst int) *RateLimit {
	return &RateLimit{perMinute: perMinute, burst: burst, buckets: map[uuid.UUID]*bucket{}}
}

func (rl *RateLimit) Allow(id uuid.UUID, now time.Time) (bool, int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b := rl.buckets[id]
	if b == nil {
		b = &bucket{tokens: float64(rl.burst), updated: now}
		rl.buckets[id] = b
	}
	elapsed := now.Sub(b.updated).Seconds()
	b.tokens += elapsed * (float64(rl.perMinute) / 60.0)
	if b.tokens > float64(rl.burst) {
		b.tokens = float64(rl.burst)
	}
	b.updated = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	retry := int((1 - b.tokens) / (float64(rl.perMinute) / 60.0))
	if retry < 1 {
		retry = 1
	}
	return false, retry
}

// EnforceRateLimit wraps a handler with the per-key bucket.
func EnforceRateLimit(rl *RateLimit) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := APIKeyFromCtx(r.Context())
			if key == nil {
				next.ServeHTTP(w, r)
				return
			}
			ok, retry := rl.Allow(key.ID, time.Now())
			if !ok {
				w.Header().Set("Retry-After", strconv.Itoa(retry))
				http.Error(w, `{"error":"rate_limit_exceeded"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeAuthError(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, `{"error":"invalid_api_key","detail":"`+msg+`"}`, http.StatusUnauthorized)
}

// getAPIKeyByID is a thin helper that hides the fact APIKeyRepo doesn't
// expose a direct-by-id lookup (we don't store the ID indexed for portal
// session bootstrap). We add this via a small inline query.
func getAPIKeyByID(ctx context.Context, deps Deps, id uuid.UUID) (*repo.APIKey, error) {
	row := deps.Pool.QueryRow(ctx,
		`SELECT id, waitlist_id, label, key_prefix, key_hash, created_at, last_used_at, revoked_at
		 FROM api_keys WHERE id=$1 AND revoked_at IS NULL`, id)
	var k repo.APIKey
	if err := row.Scan(&k.ID, &k.WaitlistID, &k.Label, &k.KeyPrefix, &k.KeyHash,
		&k.CreatedAt, &k.LastUsedAt, &k.RevokedAt); err != nil {
		return nil, err
	}
	return &k, nil
}
