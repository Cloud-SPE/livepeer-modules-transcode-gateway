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
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/service"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"
	paymentsv1 "github.com/Cloud-SPE/livepeer-network-modules/livepeer-network-protocol/proto-go/livepeer/payments/v1"

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
		Path:        "/v1/capabilities",
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
		Path:        "/v1/abr/upload-url",
		Summary:     "Get a presigned RustFS PUT URL for VOD ingest",
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
		Path:        "/v1/abr",
		Summary:     "Submit an ABR ladder transcode job",
		Tags:        []string{"v1"},
	}, func(ctx context.Context, in *ABRIn) (*ABROut, error) {
		if deps.Resolver == nil {
			return nil, huma.Error503ServiceUnavailable("registry_unavailable")
		}
		if deps.Payer == nil {
			return nil, huma.Error503ServiceUnavailable("payer_unavailable")
		}
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("invalid_api_key")
		}
		spec := deps.CapMap.ABR
		candidates, err := deps.Resolver.SelectMany(ctx, service.SelectRequest{
			Capability: spec.Capability,
			Offering:   spec.DefaultOffering,
		})
		if err != nil {
			return nil, huma.Error502BadGateway("registry_select_failed", err)
		}
		workID := uuid.New()
		estUnits := int64(0)
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

		// abr-runner's submit response shape: { job_id, status, renditions:[name,...] }.
		// The runner returns rendition NAMES (strings); we map them to the
		// playback URLs the gateway minted so the client gets ready-to-play
		// rendition rows in the response without a second round-trip.
		type brokerJob struct {
			JobID      string   `json:"job_id"`
			Status     string   `json:"status"`
			Renditions []string `json:"renditions,omitempty"`
		}
		started := time.Now()
		var masterPlaybackURL string // captured from the Do closure
		result, used, err := service.Dispatch(ctx, candidates, deps.Health, service.Attempt{
			MintPayment: func(ctx context.Context, c service.Candidate) ([]byte, string, error) {
				face := faceValue(estUnits, c.PricePerWorkUnitWei)
				envelope, err := deps.Payer.MintEnvelope(ctx, livepeer.MintRequest{
					RecipientEthAddrHex:   c.EthAddress,
					BrokerURL:             c.WorkerURL,
					Capability:            c.Capability,
					Offering:              c.Offering,
					PricePerUnitWei:       c.PricePerWorkUnitWei,
					UnitsPerPrice:         c.UnitsPerPrice,
					WorkUnitName:          c.WorkUnit,
					QuoteID:               c.QuoteID,
					QuoteVersion:          c.QuoteVersion,
					ConstraintFingerprint: c.ConstraintFingerprint,
					RouteFingerprint:      c.RouteFingerprint,
					EstimatedUnits:        uint64(maxInt64(estUnits, 1)),
					FundedValueWei:        face,
					MaxTotalUnits:         uint64(maxInt64(estUnits, 1)),
					TopUpAllowed:          false,
				})
				if err != nil {
					return nil, "", err
				}
				return envelope.PaymentBytes, envelope.WorkID, nil
			},
			ReportRotation: func(ctx context.Context, c service.Candidate, workID string) error {
				return deps.Payer.ReportPaymentResult(ctx, workID, c.Capability, c.Offering,
					paymentsv1.PaymentRejectionReason_PAYMENT_REJECTION_REASON_INVALID_RECIPIENT_RAND)
			},
			OnRotation: func(ctx context.Context, c service.Candidate, workID, outcome string, rotErr error) {
				deps.Metrics.SessionRotationRetries.WithLabelValues(c.Capability, outcome).Inc()
				level := "warn"
				if outcome == service.RotationOutcomeSucceeded {
					level = "info"
				}
				attrs := []any{
					"work_id", workID,
					"capability", c.Capability,
					"offering", c.Offering,
					"broker", c.WorkerURL,
					"outcome", outcome,
				}
				if rotErr != nil {
					attrs = append(attrs, "err", rotErr.Error())
				}
				if level == "info" {
					deps.Log.Info("dispatcher: session-rotation retry succeeded", attrs...)
				} else {
					deps.Log.Warn("dispatcher: session-rotation retry", attrs...)
				}
			},
			Do: func(ctx context.Context, c service.Candidate, payment []byte) (any, error) {
				presetName := in.Body.Preset
				if presetName == "" {
					presetName = "abr-standard"
				}
				preset, ok := abr.Get(presetName)
				if !ok {
					return nil, fmt.Errorf("unknown preset %q (known: %v)", presetName, abr.Names())
				}
				if deps.S3 == nil {
					return nil, fmt.Errorf("output_urls require S3 — RustFS not configured")
				}
				outputs, masterURL, err := mintABROutputs(ctx, deps, ak.ID.String(), workID.String(), preset)
				if err != nil {
					return nil, fmt.Errorf("mint output_urls: %w", err)
				}
				masterPlaybackURL = masterURL
				body := map[string]any{
					"input_url":   in.Body.InputURL,
					"preset":      presetName,
					"output_urls": outputs,
				}
				if in.Body.Ladder != nil {
					body["ladder"] = in.Body.Ladder
				}
				// Webhook hookup — runner POSTs status transitions back
				// when both webhook_url + webhook_secret are set.
				// Unset → gateway stays oblivious (same as before).
				if deps.Cfg.GatewayPublicURL != "" {
					secret, _ := crypto.RandomToken(32)
					if err := deps.Usage.SetWebhookSecret(ctx, workID, secret); err == nil {
						body["webhook_url"] = strings.TrimRight(deps.Cfg.GatewayPublicURL, "/") +
							"/api/abr/callback?work_id=" + workID.String()
						body["webhook_secret"] = secret
					}
				}
				var j brokerJob
				dispatchURL := strings.TrimRight(c.WorkerURL, "/") + "/v1/cap"
				if err := deps.HTTP.PostJSON(ctx, dispatchURL,
					c.Capability, c.Offering, RequestIDFrom(ctx), payment, body, &j); err != nil {
					return nil, err
				}
				return j, nil
			},
		})
		latency := int(time.Since(started).Milliseconds())
		if err != nil {
			_ = deps.Usage.Refund(ctx, res.ID, 502, err.Error())
			deps.Metrics.ProxyReservationsTotal.WithLabelValues(spec.Capability, "refunded").Inc()
			return nil, huma.Error502BadGateway("upstream_broker_error", err)
		}
		j := result.(brokerJob)
		statusCode := 200
		_ = deps.Usage.Commit(ctx, res.ID, repo.CommitInput{
			BrokerURL:  used.WorkerURL,
			EthAddress: used.EthAddress,
			LatencyMs:  &latency,
			StatusCode: &statusCode,
		})
		// Persist the runner-assigned job id so GET /v1/abr/{work_id}
		// can ask the runner for live status.
		if j.JobID != "" {
			_ = deps.Usage.SetRunnerJobID(ctx, workID, j.JobID)
		}
		deps.Metrics.ProxyReservationsTotal.WithLabelValues(spec.Capability, "committed").Inc()
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
			BrokerURL:         used.WorkerURL,
			EthAddress:        used.EthAddress,
			CreatedAt:         res.CreatedAt,
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "v1-abr-get",
		Method:      http.MethodGet,
		Path:        "/v1/abr/{id}",
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
		Path:        "/v1/abr/objects",
		Summary:     "Delete a VOD upload and its transcode outputs from RustFS",
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
	ID         uuid.UUID    `json:"id"`
	Status     string       `json:"status"`
	Ingest     LiveIngest   `json:"ingest"`
	Playback   LivePlayback `json:"playback"`
	CreatedAt  time.Time    `json:"created_at"`
	StartedAt  *time.Time   `json:"started_at,omitempty"`
	EndedAt    *time.Time   `json:"ended_at,omitempty"`
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
		Path:        "/v1/live",
		Summary:     "Allocate an RTMP ingest + HLS egress session",
		Tags:        []string{"v1"},
	}, func(ctx context.Context, in *LiveIn) (*LiveCreateOut, error) {
		if deps.Resolver == nil {
			return nil, huma.Error503ServiceUnavailable("registry_unavailable")
		}
		if deps.Payer == nil {
			return nil, huma.Error503ServiceUnavailable("payer_unavailable")
		}
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("invalid_api_key")
		}
		spec := deps.CapMap.Live
		candidates, err := deps.Resolver.SelectMany(ctx, service.SelectRequest{
			Capability: spec.Capability,
			Offering:   spec.DefaultOffering,
		})
		if err != nil {
			return nil, huma.Error502BadGateway("registry_select_failed", err)
		}
		if len(candidates) == 0 {
			return nil, huma.Error502BadGateway("no_capable_broker")
		}
		// No failover on live session-open — pick top candidate only.
		c := candidates[0]
		const liveInitialEstUnits int64 = 60_000 // ~1m initial budget; refined by broker
		mintLive := func() (livepeer.MintEnvelopeResult, error) {
			face := faceValue(liveInitialEstUnits, c.PricePerWorkUnitWei)
			return deps.Payer.MintEnvelope(ctx, livepeer.MintRequest{
				RecipientEthAddrHex:   c.EthAddress,
				BrokerURL:             c.WorkerURL,
				Capability:            c.Capability,
				Offering:              c.Offering,
				PricePerUnitWei:       c.PricePerWorkUnitWei,
				UnitsPerPrice:         c.UnitsPerPrice,
				WorkUnitName:          c.WorkUnit,
				QuoteID:               c.QuoteID,
				QuoteVersion:          c.QuoteVersion,
				ConstraintFingerprint: c.ConstraintFingerprint,
				RouteFingerprint:      c.RouteFingerprint,
				EstimatedUnits:        uint64(liveInitialEstUnits),
				FundedValueWei:        face,
				MaxTotalUnits:         0, // 0 → defaults to estimated; live sessions can top-up
				TopUpAllowed:          true,
			})
		}
		envelope, err := mintLive()
		if err != nil {
			return nil, huma.Error502BadGateway("mint_payment_failed", err)
		}
		payment := envelope.PaymentBytes
		ladderJSON, _ := json.Marshal(in.Body.Ladder)
		// Open a long-lived reservation row.
		workID := uuid.New()
		res, err := deps.Usage.Open(ctx, repo.OpenInput{
			APIKeyID:   ak.ID,
			WorkID:     workID,
			Capability: spec.Capability,
			Offering:   spec.DefaultOffering,
		})
		if err != nil {
			return nil, huma.Error500InternalServerError("reservation open", err)
		}
		live, err := deps.Live.Insert(ctx, repo.InsertLiveInput{
			APIKeyID:      ak.ID,
			ReservationID: &res.ID,
			Name:          in.Body.Name,
			Capability:    spec.Capability,
			Offering:      spec.DefaultOffering,
			LadderJSON:    ladderJSON,
		})
		if err != nil {
			_ = deps.Usage.Refund(ctx, res.ID, 500, "live_insert_failed")
			return nil, huma.Error500InternalServerError("live insert", err)
		}
		sess, err := livepeer.OpenLiveSession(ctx, deps.HTTP, c.WorkerURL,
			c.Capability, c.Offering, RequestIDFrom(ctx), payment, ladderJSON)
		if err != nil && livepeer.IsInvalidRecipientRandError(err) && envelope.WorkID != "" {
			// Session-rotation retry-once on the live-open path. The
			// receiver rotated since we last minted; evict our cached
			// session and re-mint. Outcomes mirror the dispatcher's
			// rotation label set so the same Prometheus counter covers
			// both paths.
			oldWID := envelope.WorkID
			outcome := service.RotationOutcomeRetryFailed
			if rerr := deps.Payer.ReportPaymentResult(ctx, oldWID, c.Capability, c.Offering,
				paymentsv1.PaymentRejectionReason_PAYMENT_REJECTION_REASON_INVALID_RECIPIENT_RAND); rerr != nil {
				outcome = service.RotationOutcomeReportFailed
				deps.Log.Warn("live: session-rotation retry",
					"work_id", oldWID, "capability", c.Capability, "outcome", outcome, "err", rerr)
			} else if re, mintErr := mintLive(); mintErr != nil {
				outcome = service.RotationOutcomeMintFailed
				deps.Log.Warn("live: session-rotation retry",
					"work_id", oldWID, "capability", c.Capability, "outcome", outcome, "err", mintErr)
			} else {
				envelope = re
				payment = re.PaymentBytes
				sess, err = livepeer.OpenLiveSession(ctx, deps.HTTP, c.WorkerURL,
					c.Capability, c.Offering, RequestIDFrom(ctx), payment, ladderJSON)
				if err == nil {
					outcome = service.RotationOutcomeSucceeded
					deps.Log.Info("live: session-rotation retry succeeded",
						"work_id", oldWID, "capability", c.Capability)
				} else {
					deps.Log.Warn("live: session-rotation retry",
						"work_id", oldWID, "capability", c.Capability, "outcome", outcome, "err", err)
				}
			}
			deps.Metrics.SessionRotationRetries.WithLabelValues(c.Capability, outcome).Inc()
		}
		if err != nil {
			_ = deps.Live.Fail(ctx, live.ID, err.Error())
			_ = deps.Usage.Refund(ctx, res.ID, 502, err.Error())
			return nil, huma.Error502BadGateway("broker_open_session_failed", err)
		}
		streamKeyHash := crypto.HashWithPepper(sess.StreamKey, deps.Cfg.IPHashPepper)
		if err := deps.Live.Activate(ctx, live.ID, repo.ActivateLiveInput{
			BrokerURL:     c.WorkerURL,
			EthAddress:    c.EthAddress,
			IngestURL:     sess.RTMPURL,
			StreamKeyHash: streamKeyHash,
			PlaybackURL:   sess.HLSURL,
		}); err != nil {
			_ = deps.Live.Fail(ctx, live.ID, err.Error())
			_ = deps.Usage.Refund(ctx, res.ID, 500, "activate_failed")
			return nil, huma.Error500InternalServerError("activate failed", err)
		}
		deps.Metrics.LiveStreamsActive.Inc()
		out := &LiveCreateOut{}
		out.Body.Session = LiveSessionView{
			ID:        live.ID,
			Status:    string(repo.LiveActive),
			Ingest:    LiveIngest{RTMPURL: sess.RTMPURL, StreamKey: sess.StreamKey},
			Playback:  LivePlayback{HLSURL: sess.HLSURL},
			CreatedAt: live.CreatedAt,
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "v1-live-get",
		Method:      http.MethodGet,
		Path:        "/v1/live/{id}",
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
		out := &LiveCreateOut{}
		out.Body.Session = LiveSessionView{
			ID:        live.ID,
			Status:    string(live.Status),
			Ingest:    LiveIngest{RTMPURL: derefString(live.IngestURL)},
			Playback:  LivePlayback{HLSURL: derefString(live.PlaybackURL)},
			CreatedAt: live.CreatedAt,
			StartedAt: live.StartedAt,
			EndedAt:   live.EndedAt,
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "v1-live-delete",
		Method:      http.MethodDelete,
		Path:        "/v1/live/{id}",
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
			return nil, huma.Error409Conflict("live_already_ended")
		}
		// Best-effort broker teardown.
		if live.BrokerURL != nil {
			_ = livepeer.CloseLiveSession(ctx, deps.HTTP, *live.BrokerURL, live.ID.String())
		}
		_ = deps.Live.End(ctx, live.ID)
		if live.ReservationID != nil {
			_ = deps.Usage.Commit(ctx, *live.ReservationID, repo.CommitInput{
				BrokerURL:  derefString(live.BrokerURL),
				EthAddress: derefString(live.EthAddress),
			})
		}
		deps.Metrics.LiveStreamsActive.Dec()
		deps.Metrics.ProxyReservationsTotal.WithLabelValues(live.Capability, "committed").Inc()
		out := &GenericOK{}
		out.Body.OK = true
		return out, nil
	})
}

// faceValue derives a wei face value from estimated units × price/unit.
// When price is nil we default to 1 wei × units (smoke-test friendly).
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

func faceValue(units int64, pricePerUnit *big.Int) *big.Int {
	if units <= 0 {
		units = 1
	}
	if pricePerUnit == nil || pricePerUnit.Sign() <= 0 {
		return big.NewInt(units)
	}
	out := new(big.Int).Mul(pricePerUnit, big.NewInt(units))
	if out.Sign() <= 0 {
		out.SetInt64(1)
	}
	return out
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func bigStr(b *big.Int) string {
	if b == nil {
		return ""
	}
	return b.String()
}
