package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/crypto"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/service"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

func RegisterAdmin(api huma.API, deps Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "admin-list-waitlist",
		Method:      http.MethodGet,
		Path:        "/admin/waitlist",
		Summary:     "List waitlist rows",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		Status string `query:"status" enum:"pending,approved,rejected,all" default:"pending"`
		Limit  int    `query:"limit" default:"100" minimum:"1" maximum:"500"`
	}) (*AdminWaitlistOut, error) {
		rows, err := deps.Waitlist.ListByStatus(ctx, repo.WaitlistStatus(in.Status), in.Limit)
		if err != nil {
			return nil, huma.Error500InternalServerError("waitlist list failed", err)
		}
		out := &AdminWaitlistOut{}
		for _, r := range rows {
			out.Body.Items = append(out.Body.Items, WaitlistView{
				ID:              r.ID,
				Name:            r.Name,
				Email:           r.Email,
				EmailVerifiedAt: r.EmailVerifiedAt,
				Status:          string(r.Status),
				ApprovedAt:      r.ApprovedAt,
				ApprovedBy:      derefString(r.ApprovedBy),
				CreatedAt:       r.CreatedAt,
			})
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "admin-approve-waitlist",
		Method:      http.MethodPost,
		Path:        "/admin/waitlist/{id}/approve",
		Summary:     "Approve a waitlist row and email the API key",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		ID uuid.UUID `path:"id"`
	}) (*AdminApproveOut, error) {
		wl, err := deps.Waitlist.GetByID(ctx, in.ID)
		if err != nil || wl == nil {
			return nil, huma.Error404NotFound("waitlist row not found")
		}
		if wl.EmailVerifiedAt == nil {
			return nil, huma.Error409Conflict("email_not_verified")
		}
		if wl.Status != repo.WaitlistPending {
			return nil, huma.Error409Conflict("waitlist_not_pending")
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
		if _, err := deps.APIKeys.Insert(ctx, tx, wl.ID, "", prefix, hash); err != nil {
			return nil, huma.Error500InternalServerError("api key insert", err)
		}
		if err := deps.Waitlist.MarkApproved(ctx, tx, wl.ID, "admin"); err != nil {
			return nil, huma.Error500InternalServerError("waitlist update", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, huma.Error500InternalServerError("tx commit", err)
		}
		deliverAPIKeyEmail(ctx, deps, wl.Email, wl.Name, key)
		out := &AdminApproveOut{}
		out.Body.OK = true
		out.Body.KeyPrefix = prefix
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "admin-reject-waitlist",
		Method:      http.MethodPost,
		Path:        "/admin/waitlist/{id}/reject",
		Summary:     "Reject a waitlist row",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		ID uuid.UUID `path:"id"`
	}) (*GenericOK, error) {
		if err := deps.Waitlist.MarkRejected(ctx, in.ID, "admin"); err != nil {
			return nil, huma.Error500InternalServerError("waitlist reject", err)
		}
		out := &GenericOK{}
		out.Body.OK = true
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "admin-resend-verification",
		Method:      http.MethodPost,
		Path:        "/admin/waitlist/{id}/resend-verification",
		Summary:     "Re-mint and re-send the verification token",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		ID uuid.UUID `path:"id"`
	}) (*GenericOK, error) {
		wl, err := deps.Waitlist.GetByID(ctx, in.ID)
		if err != nil || wl == nil {
			return nil, huma.Error404NotFound("waitlist row not found")
		}
		token, err := crypto.RandomToken(32)
		if err != nil {
			return nil, huma.Error500InternalServerError("token mint", err)
		}
		hash := crypto.HashWithPepper(token, deps.Cfg.IPHashPepper)
		if err := deps.Waitlist.ResetVerificationToken(ctx, wl.ID, hash, time.Now().Add(24*time.Hour)); err != nil {
			return nil, huma.Error500InternalServerError("token reset", err)
		}
		deliverVerificationEmail(ctx, deps, wl.Email, wl.Name, token)
		out := &GenericOK{}
		out.Body.OK = true
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "admin-capabilities",
		Method:      http.MethodGet,
		Path:        "/admin/capabilities",
		Summary:     "Debug view of the cached capability catalog",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, _ *struct{}) (*AdminCapabilitiesOut, error) {
		rows, err := deps.Caps.ListActive(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("capabilities list", err)
		}
		out := &AdminCapabilitiesOut{}
		if meta, err := deps.Caps.GetRefreshMeta(ctx); err == nil && meta != nil {
			out.Body.LastRefreshAt = meta.LastRefreshAt
			out.Body.LastOutcome = meta.LastOutcome
			if meta.LastError != nil {
				out.Body.LastError = *meta.LastError
			}
			out.Body.CapabilityFilter = meta.CapabilityFilter
			out.Body.RowsMatched = meta.RowsMatched
		}
		// Backwards-compatible: keep SnapshotAt as the per-row max so existing
		// clients still parse, but operators should look at LastRefreshAt.
		out.Body.SnapshotAt, _ = deps.Caps.LastSnapshot(ctx)
		for _, c := range rows {
			out.Body.Items = append(out.Body.Items, CapabilityView{
				ID:              c.CapabilityID,
				Capability:      c.Capability,
				Offering:        c.Offering,
				InteractionMode: derefString(c.InteractionMode),
				Name:            derefString(c.Name),
				BrokerURL:       derefString(c.BrokerURL),
				EthAddress:      derefString(c.EthAddress),
				PriceWei:        bigToString(c.PricePerWorkUnitWei),
				Active:          c.Active,
			})
		}
		return out, nil
	})

	registerAdminUsers(api, deps)
	registerAdminUsage(api, deps)
	registerAdminLiveStreams(api, deps)
	registerAdminABRJobs(api, deps)
	registerAdminRegistry(api, deps)
}

// ── /admin/users + /admin/users/{id} ────────────────────────────────

func registerAdminUsers(api huma.API, deps Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "admin-list-users",
		Method:      http.MethodGet,
		Path:        "/admin/users",
		Summary:     "List approved users",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		Limit int `query:"limit" default:"200" minimum:"1" maximum:"500"`
	}) (*AdminUsersListOut, error) {
		rows, err := deps.Waitlist.ListByStatus(ctx, repo.WaitlistApproved, in.Limit)
		if err != nil {
			return nil, huma.Error500InternalServerError("users list failed", err)
		}
		out := &AdminUsersListOut{}
		for _, r := range rows {
			out.Body.Items = append(out.Body.Items, AdminUserSummary{
				ID:         r.ID,
				Name:       r.Name,
				Email:      r.Email,
				ApprovedAt: r.ApprovedAt,
				CreatedAt:  r.CreatedAt,
			})
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "admin-get-user",
		Method:      http.MethodGet,
		Path:        "/admin/users/{id}",
		Summary:     "User detail with API keys + usage summary",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		ID uuid.UUID `path:"id"`
	}) (*AdminUserDetailOut, error) {
		w, err := deps.Waitlist.GetByID(ctx, in.ID)
		if err != nil || w == nil {
			return nil, huma.Error404NotFound("user not found")
		}
		keys, err := deps.APIKeys.ListByWaitlist(ctx, w.ID)
		if err != nil {
			return nil, huma.Error500InternalServerError("api keys list", err)
		}
		summary, err := deps.Usage.SummaryByWaitlist(ctx, w.ID)
		if err != nil {
			return nil, huma.Error500InternalServerError("usage summary", err)
		}
		out := &AdminUserDetailOut{}
		out.Body.ID = w.ID
		out.Body.Email = w.Email
		out.Body.Name = w.Name
		out.Body.Status = string(w.Status)
		out.Body.EmailVerifiedAt = w.EmailVerifiedAt
		out.Body.ApprovedAt = w.ApprovedAt
		out.Body.CreatedAt = w.CreatedAt
		for _, k := range keys {
			out.Body.APIKeys = append(out.Body.APIKeys, APIKeyView{
				ID:         k.ID,
				Label:      derefString(k.Label),
				KeyPrefix:  k.KeyPrefix,
				CreatedAt:  k.CreatedAt,
				LastUsedAt: k.LastUsedAt,
				RevokedAt:  k.RevokedAt,
			})
		}
		out.Body.Usage.TotalRequests = summary.TotalRequests
		out.Body.Usage.CommittedTotal = summary.CommittedTotal
		out.Body.Usage.RefundedTotal = summary.RefundedTotal
		out.Body.Usage.OpenTotal = summary.OpenTotal
		out.Body.Usage.LastUsedAt = summary.LastUsedAt
		return out, nil
	})
}

// ── /admin/usage (aggregate by API key) ─────────────────────────────

func registerAdminUsage(api huma.API, deps Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "admin-usage",
		Method:      http.MethodGet,
		Path:        "/admin/usage",
		Summary:     "Aggregate usage by API key (joined to owning user email)",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		Limit int `query:"limit" default:"200" minimum:"1" maximum:"500"`
	}) (*AdminUsageOut, error) {
		rows, err := deps.Usage.SummaryByAPIKey(ctx, in.Limit)
		if err != nil {
			return nil, huma.Error500InternalServerError("usage summary", err)
		}
		out := &AdminUsageOut{}
		for _, r := range rows {
			out.Body.Items = append(out.Body.Items, AdminUsageRow{
				APIKeyID:       r.APIKeyID,
				WaitlistID:     r.WaitlistID,
				Email:          r.Email,
				KeyPrefix:      r.KeyPrefix,
				TotalRequests:  r.TotalRequests,
				CommittedTotal: r.CommittedTotal,
				RefundedTotal:  r.RefundedTotal,
				OpenTotal:      r.OpenTotal,
				LastUsedAt:     r.LastUsedAt,
			})
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "admin-usage-by-capability",
		Method:      http.MethodGet,
		Path:        "/admin/usage-by-capability",
		Summary:     "Per-capability rollup separating gateway commit vs runner outcome",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		SinceHours int `query:"since_hours" default:"168" minimum:"1" maximum:"8760"`
	}) (*AdminUsageByCapOut, error) {
		since := time.Now().Add(-time.Duration(in.SinceHours) * time.Hour)
		rows, err := deps.Usage.SummaryByCapability(ctx, since)
		if err != nil {
			return nil, huma.Error500InternalServerError("by-capability summary", err)
		}
		out := &AdminUsageByCapOut{}
		out.Body.SinceHours = in.SinceHours
		for _, r := range rows {
			out.Body.Items = append(out.Body.Items, AdminUsageByCapRow{
				Capability:      r.Capability,
				TotalRequests:   r.TotalRequests,
				CommittedTotal:  r.CommittedTotal,
				RefundedTotal:   r.RefundedTotal,
				OpenTotal:       r.OpenTotal,
				RunnerSucceeded: r.RunnerSucceeded,
				RunnerFailed:    r.RunnerFailed,
				LastUsedAt:      r.LastUsedAt,
			})
		}
		return out, nil
	})
}

// ── /admin/live-streams (transcode-specific) ────────────────────────

func registerAdminLiveStreams(api huma.API, deps Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "admin-live-streams",
		Method:      http.MethodGet,
		Path:        "/admin/live-streams",
		Summary:     "RTMP→HLS sessions across all users",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		ActiveOnly bool `query:"active_only" doc:"If true, only provisioning + live sessions."`
		Limit      int  `query:"limit" default:"200" minimum:"1" maximum:"500"`
	}) (*AdminLiveStreamsOut, error) {
		rows, err := deps.Live.ListAll(ctx, in.ActiveOnly, in.Limit)
		if err != nil {
			return nil, huma.Error500InternalServerError("live list failed", err)
		}
		out := &AdminLiveStreamsOut{}
		for _, r := range rows {
			item := AdminLiveStreamView{
				ID:              r.ID,
				APIKeyID:        r.APIKeyID,
				Name:            derefString(r.Name),
				Status:          string(r.Status),
				Capability:      r.Capability,
				Offering:        r.Offering,
				BrokerURL:       derefString(r.BrokerURL),
				EthAddress:      derefString(r.EthAddress),
				IngestURL:       derefString(r.IngestURL),
				PlaybackURL:     derefString(r.PlaybackURL),
				ErrorText:       derefString(r.ErrorText),
				CreatedAt:       r.CreatedAt,
				StartedAt:       r.StartedAt,
				LastHeartbeatAt: r.LastHeartbeatAt,
				EndedAt:         r.EndedAt,
			}
			// Surface the broker's runner-status surface (ingest +
			// output blocks per status-hardening spec) so operators
			// can see ConnectedPublisher / PutFailureCount / etc.
			// without doing a broker round-trip. Reconciler caches
			// this on each tick; admin UI parses what's present.
			if len(r.RunnerStatusJSON) > 0 {
				item.RunnerStatus = json.RawMessage(r.RunnerStatusJSON)
			}
			out.Body.Items = append(out.Body.Items, item)
		}
		return out, nil
	})
}

// ── /admin/abr-jobs (transcode-specific) ────────────────────────────

func registerAdminABRJobs(api huma.API, deps Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "admin-abr-jobs",
		Method:      http.MethodGet,
		Path:        "/admin/abr-jobs",
		Summary:     "Recent ABR ladder transcode jobs across all users",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		SinceHours int `query:"since_hours" default:"168" minimum:"1" maximum:"8760"`
		Limit      int `query:"limit"       default:"200" minimum:"1" maximum:"500"`
	}) (*AdminABRJobsOut, error) {
		since := time.Now().Add(-time.Duration(in.SinceHours) * time.Hour)
		rows, err := deps.Usage.ListByCapability(ctx, deps.Cfg.ABRCapability, since, in.Limit)
		if err != nil {
			return nil, huma.Error500InternalServerError("abr jobs list", err)
		}
		out := &AdminABRJobsOut{}
		for _, r := range rows {
			out.Body.Items = append(out.Body.Items, AdminABRJobView{
				WorkID:      r.WorkID,
				APIKeyID:    r.APIKeyID,
				RunnerJobID: derefString(r.RunnerJobID),
				State:       string(r.State),
				BrokerURL:   derefString(r.BrokerURL),
				LatencyMs:   derefInt(r.LatencyMs),
				StatusCode:  derefInt(r.StatusCode),
				ErrorText:   derefString(r.ErrorText),
				CreatedAt:   r.CreatedAt,
				ResolvedAt:  r.ResolvedAt,
			})
		}
		return out, nil
	})
}

// ── /admin/registry/{summary,candidates,health} ─────────────────────

func registerAdminRegistry(api huma.API, deps Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "admin-registry-summary",
		Method:      http.MethodGet,
		Path:        "/admin/registry/summary",
		Summary:     "High-level numbers: cached rows + filter + last refresh",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, _ *struct{}) (*AdminRegistrySummaryOut, error) {
		out := &AdminRegistrySummaryOut{}
		if meta, err := deps.Caps.GetRefreshMeta(ctx); err == nil && meta != nil {
			out.Body.LastRefreshAt = meta.LastRefreshAt
			out.Body.LastOutcome = meta.LastOutcome
			if meta.LastError != nil {
				out.Body.LastError = *meta.LastError
			}
			out.Body.CapabilityFilter = meta.CapabilityFilter
			out.Body.RowsMatched = meta.RowsMatched
		}
		rows, _ := deps.Caps.ListActive(ctx)
		out.Body.ActiveCount = len(rows)
		// Per-capability breakdown.
		byCap := map[string]int{}
		for _, r := range rows {
			byCap[r.Capability]++
		}
		for cap, n := range byCap {
			out.Body.ByCapability = append(out.Body.ByCapability, AdminRegistryCapCount{
				Capability: cap,
				Count:      n,
			})
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "admin-registry-candidates",
		Method:      http.MethodGet,
		Path:        "/admin/registry/candidates",
		Summary:     "Live SelectMany candidates straight from the resolver (uncached)",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, in *struct {
		Capability string `query:"capability" doc:"defaults to ABR_CAPABILITY"`
		Offering   string `query:"offering"   default:"default"`
	}) (*AdminRegistryCandidatesOut, error) {
		if deps.Resolver == nil {
			return nil, huma.Error503ServiceUnavailable("resolver_unavailable")
		}
		cap := in.Capability
		if cap == "" {
			cap = deps.Cfg.ABRCapability
		}
		cands, err := deps.Resolver.SelectMany(ctx, service.SelectRequest{
			Capability: cap,
			Offering:   in.Offering,
		})
		if err != nil {
			return nil, huma.Error502BadGateway("resolver SelectMany failed", err)
		}
		out := &AdminRegistryCandidatesOut{}
		out.Body.Capability = cap
		out.Body.Offering = in.Offering
		for _, c := range cands {
			out.Body.Items = append(out.Body.Items, AdminRegistryCandidate{
				WorkerURL:       c.WorkerURL,
				EthAddress:      c.EthAddress,
				Capability:      c.Capability,
				Offering:        c.Offering,
				PriceWei:        bigToString(c.PricePerWorkUnitWei),
				WorkUnit:        c.WorkUnit,
				QuoteID:         c.QuoteID,
				QuoteVersion:    int64(c.QuoteVersion),
				UnitsPerPrice:   int64(c.UnitsPerPrice),
			})
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "admin-registry-health",
		Method:      http.MethodGet,
		Path:        "/admin/registry/health",
		Summary:     "In-memory route health (cooldowns + failure counters)",
		Tags:        []string{"admin"},
	}, func(ctx context.Context, _ *struct{}) (*AdminRouteHealthOut, error) {
		out := &AdminRouteHealthOut{}
		if deps.Health == nil {
			return out, nil
		}
		now := time.Now()
		snap := deps.Health.Snapshot(now)
		threshold, cooldown := deps.Health.Thresholds()
		out.Body.FailureThreshold = threshold
		out.Body.CooldownSeconds = int(cooldown.Seconds())
		for _, e := range snap {
			out.Body.Items = append(out.Body.Items, AdminRouteHealthEntry{
				Key:            e.Key,
				ConsecFailures: e.ConsecFailures,
				CoolingDown:    e.CoolingDown,
				CooldownUntil:  e.CooldownUntil,
			})
		}
		return out, nil
	})
}

// ── output types ────────────────────────────────────────────────────

type AdminUserSummary struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Email      string     `json:"email"`
	ApprovedAt *time.Time `json:"approved_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type AdminUsersListOut struct {
	Body struct {
		Items []AdminUserSummary `json:"items"`
	}
}

type AdminUserUsageSummary struct {
	TotalRequests  int        `json:"total_requests"`
	CommittedTotal int        `json:"committed_total"`
	RefundedTotal  int        `json:"refunded_total"`
	OpenTotal      int        `json:"open_total"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
}

type AdminUserDetailOut struct {
	Body struct {
		ID              uuid.UUID             `json:"id"`
		Email           string                `json:"email"`
		Name            string                `json:"name"`
		Status          string                `json:"status"`
		EmailVerifiedAt *time.Time            `json:"email_verified_at,omitempty"`
		ApprovedAt      *time.Time            `json:"approved_at,omitempty"`
		CreatedAt       time.Time             `json:"created_at"`
		APIKeys         []APIKeyView          `json:"api_keys"`
		Usage           AdminUserUsageSummary `json:"usage"`
	}
}

type AdminUsageRow struct {
	APIKeyID       uuid.UUID  `json:"api_key_id"`
	WaitlistID     uuid.UUID  `json:"waitlist_id"`
	Email          string     `json:"email"`
	KeyPrefix      string     `json:"key_prefix"`
	TotalRequests  int        `json:"total_requests"`
	CommittedTotal int        `json:"committed_total"`
	RefundedTotal  int        `json:"refunded_total"`
	OpenTotal      int        `json:"open_total"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
}

type AdminUsageOut struct {
	Body struct {
		Items []AdminUsageRow `json:"items"`
	}
}

type AdminUsageByCapRow struct {
	Capability      string     `json:"capability"`
	TotalRequests   int        `json:"total_requests"`
	CommittedTotal  int        `json:"committed_total"`
	RefundedTotal   int        `json:"refunded_total"`
	OpenTotal       int        `json:"open_total"`
	RunnerSucceeded int        `json:"runner_succeeded"`
	RunnerFailed    int        `json:"runner_failed"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
}

type AdminUsageByCapOut struct {
	Body struct {
		SinceHours int                  `json:"since_hours"`
		Items      []AdminUsageByCapRow `json:"items"`
	}
}

type AdminLiveStreamView struct {
	ID              uuid.UUID  `json:"id"`
	APIKeyID        uuid.UUID  `json:"api_key_id"`
	Name            string     `json:"name,omitempty"`
	Status          string     `json:"status"`
	IngestMode      string     `json:"ingest_mode,omitempty"`
	Capability      string     `json:"capability"`
	Offering        string     `json:"offering"`
	BrokerURL       string     `json:"broker_url,omitempty"`
	EthAddress      string     `json:"eth_address,omitempty"`
	IngestURL       string     `json:"ingest_url,omitempty"`
	PlaybackURL     string     `json:"playback_url,omitempty"`
	ErrorText       string     `json:"error_text,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`
	// RunnerStatus is the broker's runner-status surface — ingest +
	// output blocks per the status-hardening spec. Cached by the
	// reconciler each tick. Shape is intentionally loose because the
	// fields evolve faster than gateway schema.
	RunnerStatus json.RawMessage `json:"runner_status,omitempty"`
}

type AdminLiveStreamsOut struct {
	Body struct {
		Items []AdminLiveStreamView `json:"items"`
	}
}

type AdminABRJobView struct {
	WorkID      uuid.UUID  `json:"work_id"`
	APIKeyID    uuid.UUID  `json:"api_key_id"`
	RunnerJobID string     `json:"runner_job_id,omitempty"`
	State       string     `json:"state"`
	BrokerURL   string     `json:"broker_url,omitempty"`
	LatencyMs   int        `json:"latency_ms,omitempty"`
	StatusCode  int        `json:"status_code,omitempty"`
	ErrorText   string     `json:"error_text,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
}

type AdminABRJobsOut struct {
	Body struct {
		Items []AdminABRJobView `json:"items"`
	}
}

type AdminRegistryCapCount struct {
	Capability string `json:"capability"`
	Count      int    `json:"count"`
}

type AdminRegistrySummaryOut struct {
	Body struct {
		LastRefreshAt    time.Time               `json:"last_refresh_at"`
		LastOutcome      string                  `json:"last_outcome"`
		LastError        string                  `json:"last_error,omitempty"`
		CapabilityFilter []string                `json:"capability_filter"`
		RowsMatched      int                     `json:"rows_matched"`
		ActiveCount      int                     `json:"active_count"`
		ByCapability     []AdminRegistryCapCount `json:"by_capability"`
	}
}

type AdminRegistryCandidate struct {
	WorkerURL     string `json:"worker_url"`
	EthAddress    string `json:"eth_address,omitempty"`
	Capability    string `json:"capability"`
	Offering      string `json:"offering"`
	PriceWei      string `json:"price_per_work_unit_wei,omitempty"`
	WorkUnit      string `json:"work_unit,omitempty"`
	QuoteID       string `json:"quote_id,omitempty"`
	QuoteVersion  int64  `json:"quote_version,omitempty"`
	UnitsPerPrice int64  `json:"units_per_price,omitempty"`
}

type AdminRegistryCandidatesOut struct {
	Body struct {
		Capability string                   `json:"capability"`
		Offering   string                   `json:"offering"`
		Items      []AdminRegistryCandidate `json:"items"`
	}
}

type AdminRouteHealthEntry struct {
	Key            string    `json:"key"`
	ConsecFailures int       `json:"consec_failures"`
	CoolingDown    bool      `json:"cooling_down"`
	CooldownUntil  time.Time `json:"cooldown_until"`
}

type AdminRouteHealthOut struct {
	Body struct {
		FailureThreshold int                      `json:"failure_threshold"`
		CooldownSeconds  int                      `json:"cooldown_seconds"`
		Items            []AdminRouteHealthEntry  `json:"items"`
	}
}

type WaitlistView struct {
	ID              uuid.UUID  `json:"id"`
	Name            string     `json:"name"`
	Email           string     `json:"email"`
	EmailVerifiedAt *time.Time `json:"email_verified_at,omitempty"`
	Status          string     `json:"status"`
	ApprovedAt      *time.Time `json:"approved_at,omitempty"`
	ApprovedBy      string     `json:"approved_by,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

type AdminWaitlistOut struct {
	Body struct {
		Items []WaitlistView `json:"items"`
	}
}

type AdminApproveOut struct {
	Body struct {
		OK        bool   `json:"ok"`
		KeyPrefix string `json:"key_prefix"`
	}
}

type CapabilityView struct {
	ID              string `json:"id"`
	Capability      string `json:"capability"`
	Offering        string `json:"offering"`
	InteractionMode string `json:"interaction_mode,omitempty"`
	Name            string `json:"name,omitempty"`
	BrokerURL       string `json:"broker_url,omitempty"`
	EthAddress      string `json:"eth_address,omitempty"`
	PriceWei        string `json:"price_per_work_unit_wei,omitempty"`
	Active          bool   `json:"active"`
}

type AdminCapabilitiesOut struct {
	Body struct {
		Items            []CapabilityView `json:"items"`
		SnapshotAt       time.Time        `json:"snapshot_at"`
		LastRefreshAt    time.Time        `json:"last_refresh_at"`
		LastOutcome      string           `json:"last_outcome"`
		LastError        string           `json:"last_error,omitempty"`
		RowsMatched      int              `json:"rows_matched"`
		CapabilityFilter []string         `json:"capability_filter"`
	}
}

func bigToString(b interface{}) string {
	if b == nil {
		return ""
	}
	if s, ok := b.(interface{ String() string }); ok {
		return s.String()
	}
	return ""
}
