package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/abr"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/crypto"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/livepeer"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/loc"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

// Default rough estimate: 600 work units per second of input video for ABR ladder.
const abrUnitsPerInputSecond = 600

func RegisterV1(api huma.API, deps Deps) {
	registerV1Capabilities(api, deps)
	registerV1Upload(api, deps)
	registerV1ABR(api, deps)
	registerV1Live(api, deps)
}

// ── /v1/capabilities ──────────────────────────────────────────────────

type CapabilitiesOut struct {
	Body struct {
		Object string             `json:"object"`
		Data   []CapabilityListed `json:"data"`
		SnapshotAt time.Time      `json:"snapshot_at"`
	}
}

type CapabilityListed struct {
	ID              string          `json:"id"`
	Capability      string          `json:"capability"`
	Offering        string          `json:"offering"`
	InteractionMode string          `json:"interaction_mode,omitempty"`
	Name            string          `json:"name,omitempty"`
	Category        string          `json:"category,omitempty"`
	PriceWei        string          `json:"price_per_work_unit_wei,omitempty"`
	Extra           json.RawMessage `json:"extra,omitempty"`
	Constraints     json.RawMessage `json:"constraints,omitempty"`
}

func registerV1Capabilities(api huma.API, deps Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "v1-capabilities",
		Method:      http.MethodGet,
		Path:        "/api/v1/capabilities",
		Summary:     "List transcode capabilities advertised by the network",
		Tags:        []string{"v1"},
	}, func(ctx context.Context, _ *struct{}) (*CapabilitiesOut, error) {
		rows, err := deps.Caps.ListActive(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("capabilities list", err)
		}
		snap, _ := deps.Caps.LastSnapshot(ctx)
		out := &CapabilitiesOut{}
		out.Body.Object = "list"
		out.Body.SnapshotAt = snap
		for _, c := range rows {
			out.Body.Data = append(out.Body.Data, CapabilityListed{
				ID:              c.CapabilityID,
				Capability:      c.Capability,
				Offering:        c.Offering,
				InteractionMode: derefString(c.InteractionMode),
				Name:            derefString(c.Name),
				Category:        derefString(c.Category),
				PriceWei:        bigStr(c.PricePerWorkUnitWei),
				Extra:           c.ExtraJSON,
				Constraints:     c.ConstraintsJSON,
			})
		}
		return out, nil
	})
}

// ── /v1/abr/upload-url ────────────────────────────────────────────────

type UploadURLIn struct {
	Body struct {
		Filename    string `json:"filename" required:"true" maxLength:"200"`
		ContentType string `json:"content_type" required:"true" example:"video/mp4"`
	}
}

type UploadURLOut struct {
	Body struct {
		UploadURL string    `json:"upload_url"`
		ObjectURL string    `json:"object_url"`
		ExpiresAt time.Time `json:"expires_at"`
	}
}

func registerV1Upload(api huma.API, deps Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "v1-abr-upload-url",
		Method:      http.MethodPost,
		Path:        "/api/v1/abr/upload-url",
		Summary:     "Get a presigned S3 PUT URL for VOD ingest",
		Tags:        []string{"v1"},
	}, func(ctx context.Context, in *UploadURLIn) (*UploadURLOut, error) {
		if deps.S3 == nil {
			return nil, huma.Error503ServiceUnavailable("s3_unavailable")
		}
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("invalid_api_key")
		}
		// Per-user prefix keeps tenant uploads isolated and enables future GC.
		objKey := path.Clean(fmt.Sprintf("abr/%s/%s/%s",
			ak.ID.String(), uuid.NewString(), strings.ReplaceAll(in.Body.Filename, "..", "")))
		p, err := deps.S3.PresignPut(ctx, objKey, in.Body.ContentType)
		if err != nil {
			return nil, huma.Error500InternalServerError("presign failed", err)
		}
		out := &UploadURLOut{}
		out.Body.UploadURL = p.UploadURL
		out.Body.ObjectURL = p.ObjectURL
		out.Body.ExpiresAt = p.ExpiresAt
		return out, nil
	})
}

// ── /v1/abr ───────────────────────────────────────────────────────────

type ABRRung struct {
	Name        string `json:"name"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	BitrateKbps int    `json:"bitrate_kbps,omitempty"`
	Passthrough bool   `json:"passthrough,omitempty"`
}

type ABRLadder struct {
	Rungs []ABRRung `json:"rungs,omitempty"`
}

type ABRIn struct {
	Body struct {
		InputURL      string     `json:"input_url" required:"true" format:"uri"`
		Preset        string     `json:"preset,omitempty" enum:"abr-standard,abr-premium,abr-mobile,abr-hevc,abr-av1" doc:"Runner preset. Defaults to abr-standard."`
		Ladder        *ABRLadder `json:"ladder,omitempty"`
		EstimatedSecs int        `json:"estimated_input_seconds,omitempty" minimum:"0"`
	}
}

type ABRRendition struct {
	Name        string `json:"name"`
	PlaylistURL string `json:"playlist_url"`
	Bandwidth   int    `json:"bandwidth"`
}

type ABRJob struct {
	ID                uuid.UUID      `json:"id"`
	Status            string         `json:"status"`
	Phase             string         `json:"phase,omitempty"`
	OverallProgress   float64        `json:"overall_progress,omitempty"`
	Error             string         `json:"error,omitempty"`
	ErrorCode         string         `json:"error_code,omitempty"`
	GPU               string         `json:"gpu,omitempty"`
	InputURL          string         `json:"input_url"`
	MasterPlaylistURL string         `json:"master_playlist_url,omitempty"`
	Renditions        []ABRRendition `json:"renditions,omitempty"`
	BrokerURL         string         `json:"broker_url,omitempty"`
	EthAddress        string         `json:"eth_address,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
}

type ABROut struct {
	Body struct {
		Job ABRJob `json:"job"`
	}
}

func registerV1ABR(api huma.API, deps Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "v1-abr-submit",
		Method:      http.MethodPost,
		Path:        "/api/v1/abr",
		Summary:     "Submit an ABR ladder transcode job",
		Tags:        []string{"v1"},
	}, func(ctx context.Context, in *ABRIn) (*ABROut, error) {
		if deps.LOC == nil {
			return nil, huma.Error503ServiceUnavailable("loc_unavailable")
		}
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("invalid_api_key")
		}
		if deps.S3 == nil {
			return nil, huma.Error503ServiceUnavailable("s3_unavailable",
				fmt.Errorf("output_urls require S3 — not configured"))
		}
		presetName := in.Body.Preset
		if presetName == "" {
			presetName = "abr-standard"
		}
		preset, ok := abr.Get(presetName)
		if !ok {
			return nil, huma.Error400BadRequest(
				fmt.Sprintf("unknown preset %q (known: %v)", presetName, abr.Names()))
		}
		spec := deps.CapMap.ABR
		workID := uuid.New()
		// LOC requires estimated_units > 0; jobs are settled against the
		// same estimate after dispatch (the runner webhook carries no
		// unit counts), so the estimate is also what we bill.
		estUnits := int64(1)
		if in.Body.EstimatedSecs > 0 {
			estUnits = int64(in.Body.EstimatedSecs * abrUnitsPerInputSecond)
		}
		res, err := deps.Usage.Open(ctx, repo.OpenInput{
			APIKeyID:           ak.ID,
			WorkID:             workID,
			Capability:         spec.Capability,
			Offering:           spec.DefaultOffering,
			EstimatedWorkUnits: &estUnits,
		})
		if err != nil {
			return nil, huma.Error500InternalServerError("reservation open failed", err)
		}

		// Broker request body is job-independent — mint outputs + webhook
		// hookup once, reuse across rotation retries.
		outputs, masterPlaybackURL, err := mintABROutputs(ctx, deps, ak.ID.String(), workID.String(), preset)
		if err != nil {
			_ = deps.Usage.Refund(ctx, res.ID, 500, err.Error())
			return nil, huma.Error500InternalServerError("mint output_urls", err)
		}
		body := map[string]any{
			"input_url":   in.Body.InputURL,
			"preset":      presetName,
			"output_urls": outputs,
		}
		if in.Body.Ladder != nil {
			body["ladder"] = in.Body.Ladder
		}
		// Webhook hookup — runner POSTs status transitions back when both
		// webhook_url + webhook_secret are set. Unset → gateway stays
		// oblivious (same as before).
		if deps.Cfg.GatewayPublicURL != "" {
			secret, _ := crypto.RandomToken(32)
			if err := deps.Usage.SetWebhookSecret(ctx, workID, secret); err == nil {
				body["webhook_url"] = strings.TrimRight(deps.Cfg.GatewayPublicURL, "/") +
					"/api/webhooks/abr?work_id=" + workID.String()
				body["webhook_secret"] = secret
			}
		}

		// abr-runner's submit response shape: { job_id, status, renditions:[name,...] }.
		// The runner returns rendition NAMES (strings); we map them to the
		// playback URLs the gateway minted so the client gets ready-to-play
		// rendition rows in the response without a second round-trip.
		type brokerJob struct {
			JobID      string   `json:"job_id"`
			Status     string   `json:"status"`
			Renditions []string `json:"renditions,omitempty"`
		}

		// Create-job + dispatch loop. LOC selects the route and encumbers
		// worst-case credit at create; every failed create attempt MUST be
		// settled with actual_units=0 to release the encumbrance. On
		// INVALID_RECIPIENT_RAND (receiver rotated its payment session) LOC
		// has no ReportPaymentResult — recovery is settle(0) + a fresh
		// CreateJob, which mints a fresh envelope. One retry, then fail.
		const maxCreates = 2
		var (
			j        brokerJob
			job      *loc.CreateJobResponse
			rotated  bool
			started  = time.Now()
		)
		for attempt := 1; ; attempt++ {
			job, err = deps.LOC.CreateJob(ctx, loc.CreateJobRequest{
				Capability:     spec.Capability,
				Offering:       spec.DefaultOffering,
				EstimatedUnits: estUnits,
			})
			if err != nil {
				deps.Metrics.ProxyReservationsTotal.WithLabelValues(spec.Capability, "refunded").Inc()
				switch {
				case loc.IsNoRoute(err):
					_ = deps.Usage.Refund(ctx, res.ID, 502, err.Error())
					return nil, huma.Error502BadGateway("no_capable_broker", err)
				case loc.IsInsufficientCredit(err):
					_ = deps.Usage.Refund(ctx, res.ID, 402, err.Error())
					return nil, huma.NewError(http.StatusPaymentRequired, "insufficient_credit", err)
				default:
					_ = deps.Usage.Refund(ctx, res.ID, 502, err.Error())
					return nil, huma.Error502BadGateway("loc_create_job_failed", err)
				}
			}
			// Link the LOC job BEFORE the broker call so a crash mid-
			// dispatch leaves a 'pending' row the settle janitor releases.
			// On a rotation retry this overwrites the abandoned job's ids —
			// its settle(0) below has its own retries; a residual leak is
			// surfaced via the warn log.
			_ = deps.Usage.SetLOCJob(ctx, workID, job.JobID, job.WorkID,
				job.FundedValueWei.BigInt(), job.ExpectedValueWei.BigInt())

			var payment []byte
			if payment, err = job.PaymentBytes(); err == nil {
				dispatchURL := strings.TrimRight(job.BrokerURL, "/") + "/v1/cap"
				err = deps.HTTP.PostJSON(ctx, dispatchURL,
					spec.Capability, spec.DefaultOffering, RequestIDFrom(ctx), payment, body, &j)
			}
			if err == nil {
				break
			}
			// Release this attempt's worst-case encumbrance.
			settleLOCJob(ctx, deps, res.ID, job.JobID, 0, "broker_dispatch_failed")
			if livepeer.IsInvalidRecipientRandError(err) && attempt < maxCreates {
				rotated = true
				deps.Metrics.SessionRotationRetries.WithLabelValues(spec.Capability, "retried").Inc()
				deps.Log.Warn("abr: session rotation; re-creating LOC job",
					"work_id", workID, "broker", job.BrokerURL, "err", err)
				continue
			}
			break
		}
		latency := int(time.Since(started).Milliseconds())
		if err != nil {
			_ = deps.Usage.Refund(ctx, res.ID, 502, err.Error())
			deps.Metrics.ProxyReservationsTotal.WithLabelValues(spec.Capability, "refunded").Inc()
			return nil, huma.Error502BadGateway("upstream_broker_error", err)
		}
		if rotated {
			deps.Metrics.SessionRotationRetries.WithLabelValues(spec.Capability, "succeeded").Inc()
		}
		statusCode := 200
		_ = deps.Usage.Commit(ctx, res.ID, repo.CommitInput{
			BrokerURL: job.BrokerURL,
			// LOC owns the recipient identity now — no eth_address surfaced.
			LatencyMs:  &latency,
			StatusCode: &statusCode,
		})
		// Persist the runner-assigned job id so GET /v1/abr/{work_id}
		// can ask the runner for live status.
		if j.JobID != "" {
			_ = deps.Usage.SetRunnerJobID(ctx, workID, j.JobID)
		}
		deps.Metrics.ProxyReservationsTotal.WithLabelValues(spec.Capability, "committed").Inc()
		// Settle against the estimate now — releases funded − billed back
		// to the credit pool. On failure the row stays 'pending' and the
		// settle janitor retries.
		settleLOCJob(ctx, deps, res.ID, job.JobID, estUnits, "dispatched")
		// Convert the runner's rendition-name list into URL-bearing rows by
		// pairing each name with the playback URL the gateway already minted.
		rendOut := make([]ABRRendition, 0, len(j.Renditions))
		for _, name := range j.Renditions {
			rendOut = append(rendOut, ABRRendition{
				Name:        name,
				PlaylistURL: deps.S3.PublicObjectURL(fmt.Sprintf("abr-out/%s/%s/%s/playlist.m3u8", ak.ID, workID, name)),
			})
		}
		out := &ABROut{}
		out.Body.Job = ABRJob{
			ID:                workID,
			Status:            j.Status,
			InputURL:          in.Body.InputURL,
			MasterPlaylistURL: masterPlaybackURL,
			Renditions:        rendOut,
			BrokerURL:         job.BrokerURL,
			CreatedAt:         res.CreatedAt,
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "v1-abr-get",
		Method:      http.MethodGet,
		Path:        "/api/v1/abr/{id}",
		Summary:     "Get the status of an ABR job",
		Tags:        []string{"v1"},
	}, func(ctx context.Context, in *struct {
		ID uuid.UUID `path:"id"`
	}) (*ABROut, error) {
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("invalid_api_key")
		}
		// The ID we hand to clients is the reservation's work_id (not the
		// internal PK). Look up by work_id; ask the runner (via broker) for
		// live status if we have its job id; fall back to a master.m3u8
		// HEAD probe when the runner's status endpoint isn't reachable.
		row, err := deps.Usage.GetByWorkID(ctx, in.ID)
		if err != nil || row == nil || row.APIKeyID != ak.ID {
			return nil, huma.Error404NotFound("not_found")
		}
		masterKey := fmt.Sprintf("abr-out/%s/%s/master.m3u8", ak.ID, in.ID)
		masterURL := deps.S3.PublicObjectURL(masterKey)

		var (
			gatewayStatus = "processing" // succeeded | failed | processing | dispatched (no runner job id yet)
			phase         string
			progress      float64
			runnerErr     string
			runnerErrCode string
			runnerGPU     string
		)

		// First preference: the runner's last webhook payload, if any.
		// The webhook flow is push-based and authoritative — if it's
		// landed we use it without paying for a broker round-trip.
		if row.RunnerStatus != nil && *row.RunnerStatus != "" {
			phase = derefString(row.RunnerPhase)
			if row.RunnerProgress != nil {
				progress = *row.RunnerProgress
			}
			runnerErr = derefString(row.RunnerErrorText)
			runnerErrCode = derefString(row.RunnerErrorCode)
			switch *row.RunnerStatus {
			case "complete":
				gatewayStatus = "succeeded"
			case "error":
				gatewayStatus = "failed"
			default:
				gatewayStatus = "processing"
			}
		} else if row.RunnerJobID != nil && *row.RunnerJobID != "" && row.BrokerURL != nil && *row.BrokerURL != "" {
			rs, err := deps.HTTP.QueryABRRunnerStatus(ctx, *row.BrokerURL, *row.RunnerJobID)
			switch {
			case err != nil:
				deps.Log.Debug("runner status query failed; falling back to HEAD probe",
					"work_id", in.ID, "err", err)
			case rs != nil:
				phase = rs.Phase
				progress = rs.OverallProgress
				runnerErr = rs.Error
				runnerErrCode = rs.ErrorCode
				runnerGPU = rs.GPU
				switch rs.Status {
				case "complete":
					gatewayStatus = "succeeded"
				case "error":
					gatewayStatus = "failed"
				default:
					gatewayStatus = "processing"
				}
			}
		}
		if row.RunnerStatus == nil && (row.RunnerJobID == nil || *row.RunnerJobID == "") {
			gatewayStatus = "dispatched"
		}

		// Fallback / corroboration: master.m3u8 in the bucket is also a "done" signal.
		if gatewayStatus == "processing" || gatewayStatus == "dispatched" {
			if exists, _ := deps.S3.ObjectExists(ctx, masterKey); exists {
				gatewayStatus = "succeeded"
			}
		}

		preset, _ := abr.Get("abr-standard") // default; refine when we persist the picked preset
		rendOut := make([]ABRRendition, 0, len(preset.Renditions))
		for _, r := range preset.Renditions {
			rendOut = append(rendOut, ABRRendition{
				Name:        r.Name,
				PlaylistURL: deps.S3.PublicObjectURL(fmt.Sprintf("abr-out/%s/%s/%s/playlist.m3u8", ak.ID, in.ID, r.Name)),
			})
		}
		out := &ABROut{}
		out.Body.Job = ABRJob{
			ID:                row.WorkID,
			Status:            gatewayStatus,
			Phase:             phase,
			OverallProgress:   progress,
			Error:             runnerErr,
			ErrorCode:         runnerErrCode,
			GPU:               runnerGPU,
			MasterPlaylistURL: masterURL,
			Renditions:        rendOut,
			BrokerURL:         derefString(row.BrokerURL),
			EthAddress:        derefString(row.EthAddress),
			CreatedAt:         row.CreatedAt,
		}
		return out, nil
	})

	// DELETE /v1/abr/objects — drop the source upload and (if the job ran)
	// every output the runner wrote. The caller can pass either:
	//   - object_url (the s3-public URL we returned from /v1/abr/upload-url)
	//   - work_id    (the job id returned from /v1/abr submit; deletes the
	//                 entire output prefix abr-out/<api_key_id>/<work_id>/)
	// Both fields are optional independently; missing-key deletes are no-ops.
	// Authorization is per-prefix: we only allow deletes under the calling
	// API key's namespace (`abr/<api_key_id>/...` or `abr-out/<api_key_id>/...`).
	huma.Register(api, huma.Operation{
		OperationID: "v1-abr-delete-objects",
		Method:      http.MethodDelete,
		Path:        "/api/v1/abr/objects",
		Summary:     "Delete a VOD upload and its transcode outputs from S3",
		Tags:        []string{"v1"},
	}, func(ctx context.Context, in *struct {
		Body struct {
			ObjectURL string     `json:"object_url,omitempty"`
			WorkID    *uuid.UUID `json:"work_id,omitempty"`
		}
	}) (*ABRDeleteOut, error) {
		if deps.S3 == nil {
			return nil, huma.Error503ServiceUnavailable("s3_unavailable")
		}
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("invalid_api_key")
		}
		out := &ABRDeleteOut{}

		if in.Body.ObjectURL != "" {
			key := deps.S3.KeyFromURL(in.Body.ObjectURL)
			if key == "" {
				return nil, huma.Error400BadRequest("object_url is not a recognized bucket URL")
			}
			expectedPrefix := fmt.Sprintf("abr/%s/", ak.ID)
			if !strings.HasPrefix(key, expectedPrefix) {
				return nil, huma.Error403Forbidden("object_url is outside your namespace")
			}
			if err := deps.S3.DeleteObject(ctx, key); err != nil {
				return nil, huma.Error500InternalServerError("delete input failed", err)
			}
			out.Body.InputDeleted = true
		}

		if in.Body.WorkID != nil {
			prefix := fmt.Sprintf("abr-out/%s/%s/", ak.ID, in.Body.WorkID)
			n, err := deps.S3.DeletePrefix(ctx, prefix)
			if err != nil {
				return nil, huma.Error500InternalServerError("delete outputs failed", err)
			}
			out.Body.OutputObjectsDeleted = n
		}

		out.Body.OK = true
		return out, nil
	})
}

type ABRDeleteOut struct {
	Body struct {
		OK                   bool `json:"ok"`
		InputDeleted         bool `json:"input_deleted"`
		OutputObjectsDeleted int  `json:"output_objects_deleted"`
	}
}

// ── /v1/live ──────────────────────────────────────────────────────────

type LiveIn struct {
	Body struct {
		Name   string     `json:"name,omitempty" maxLength:"200"`
		Ladder *ABRLadder `json:"ladder,omitempty"`
	}
}

type LiveIngest struct {
	RTMPURL   string `json:"rtmp_url"`
	StreamKey string `json:"stream_key,omitempty" doc:"Returned exactly once on create."`
}

type LivePlayback struct {
	HLSURL string `json:"hls_url"`
}

type LiveSessionView struct {
	ID          uuid.UUID    `json:"id"`
	Status      string       `json:"status"`
	Ingest      LiveIngest   `json:"ingest"`
	Playback    LivePlayback `json:"playback"`
	CloseReason string       `json:"close_reason,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	StartedAt   *time.Time   `json:"started_at,omitempty"`
	EndedAt     *time.Time   `json:"ended_at,omitempty"`
}

type LiveCreateOut struct {
	Body struct {
		Session LiveSessionView `json:"session"`
	}
}

func registerV1Live(api huma.API, deps Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "v1-live-create",
		Method:      http.MethodPost,
		Path:        "/api/v1/live",
		Summary:     "Allocate an RTMP ingest + HLS egress session",
		Tags:        []string{"v1"},
	}, func(ctx context.Context, in *LiveIn) (*LiveCreateOut, error) {
		if deps.LOC == nil {
			return nil, huma.Error503ServiceUnavailable("loc_unavailable")
		}
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("invalid_api_key")
		}
		return openLiveGatewayIngest(ctx, deps, ak, in)
	})

	huma.Register(api, huma.Operation{
		OperationID: "v1-live-get",
		Method:      http.MethodGet,
		Path:        "/api/v1/live/{id}",
		Summary:     "Get the current state of a live session",
		Tags:        []string{"v1"},
	}, func(ctx context.Context, in *struct {
		ID uuid.UUID `path:"id"`
	}) (*LiveCreateOut, error) {
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("invalid_api_key")
		}
		live, err := deps.Live.GetByID(ctx, in.ID, ak.ID)
		if err != nil || live == nil {
			return nil, huma.Error404NotFound("not_found")
		}
		// On-GET reconcile against broker GET /v1/cap/{bsess}. Best
		// effort: failure here doesn't fail the customer request — the
		// background reconciler will catch the row on its next tick.
		// Only reconcile sessions that aren't already terminal.
		if live.BrokerSessionID != nil && live.BrokerURL != nil &&
			(live.Status == repo.LiveProvisioning || live.Status == repo.LiveActive) {
			if updated := reconcileLiveSession(ctx, deps, live); updated != nil {
				live = updated
			}
		}
		out := &LiveCreateOut{}
		out.Body.Session = LiveSessionView{
			ID:          live.ID,
			Status:      string(live.Status),
			Ingest:      LiveIngest{RTMPURL: derefString(live.IngestURL)}, // no plaintext stream key on GET
			Playback:    LivePlayback{HLSURL: derefString(live.PlaybackURL)},
			CloseReason: derefString(live.CloseReason),
			CreatedAt:   live.CreatedAt,
			StartedAt:   live.StartedAt,
			EndedAt:     live.EndedAt,
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "v1-live-delete",
		Method:      http.MethodDelete,
		Path:        "/api/v1/live/{id}",
		Summary:     "Close a live session",
		Tags:        []string{"v1"},
	}, func(ctx context.Context, in *struct {
		ID uuid.UUID `path:"id"`
	}) (*GenericOK, error) {
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("invalid_api_key")
		}
		live, err := deps.Live.GetByID(ctx, in.ID, ak.ID)
		if err != nil || live == nil {
			return nil, huma.Error404NotFound("not_found")
		}
		if live.Status == repo.LiveEnded || live.Status == repo.LiveFailed {
			// Idempotent from the customer's perspective.
			out := &GenericOK{}
			out.Body.OK = true
			return out, nil
		}
		// Best-effort broker teardown. The broker's /v1/cap/{bsess}/end
		// is idempotent server-side; we don't fail the customer if it
		// returns an error — we still mark the row ended locally.
		closeReason := livepeer.LiveCloseGatewayClose
		if live.BrokerSessionID != nil && live.BrokerURL != nil {
			if endResp, endErr := deps.HTTP.EndLiveSession(ctx,
				*live.BrokerURL, *live.BrokerSessionID, RequestIDFrom(ctx), closeReason); endErr != nil {
				deps.Log.Warn("live: broker end failed; marking ended locally anyway",
					"live_id", live.ID, "broker_session_id", *live.BrokerSessionID, "err", endErr)
			} else if endResp != nil && endResp.CloseReason != "" {
				closeReason = endResp.CloseReason
			}
		}
		_ = deps.Live.EndWithReason(ctx, live.ID, repo.LiveEnded, closeReason)
		// Settle the LOC session (duration-estimate units; idempotent
		// via ClaimLOCClose — the reconciler may have beaten us here).
		closeLiveLOCSession(ctx, deps, live, "gateway_close")
		// Synchronously tear down the customer's RTMP socket + our
		// upstream relay push. Without this, OBS would happily keep
		// pushing bytes to a now-dead broker session until TCP
		// keepalive notices (~30-60s later). Idempotent if no relay
		// is currently active.
		if deps.RTMPProbe != nil {
			if closed := deps.RTMPProbe.CloseSession(live.ID.String()); closed {
				deps.Log.Info("live: rtmp relay torn down",
					"live_id", live.ID, "close_reason", closeReason)
			}
		}
		// Per the runner team's call (E): no refund on customer DELETE
		// of an accepted session; the reservation was already committed
		// at session-open time. We just decrement the active gauge.
		deps.Metrics.LiveStreamsActive.Dec()
		out := &GenericOK{}
		out.Body.OK = true
		return out, nil
	})
}

// reconcileLiveSession polls the broker for the latest state of one
// session and writes the result back to live_streams. Used by both the
// on-GET path (above) and the background reconciler (Phase 6). Returns
// the freshly-loaded row when a write happened; nil if reconciliation
// produced no actionable change.
func reconcileLiveSession(ctx context.Context, deps Deps, live *repo.LiveStream) *repo.LiveStream {
	if live == nil || live.BrokerSessionID == nil || live.BrokerURL == nil {
		return nil
	}
	resp, err := deps.HTTP.GetLiveSession(ctx, *live.BrokerURL, *live.BrokerSessionID)
	if err != nil {
		deps.Log.Debug("live reconcile: broker GET failed",
			"live_id", live.ID, "broker_session_id", *live.BrokerSessionID, "err", err)
		return nil
	}
	mappedStatus, endedAt := mapBrokerState(resp.State)
	if mappedStatus == "" {
		return nil
	}
	snap := repo.ReconcileSnapshot{
		Status:      mappedStatus,
		PlaybackURL: resp.Media.Playback.HLSURL,
	}
	if resp.CloseReason != nil {
		snap.CloseReason = *resp.CloseReason
	}
	if resp.LastHeartbeatAt != nil {
		if t, perr := time.Parse(time.RFC3339, *resp.LastHeartbeatAt); perr == nil {
			snap.Heartbeat = &t
		}
	}
	if endedAt && resp.EndedAt != nil {
		if t, perr := time.Parse(time.RFC3339, *resp.EndedAt); perr == nil {
			snap.EndedAt = &t
		}
	}
	// Persist the runner's status-hardening surface (ingest + output)
	// when present so the admin UI can render it without polling the
	// broker per page load. Both blocks are optional from the broker.
	if resp.Ingest != nil || resp.Output != nil {
		runnerStatus := map[string]any{}
		if resp.Ingest != nil {
			runnerStatus["ingest"] = resp.Ingest
		}
		if resp.Output != nil {
			runnerStatus["output"] = resp.Output
		}
		if b, mErr := json.Marshal(runnerStatus); mErr == nil {
			snap.RunnerStatusJSON = b
		}
	}
	if err := deps.Live.RecordBrokerSync(ctx, live.ID, snap); err != nil {
		deps.Log.Warn("live reconcile: persist failed", "live_id", live.ID, "err", err)
		return nil
	}
	// Re-load so callers see the freshly-written state.
	reloaded, _ := deps.Live.GetByID(ctx, live.ID, live.APIKeyID)
	// Broker drove the session terminal (balance exhausted, runner
	// crash, idle timeout, …) — settle the LOC session too. ClaimLOCClose
	// makes this race-safe against the customer DELETE path.
	if reloaded != nil && (reloaded.Status == repo.LiveEnded || reloaded.Status == repo.LiveFailed) {
		closeLiveLOCSession(ctx, deps, reloaded, "broker_ended")
	}
	return reloaded
}

// mapBrokerState turns the broker's state vocabulary into the gateway's
// existing LiveStreamStatus enum per the runner team's mapping. Returns
// "" when the broker state isn't recognized — we leave the row alone
// rather than guess.
func mapBrokerState(brokerState string) (repo.LiveStreamStatus, bool) {
	switch brokerState {
	case livepeer.LiveStateProvisioning,
		livepeer.LiveStateReady:
		return repo.LiveProvisioning, false
	case livepeer.LiveStatePublishing,
		livepeer.LiveStateEnding:
		return repo.LiveActive, false
	case livepeer.LiveStateEnded:
		return repo.LiveEnded, true
	case livepeer.LiveStateFailed:
		return repo.LiveFailed, true
	}
	return "", false
}

// ladderFromInput converts the customer-facing ABRLadder (used for both
// ABR and live by historical reuse) into the live broker wire shape
// (the LiveLadder / LiveLadderRung types in proxy/livepeer/live_session.go,
// which both legacy live-session-remote-runner@v0 and current
// live-session-gateway-ingest@v0 share). Returns nil for empty /
// unspecified ladders so the broker falls back to the capability default.
func ladderFromInput(in *ABRLadder) *livepeer.LiveLadder {
	if in == nil || len(in.Rungs) == 0 {
		return nil
	}
	rungs := make([]livepeer.LiveLadderRung, 0, len(in.Rungs))
	for _, r := range in.Rungs {
		rungs = append(rungs, livepeer.LiveLadderRung{
			Name:        r.Name,
			Width:       r.Width,
			Height:      r.Height,
			BitrateKbps: r.BitrateKbps,
			Passthrough: r.Passthrough,
		})
	}
	return &livepeer.LiveLadder{Rungs: rungs}
}

// openLiveGatewayIngest implements the plan-0003 path:
//   1. resolve a live-capable orch
//   2. mint payment
//   3. open local row in 'gateway_ingest' mode
//   4. generate customer stream key + gateway↔runner ingest credential
//   5. mint S3 output credential scoped to the session's prefix
//   6. call broker /v1/cap with the new mode body
//   7. activate the row with broker-returned IDs + persist all artifacts
//   8. return customer-facing URLs (gateway RTMP + our S3 HLS)
func openLiveGatewayIngest(ctx context.Context, deps Deps, ak *repo.APIKey, in *LiveIn) (*LiveCreateOut, error) {
	if deps.S3 == nil {
		return nil, huma.Error503ServiceUnavailable("s3_unavailable")
	}
	if deps.Cfg.LiveRTMPPort <= 0 {
		return nil, huma.Error503ServiceUnavailable("rtmp_ingress_disabled",
			fmt.Errorf("set LIVE_RTMP_PORT to enable gateway_ingest mode"))
	}
	// `video:transcode.live` with the gateway-ingest offering label —
	// LOC resolves the route when we open the session below.
	gwCapability := deps.CapMap.Live.Capability
	gwOffering := deps.Cfg.LiveGatewayIngestOffering
	if gwOffering == "" {
		gwOffering = "gateway-ingest"
	}

	const liveInitialEstUnits int64 = 60_000

	// Open reservation row + live row in 'gateway_ingest' mode. The
	// row's capability records the gateway-ingest id (NOT the legacy
	// LIVE_CAPABILITY) so admin views can group cleanly by mode.
	ladderJSON, _ := json.Marshal(in.Body.Ladder)
	workID := uuid.New()
	res, err := deps.Usage.Open(ctx, repo.OpenInput{
		APIKeyID:   ak.ID,
		WorkID:     workID,
		Capability: gwCapability,
		Offering:   gwOffering,
	})
	if err != nil {
		return nil, huma.Error500InternalServerError("reservation open", err)
	}
	live, err := deps.Live.Insert(ctx, repo.InsertLiveInput{
		APIKeyID:      ak.ID,
		ReservationID: &res.ID,
		Name:          in.Body.Name,
		Capability:    gwCapability,
		Offering:      gwOffering,
		LadderJSON:    ladderJSON,
	})
	if err != nil {
		_ = deps.Usage.Refund(ctx, res.ID, 500, "live_insert_failed")
		return nil, huma.Error500InternalServerError("live insert", err)
	}
	_ = deps.Usage.SetLiveStreamID(ctx, workID, live.ID)

	// Two distinct credentials minted here:
	//   - customerStreamKey: what we hand the customer for OBS
	//   - ingestAcceptKey:   what we'll present when pushing RTMP upstream
	customerStreamKey, err := crypto.RandomToken(24)
	if err != nil {
		_ = deps.Usage.Refund(ctx, res.ID, 500, "stream_key_gen_failed")
		_ = deps.Live.Fail(ctx, live.ID, "stream_key_gen_failed")
		return nil, huma.Error500InternalServerError("stream_key gen", err)
	}
	customerStreamKey = "lvk_" + customerStreamKey
	ingestAcceptKey, _ := crypto.RandomToken(24)
	ingestAcceptKey = "gws_" + ingestAcceptKey

	// S3 output credentials scoped to live-out/<api_key>/<live_id>/.
	keyPrefix := fmt.Sprintf("live-out/%s/%s", ak.ID, live.ID)
	credTTL := time.Duration(deps.Cfg.LiveS3CredentialTTLHrs) * time.Hour
	if credTTL <= 0 {
		credTTL = 4 * time.Hour
	}
	creds, err := deps.S3.MintLiveSessionCredentials(ctx, keyPrefix, credTTL)
	if err != nil {
		_ = deps.Usage.Refund(ctx, res.ID, 500, "s3_creds_failed")
		_ = deps.Live.Fail(ctx, live.ID, "s3_creds_failed")
		return nil, huma.Error500InternalServerError("s3 credential mint", err)
	}

	// Call broker /v1/cap with the new mode body.
	openBody := livepeer.LiveOpenGatewayIngestRequest{
		GatewaySessionID: live.ID,
		SessionParams: livepeer.LiveOpenParams{
			Name:               in.Body.Name,
			IdleTimeoutSeconds: deps.Cfg.LiveIdleTimeoutSecs,
			Ladder:             ladderFromInput(in.Body.Ladder),
		},
		OutputCredential: livepeer.LiveOutputCredential{
			Endpoint:        creds.Endpoint,
			Region:          creds.Region,
			Bucket:          creds.Bucket,
			KeyPrefix:       creds.KeyPrefix,
			AccessKeyID:     creds.AccessKeyID,
			SecretAccessKey: creds.SecretAccessKey,
			SessionToken:    creds.SessionToken,
			ExpiresAt:       creds.ExpiresAt.UTC().Format(time.RFC3339),
		},
		IngestAccept: livepeer.LiveIngestAccept{StreamKey: ingestAcceptKey},
	}

	// LOC session open + broker dispatch, with rotation retry-once. LOC
	// encumbers toward max_total_units at open, so every failed attempt
	// MUST close(0) to release the credit. On INVALID_RECIPIENT_RAND
	// (receiver rotated its payment session) the recovery is close(0) +
	// a fresh OpenSession, which mints a fresh envelope.
	var (
		locSess *loc.CreateSessionResponse
		sess    *livepeer.LiveOpenGatewayIngestResponse
	)
	for attempt := 1; ; attempt++ {
		locSess, err = deps.LOC.OpenSession(ctx, loc.CreateSessionRequest{
			Capability:           gwCapability,
			Offering:             gwOffering,
			EstimatedRunwayUnits: liveInitialEstUnits,
			MaxTotalUnits:        deps.Cfg.LiveMaxTotalUnits,
		})
		if err != nil {
			_ = deps.Live.Fail(ctx, live.ID, "loc_open_session_failed")
			_ = deps.Usage.Refund(ctx, res.ID, 502, err.Error())
			switch {
			case loc.IsNoRoute(err):
				return nil, huma.Error502BadGateway("no_capable_broker", err)
			case loc.IsInsufficientCredit(err):
				return nil, huma.NewError(http.StatusPaymentRequired, "insufficient_credit", err)
			default:
				return nil, huma.Error502BadGateway("loc_open_session_failed", err)
			}
		}
		var payment []byte
		if payment, err = locSess.PaymentBytes(); err == nil {
			sess, err = deps.HTTP.OpenLiveSessionGatewayIngest(ctx, locSess.BrokerURL,
				gwCapability, gwOffering, RequestIDFrom(ctx), payment, openBody)
		}
		if err == nil {
			break
		}
		// Release this attempt's encumbrance.
		if _, cerr := deps.LOC.CloseSession(ctx, locSess.SessionID, loc.CloseSessionRequest{
			ActualUnits: 0, Outcome: "broker_open_failed",
		}); cerr != nil && !loc.IsAlreadySettled(cerr) {
			deps.Log.Warn("live: loc close after failed open also failed; session may stay encumbered",
				"loc_session_id", locSess.SessionID, "err", cerr)
		}
		if livepeer.IsInvalidRecipientRandError(err) && attempt < 2 {
			deps.Metrics.SessionRotationRetries.WithLabelValues(gwCapability, "retried").Inc()
			deps.Log.Warn("live: session rotation on open; re-opening LOC session",
				"live_id", live.ID, "broker", locSess.BrokerURL, "err", err)
			continue
		}
		break
	}
	if err != nil {
		_ = deps.Live.Fail(ctx, live.ID, err.Error())
		_ = deps.Usage.Refund(ctx, res.ID, 502, err.Error())
		return nil, huma.Error502BadGateway("broker_open_session_failed", err)
	}

	// Activate row with the broker-issued IDs + our generated credentials.
	streamKeyHash := crypto.HashWithPepper(customerStreamKey, deps.Cfg.IPHashPepper)
	streamKeyHint := lastFour(customerStreamKey)
	playbackURL := deps.S3.PublicHLSMasterURL(creds.KeyPrefix)
	brokerWorkID := sess.WorkID
	locSessionID := locSess.SessionID
	if err := deps.Live.ActivateGatewayIngest(ctx, live.ID, repo.ActivateLiveGatewayInput{
		BrokerURL: locSess.BrokerURL,
		// LOC owns the recipient identity now — no eth_address surfaced.
		StreamKeyHash:    streamKeyHash,
		StreamKeyHint:    streamKeyHint,
		S3OutputPrefix:   creds.KeyPrefix,
		PrivateIngestURL: sess.PrivateIngestURL,
		PlaybackURL:      playbackURL,
		BrokerSessionID:  sess.BrokerSessionID,
		RunnerSessionID:  sess.RunnerSessionID,
		BrokerWorkID:     &brokerWorkID,
		LOCSessionID:     &locSessionID,
		LOCWorkID:        locSess.WorkID,
	}); err != nil {
		_ = deps.Live.Fail(ctx, live.ID, err.Error())
		_ = deps.Usage.Refund(ctx, res.ID, 500, "activate_failed")
		return nil, huma.Error500InternalServerError("activate failed", err)
	}
	// Reservation commits on accept; no refund on later customer DELETE
	// (matches plan 0002 semantics). The LOC-side accounting reconciles
	// at CloseSession (closeLiveLOCSession).
	statusCode := 200
	_ = deps.Usage.Commit(ctx, res.ID, repo.CommitInput{
		BrokerURL:  locSess.BrokerURL,
		StatusCode: &statusCode,
	})
	deps.Metrics.ProxyReservationsTotal.WithLabelValues(gwCapability, "committed").Inc()
	deps.Metrics.LiveStreamsActive.Inc()

	// Construct the customer-facing RTMP URL rooted at OUR gateway. The
	// stream key is returned in cleartext exactly once.
	gatewayHost := deps.Cfg.GatewayPublicURL
	if gatewayHost == "" {
		// Fall back to a relative host hint; customers can substitute.
		gatewayHost = "rtmp://<your-gateway-host>"
	} else {
		// Strip scheme if it's http(s) and substitute rtmp.
		gatewayHost = stripScheme(gatewayHost)
		gatewayHost = "rtmp://" + gatewayHost
	}
	rtmpURL := fmt.Sprintf("%s:%d/live/%s", gatewayHost, deps.Cfg.LiveRTMPPort, customerStreamKey)

	out := &LiveCreateOut{}
	out.Body.Session = LiveSessionView{
		ID:        live.ID,
		Status:    string(repo.LiveActive),
		Ingest:    LiveIngest{RTMPURL: rtmpURL, StreamKey: customerStreamKey},
		Playback:  LivePlayback{HLSURL: playbackURL},
		CreatedAt: live.CreatedAt,
	}
	return out, nil
}

// lastFour mirrors the helper in internal/rtmp — duplicated locally to
// avoid importing the rtmp package (which would create a cycle with the
// server package via Deps).
func lastFour(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[len(s)-4:]
}

// stripScheme returns the host[:port] portion of a URL, removing
// http:// or https:// prefixes. Used so we can substitute rtmp:// for
// the customer-facing ingest URL.
func stripScheme(s string) string {
	for _, p := range []string{"https://", "http://"} {
		if strings.HasPrefix(s, p) {
			return s[len(p):]
		}
	}
	return s
}

// settleLOCJob settles a LOC job and records the outcome on the
// reservation. actualUnits=0 → settle state 'refunded' (failed
// dispatch, full encumbrance release); >0 → 'settled'. loc.SettleJob
// already retries 429/5xx; a residual failure leaves the row 'pending'
// for the settle janitor, and a duplicate settle (409) counts as
// success — the first one stuck.
func settleLOCJob(ctx context.Context, deps Deps, reservationID, jobID uuid.UUID, actualUnits int64, outcome string) {
	state := repo.SettleSettled
	if actualUnits == 0 {
		state = repo.SettleRefunded
	}
	resp, err := deps.LOC.SettleJob(ctx, jobID, loc.SettleJobRequest{
		ActualUnits: actualUnits,
		Outcome:     outcome,
	})
	switch {
	case err == nil:
		_ = deps.Usage.MarkSettled(ctx, reservationID, resp.BilledValueWei.BigInt(), state)
	case loc.IsAlreadySettled(err):
		_ = deps.Usage.MarkSettled(ctx, reservationID, nil, state)
	default:
		deps.Log.Warn("loc: settle failed; settle janitor will retry",
			"loc_job_id", jobID, "actual_units", actualUnits, "outcome", outcome, "err", err)
	}
}

// abrOutputs is the shape the abr-runner consumes for output_urls.
type abrOutputs struct {
	Manifest   string                       `json:"manifest"`
	Renditions map[string]abrRenditionOutput `json:"renditions"`
}

type abrRenditionOutput struct {
	Playlist string `json:"playlist"`
	Stream   string `json:"stream"`
}

// mintABROutputs presigns the destination URLs the runner needs to
// upload (manifest + per-rendition playlist + per-rendition stream)
// and returns the playback URL for the master playlist as the second
// value so the gateway response can echo it to the client.
func mintABROutputs(ctx context.Context, deps Deps, apiKeyID, workID string, preset abr.Preset) (abrOutputs, string, error) {
	prefix := path.Join("abr-out", apiKeyID, workID)
	manifestKey := path.Join(prefix, "master.m3u8")
	mp, err := deps.S3.PresignPut(ctx, manifestKey, "application/vnd.apple.mpegurl")
	if err != nil {
		return abrOutputs{}, "", fmt.Errorf("presign manifest: %w", err)
	}
	out := abrOutputs{
		Manifest:   mp.UploadURL,
		Renditions: make(map[string]abrRenditionOutput, len(preset.Renditions)),
	}
	for _, r := range preset.Renditions {
		// The runner writes master.m3u8 with relative refs like
		// "<name>/playlist.m3u8" — without any "renditions/" segment. So
		// we presign the variants at "<prefix>/<name>/..." to match what
		// the browser will resolve when it hits each #EXT-X-STREAM-INF.
		playKey := path.Join(prefix, r.Name, "playlist.m3u8")
		streamKey := path.Join(prefix, r.Name, "stream.mp4")
		pp, err := deps.S3.PresignPut(ctx, playKey, "application/vnd.apple.mpegurl")
		if err != nil {
			return abrOutputs{}, "", fmt.Errorf("presign rendition %s playlist: %w", r.Name, err)
		}
		sp, err := deps.S3.PresignPut(ctx, streamKey, "video/mp4")
		if err != nil {
			return abrOutputs{}, "", fmt.Errorf("presign rendition %s stream: %w", r.Name, err)
		}
		out.Renditions[r.Name] = abrRenditionOutput{
			Playlist: pp.UploadURL,
			Stream:   sp.UploadURL,
		}
	}
	return out, deps.S3.PublicObjectURL(manifestKey), nil
}

func bigStr(b *big.Int) string {
	if b == nil {
		return ""
	}
	return b.String()
}
