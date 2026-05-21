package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/crypto"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

type WaitlistSignupInput struct {
	Body struct {
		Name  string `json:"name"  minLength:"1" maxLength:"200" doc:"User-facing name"`
		Email string `json:"email" format:"email"               doc:"Contact email; verification link sent here"`
	}
}

type GenericOK struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

func RegisterPublic(api huma.API, deps Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "waitlist-signup",
		Method:      http.MethodPost,
		Path:        "/api/public/waitlist",
		Summary:     "Sign up to the waitlist",
		Tags:        []string{"public"},
	}, func(ctx context.Context, in *WaitlistSignupInput) (*GenericOK, error) {
		token, err := crypto.RandomToken(32)
		if err != nil {
			return nil, huma.Error500InternalServerError("token mint failed")
		}
		hash := crypto.HashWithPepper(token, deps.Cfg.IPHashPepper)
		ip := ipHashFromCtx(ctx, deps.Cfg.IPHashPepper)
		expires := time.Now().Add(24 * time.Hour)
		w, created, err := deps.Waitlist.Insert(ctx, in.Body.Name, in.Body.Email, ip, hash, expires)
		if err != nil {
			return nil, huma.Error500InternalServerError("waitlist insert failed", err)
		}
		if created {
			deps.Metrics.WaitlistSignupsTotal.Inc()
			deliverVerificationEmail(ctx, deps, in.Body.Email, in.Body.Name, token)
		}
		// We intentionally return the same OK shape whether new or duplicate.
		_ = w
		out := &GenericOK{}
		out.Body.OK = true
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "waitlist-verify",
		Method:      http.MethodGet,
		Path:        "/api/public/verify",
		Summary:     "Verify a signup email",
		Tags:        []string{"public"},
	}, func(ctx context.Context, in *struct {
		Token string `query:"token" required:"true" doc:"Token from the verification email"`
	}) (*VerifyOut, error) {
		hash := crypto.HashWithPepper(in.Token, deps.Cfg.IPHashPepper)
		w, err := deps.Waitlist.ConsumeVerificationToken(ctx, hash)
		if err != nil {
			return nil, huma.Error500InternalServerError("verify failed", err)
		}
		if w == nil {
			return nil, huma.Error400BadRequest("verification_token_invalid_or_expired")
		}
		out := &VerifyOut{}
		out.Body.OK = true
		out.Body.Message = "Email verified — admin will review your signup."
		return out, nil
	})
}

type VerifyOut struct {
	Body struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
}

// deliverVerificationEmail sends or logs the verification email.
func deliverVerificationEmail(ctx context.Context, deps Deps, to, name, token string) {
	link := fmt.Sprintf("%s/verify.html?token=%s", deps.Cfg.PublicSiteURL, token)
	subj := "Verify your Livepeer Video Gateway signup"
	html := fmt.Sprintf(`<p>Hi %s,</p><p>Click to verify: <a href="%s">%s</a></p>`, name, link, link)
	text := fmt.Sprintf("Hi %s,\n\nClick to verify: %s\n", name, link)
	if err := deps.Email.Send(ctx, to, subj, html, text); err != nil {
		deps.Log.Error("verification email send failed", "err", err, "to", to)
	}
}

func deliverAPIKeyEmail(ctx context.Context, deps Deps, to, name, plainKey string) {
	subj := "Your Livepeer Video Gateway API key"
	html := fmt.Sprintf(`<p>Hi %s,</p>
<p>Your API key (store it now — we don't show it again):</p>
<pre>%s</pre>
<p>Use it as <code>Authorization: Bearer %s</code> against <code>%s/v1/*</code>.</p>`,
		name, plainKey, plainKey, deps.Cfg.BaseURL)
	text := fmt.Sprintf("Hi %s,\n\nYour API key (store it now): %s\n\nAuth: Bearer header against %s/v1/*.\n",
		name, plainKey, deps.Cfg.BaseURL)
	if err := deps.Email.Send(ctx, to, subj, html, text); err != nil {
		deps.Log.Error("api key email send failed", "err", err, "to", to)
	}
}

func ipHashFromCtx(_ context.Context, pepper string) string {
	// Real IP extraction happens at the reverse proxy layer; in dev we
	// don't have a stable IP source. Return a stable placeholder so the
	// per-IP rate limiter still gives consistent results.
	return crypto.HashWithPepper("dev-ip", pepper)
}

// asUUID is a tiny helper that parses or zero-values.
func asUUID(s string) uuid.UUID {
	id, _ := uuid.Parse(s)
	return id
}
