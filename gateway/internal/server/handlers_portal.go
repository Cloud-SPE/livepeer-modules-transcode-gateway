package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/crypto"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

func RegisterPortal(api huma.API, deps Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "portal-login",
		Method:      http.MethodPost,
		Path:        "/api/portal/login",
		Summary:     "Trade an API key for a session cookie",
		Tags:        []string{"portal"},
	}, func(ctx context.Context, in *struct {
		Body struct {
			APIKey string `json:"apiKey" required:"true" minLength:"10"`
		}
	}) (*PortalLoginOut, error) {
		hash := crypto.HashWithPepper(in.Body.APIKey, deps.Cfg.APIKeyHashPepper)
		ak, err := deps.APIKeys.GetByHash(ctx, hash)
		if err != nil || ak == nil {
			return nil, huma.Error401Unauthorized("invalid_api_key")
		}
		wl, err := deps.Waitlist.GetByID(ctx, ak.WaitlistID)
		if err != nil || wl == nil || wl.Status != repo.WaitlistApproved {
			return nil, huma.Error403Forbidden("key_not_approved")
		}
		token, err := crypto.RandomToken(32)
		if err != nil {
			return nil, huma.Error500InternalServerError("session mint failed")
		}
		sessHash := crypto.HashWithPepper(token, deps.Cfg.IPHashPepper)
		if _, err := deps.Sessions.Insert(ctx, ak.ID, sessHash, time.Now().Add(deps.Cfg.SessionTTL)); err != nil {
			return nil, huma.Error500InternalServerError("session insert failed", err)
		}
		out := &PortalLoginOut{SetCookie: cookieHeaderValue(deps, token)}
		out.Body.OK = true
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "portal-logout",
		Method:      http.MethodPost,
		Path:        "/api/portal/logout",
		Summary:     "Revoke the current session",
		Tags:        []string{"portal"},
	}, func(ctx context.Context, _ *struct{}) (*PortalLoginOut, error) {
		ak := APIKeyFromCtx(ctx)
		_ = ak
		// Best-effort revoke — middleware already gates by cookie validity.
		if sess := sessionFromCtx(ctx); sess != nil {
			_ = deps.Sessions.Revoke(ctx, sess.SessionHash)
		}
		out := &PortalLoginOut{SetCookie: clearedCookieHeaderValue(deps)}
		out.Body.OK = true
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "portal-account",
		Method:      http.MethodGet,
		Path:        "/api/portal/account",
		Summary:     "Current portal account",
		Tags:        []string{"portal"},
	}, func(ctx context.Context, _ *struct{}) (*PortalAccountOut, error) {
		wl := WaitlistFromCtx(ctx)
		ak := APIKeyFromCtx(ctx)
		if wl == nil || ak == nil {
			return nil, huma.Error401Unauthorized("not_authenticated")
		}
		out := &PortalAccountOut{}
		out.Body.Email = wl.Email
		out.Body.Name = wl.Name
		out.Body.WaitlistID = wl.ID
		out.Body.APIKeyPrefix = ak.KeyPrefix
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "portal-list-keys",
		Method:      http.MethodGet,
		Path:        "/api/portal/api-keys",
		Summary:     "List my API keys",
		Tags:        []string{"portal"},
	}, func(ctx context.Context, _ *struct{}) (*PortalKeysOut, error) {
		wl := WaitlistFromCtx(ctx)
		if wl == nil {
			return nil, huma.Error401Unauthorized("not_authenticated")
		}
		keys, err := deps.APIKeys.ListByWaitlist(ctx, wl.ID)
		if err != nil {
			return nil, huma.Error500InternalServerError("list keys failed", err)
		}
		out := &PortalKeysOut{}
		for _, k := range keys {
			out.Body.Keys = append(out.Body.Keys, APIKeyView{
				ID:         k.ID,
				Label:      derefString(k.Label),
				KeyPrefix:  k.KeyPrefix,
				CreatedAt:  k.CreatedAt,
				LastUsedAt: k.LastUsedAt,
				RevokedAt:  k.RevokedAt,
			})
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "portal-mint-key",
		Method:      http.MethodPost,
		Path:        "/api/portal/api-keys",
		Summary:     "Mint a new API key",
		Tags:        []string{"portal"},
	}, func(ctx context.Context, in *struct {
		Body struct {
			Label string `json:"label" maxLength:"100"`
		}
	}) (*PortalMintKeyOut, error) {
		wl := WaitlistFromCtx(ctx)
		if wl == nil {
			return nil, huma.Error401Unauthorized("not_authenticated")
		}
		key, prefix, hash, err := crypto.NewAPIKey(deps.Cfg.APIKeyHashPepper)
		if err != nil {
			return nil, huma.Error500InternalServerError("key mint failed", err)
		}
		tx, err := deps.Pool.Begin(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("tx begin", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		ak, err := deps.APIKeys.Insert(ctx, tx, wl.ID, in.Body.Label, prefix, hash)
		if err != nil {
			return nil, huma.Error500InternalServerError("api key insert", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, huma.Error500InternalServerError("tx commit", err)
		}
		out := &PortalMintKeyOut{}
		out.Body.Plaintext = key
		out.Body.Key = APIKeyView{
			ID:         ak.ID,
			Label:      in.Body.Label,
			KeyPrefix:  ak.KeyPrefix,
			CreatedAt:  ak.CreatedAt,
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "portal-revoke-key",
		Method:      http.MethodDelete,
		Path:        "/api/portal/api-keys/{id}",
		Summary:     "Revoke an API key",
		Tags:        []string{"portal"},
	}, func(ctx context.Context, in *struct {
		ID uuid.UUID `path:"id"`
	}) (*GenericOK, error) {
		wl := WaitlistFromCtx(ctx)
		if wl == nil {
			return nil, huma.Error401Unauthorized("not_authenticated")
		}
		if err := deps.APIKeys.Revoke(ctx, in.ID, wl.ID); err != nil {
			return nil, huma.Error500InternalServerError("revoke failed", err)
		}
		out := &GenericOK{}
		out.Body.OK = true
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "portal-usage",
		Method:      http.MethodGet,
		Path:        "/api/portal/usage",
		Summary:     "Recent usage for my API keys",
		Tags:        []string{"portal"},
	}, func(ctx context.Context, in *struct {
		Limit int `query:"limit" default:"100" minimum:"1" maximum:"500"`
	}) (*PortalUsageOut, error) {
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("not_authenticated")
		}
		recs, err := deps.Usage.ListByAPIKey(ctx, ak.ID, time.Now().AddDate(0, 0, -30), in.Limit)
		if err != nil {
			return nil, huma.Error500InternalServerError("usage list", err)
		}
		out := &PortalUsageOut{}
		for _, r := range recs {
			out.Body.Items = append(out.Body.Items, UsageItem{
				ID:         r.ID,
				WorkID:     r.WorkID,
				Capability: r.Capability,
				Offering:   r.Offering,
				State:      string(r.State),
				LatencyMs:  derefInt(r.LatencyMs),
				StatusCode: derefInt(r.StatusCode),
				CreatedAt:  r.CreatedAt,
			})
		}
		return out, nil
	})

	// /portal/live-streams — user-facing RTMP session history. Survives
	// the playground's localStorage being cleared.
	huma.Register(api, huma.Operation{
		OperationID: "portal-live-streams",
		Method:      http.MethodGet,
		Path:        "/api/portal/live-streams",
		Summary:     "My RTMP→HLS sessions",
		Tags:        []string{"portal"},
	}, func(ctx context.Context, in *struct {
		Limit int `query:"limit" default:"100" minimum:"1" maximum:"500"`
	}) (*PortalLiveStreamsOut, error) {
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("not_authenticated")
		}
		recs, err := deps.Live.ListByAPIKey(ctx, ak.ID, in.Limit)
		if err != nil {
			return nil, huma.Error500InternalServerError("live list", err)
		}
		out := &PortalLiveStreamsOut{}
		for _, r := range recs {
			out.Body.Items = append(out.Body.Items, PortalLiveStreamView{
				ID:          r.ID,
				Name:        derefString(r.Name),
				Status:      string(r.Status),
				PlaybackURL: derefString(r.PlaybackURL),
				ErrorText:   derefString(r.ErrorText),
				CreatedAt:   r.CreatedAt,
				StartedAt:   r.StartedAt,
				EndedAt:     r.EndedAt,
			})
		}
		return out, nil
	})

	// /portal/abr-jobs — user-facing ABR ladder history. Survives
	// localStorage being cleared.
	huma.Register(api, huma.Operation{
		OperationID: "portal-abr-jobs",
		Method:      http.MethodGet,
		Path:        "/api/portal/abr-jobs",
		Summary:     "My ABR ladder transcode jobs",
		Tags:        []string{"portal"},
	}, func(ctx context.Context, in *struct {
		Limit int `query:"limit" default:"100" minimum:"1" maximum:"500"`
	}) (*PortalABRJobsOut, error) {
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("not_authenticated")
		}
		recs, err := deps.Usage.ListByAPIKey(ctx, ak.ID, time.Now().AddDate(0, 0, -90), in.Limit)
		if err != nil {
			return nil, huma.Error500InternalServerError("abr list", err)
		}
		out := &PortalABRJobsOut{}
		for _, r := range recs {
			if r.Capability != deps.Cfg.ABRCapability {
				continue
			}
			out.Body.Items = append(out.Body.Items, PortalABRJobView{
				WorkID:      r.WorkID,
				RunnerJobID: derefString(r.RunnerJobID),
				State:       string(r.State),
				BrokerURL:   derefString(r.BrokerURL),
				LatencyMs:   derefInt(r.LatencyMs),
				ErrorText:   derefString(r.ErrorText),
				CreatedAt:   r.CreatedAt,
				ResolvedAt:  r.ResolvedAt,
			})
		}
		return out, nil
	})
}

type PortalLiveStreamView struct {
	ID          uuid.UUID  `json:"id"`
	Name        string     `json:"name,omitempty"`
	Status      string     `json:"status"`
	PlaybackURL string     `json:"playback_url,omitempty"`
	ErrorText   string     `json:"error_text,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
}

type PortalLiveStreamsOut struct {
	Body struct {
		Items []PortalLiveStreamView `json:"items"`
	}
}

type PortalABRJobView struct {
	WorkID      uuid.UUID  `json:"work_id"`
	RunnerJobID string     `json:"runner_job_id,omitempty"`
	State       string     `json:"state"`
	BrokerURL   string     `json:"broker_url,omitempty"`
	LatencyMs   int        `json:"latency_ms,omitempty"`
	ErrorText   string     `json:"error_text,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
}

type PortalABRJobsOut struct {
	Body struct {
		Items []PortalABRJobView `json:"items"`
	}
}

type PortalLoginOut struct {
	SetCookie string `header:"Set-Cookie"`
	Body      struct {
		OK bool `json:"ok"`
	}
}

type PortalAccountOut struct {
	Body struct {
		Email        string    `json:"email"`
		Name         string    `json:"name"`
		WaitlistID   uuid.UUID `json:"waitlist_id"`
		APIKeyPrefix string    `json:"api_key_prefix"`
	}
}

type APIKeyView struct {
	ID         uuid.UUID  `json:"id"`
	Label      string     `json:"label,omitempty"`
	KeyPrefix  string     `json:"key_prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type PortalKeysOut struct {
	Body struct {
		Keys []APIKeyView `json:"keys"`
	}
}

type PortalMintKeyOut struct {
	Body struct {
		Plaintext string     `json:"plaintext_key" doc:"Shown exactly once; store immediately."`
		Key       APIKeyView `json:"key"`
	}
}

type UsageItem struct {
	ID         uuid.UUID `json:"id"`
	WorkID     uuid.UUID `json:"work_id"`
	Capability string    `json:"capability"`
	Offering   string    `json:"offering"`
	State      string    `json:"state"`
	LatencyMs  int       `json:"latency_ms,omitempty"`
	StatusCode int       `json:"status_code,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type PortalUsageOut struct {
	Body struct {
		Items []UsageItem `json:"items"`
	}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func cookieHeaderValue(deps Deps, token string) string {
	c := &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(deps.Cfg.BaseURL, "https://"),
		MaxAge:   int(deps.Cfg.SessionTTL.Seconds()),
	}
	return c.String()
}

func clearedCookieHeaderValue(deps Deps) string {
	c := &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(deps.Cfg.BaseURL, "https://"),
		MaxAge:   -1,
	}
	return c.String()
}

func sessionFromCtx(ctx context.Context) *repo.UserSession {
	v, _ := ctx.Value(ctxKeySession).(*repo.UserSession)
	return v
}
