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
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/loc"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

// paymentRequiredError turns a LOC credit refusal into a 402 carrying a clear,
// actionable detail message for the client. The stable machine code
// (insufficient_credit / spend_cap_exceeded / session_cap_reached) is preserved
// in the wrapped error chain for clients that branch on it.
func paymentRequiredError(err error) error {
	code, msg := loc.CreditError(err)
	return huma.NewError(http.StatusPaymentRequired, msg, fmt.Errorf("%s: %w", code, err))
}

func RegisterV1(api huma.API, deps Deps) {
	registerV1Capabilities(api, deps)
	registerV1Upload(api, deps)
	registerV1ABR(api, deps)
	registerV1Live(api, deps)
}

// ── /v1/capabilities ──────────────────────────────────────────────────

type CapabilitiesOut struct {
	Body struct {
		Object     string             `json:"object"`
		Data       []CapabilityListed `json:"data"`
		SnapshotAt time.Time          `json:"snapshot_at"`
	}
}

type CapabilityListed struct {
	Protocol          string          `json:"protocol"`
	WorkUnit          string          `json:"work_unit,omitempty"`
	UnitsPerPrice     string          `json:"units_per_price,omitempty"`
	WorkUnitEstimator json.RawMessage `json:"work_unit_estimator,omitempty"`
	Job               json.RawMessage `json:"job,omitempty"`
	Session           json.RawMessage `json:"session,omitempty"`
	ID                string          `json:"id"`
	Capability        string          `json:"capability"`
	Offering          string          `json:"offering"`
	InteractionMode   string          `json:"interaction_mode,omitempty"`
	Name              string          `json:"name,omitempty"`
	Category          string          `json:"category,omitempty"`
	PriceWei          string          `json:"price_per_work_unit_wei,omitempty"`
	Extra             json.RawMessage `json:"extra,omitempty"`
	Constraints       json.RawMessage `json:"constraints,omitempty"`
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
				Protocol:          c.Protocol,
				WorkUnit:          c.WorkUnit,
				UnitsPerPrice:     bigStr(c.UnitsPerPrice),
				WorkUnitEstimator: c.WorkUnitEstimatorJSON,
				Job:               c.JobJSON,
				Session:           c.SessionJSON,
				ID:                c.CapabilityID,
				Capability:        c.Capability,
				Offering:          c.Offering,
				InteractionMode:   derefString(c.InteractionMode),
				Name:              derefString(c.Name),
				Category:          derefString(c.Category),
				PriceWei:          bigStr(c.PricePerWorkUnitWei),
				Extra:             c.ExtraJSON,
				Constraints:       c.ConstraintsJSON,
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
	IdempotencyKey string `header:"Idempotency-Key" maxLength:"255"`
	Body           struct {
		InputURL      string     `json:"input_url" required:"true" format:"uri"`
		Preset        string     `json:"preset,omitempty" enum:"abr-standard,abr-premium,abr-mobile,abr-hevc,abr-av1" doc:"Runner preset. Defaults to abr-standard."`
		Ladder        *ABRLadder `json:"ladder,omitempty"`
		EstimatedSecs int        `json:"estimated_input_seconds,omitempty" minimum:"0" doc:"Input duration in seconds, rounded up. Defaults to 60; estimate assumes 60 fps."`
		MaxTotalUnits int64      `json:"max_total_units,omitempty" minimum:"0" doc:"Optional authorization cap in video-frame-megapixels. Must cover the estimate and fit the server ceiling. Defaults to estimate plus 25% headroom."`
	}
}

type ABRRendition struct {
	Name        string `json:"name"`
	PlaylistURL string `json:"playlist_url"`
	Bandwidth   int    `json:"bandwidth"`
}

type ABRJob struct {
	AccountingState   string         `json:"accounting_state,omitempty"`
	WorkUnit          string         `json:"work_unit,omitempty"`
	ActualUnits       *int64         `json:"actual_units,omitempty"`
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
	huma.Register(api, huma.Operation{OperationID: "v1-abr-submit", Method: http.MethodPost, Path: "/api/v1/abr", DefaultStatus: 202, Summary: "Submit a durable ABR transcode operation", Tags: []string{"v1"}}, func(ctx context.Context, in *ABRIn) (*ABROut, error) {
		if deps.Paid == nil {
			return nil, huma.Error503ServiceUnavailable("loc_unavailable")
		}
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("invalid_api_key")
		}
		return deps.Paid.SubmitABR(ctx, ak, in)
	})
	huma.Register(api, huma.Operation{OperationID: "v1-abr-get", Method: http.MethodGet, Path: "/api/v1/abr/{id}", Summary: "Get transcode progress and accounting state", Tags: []string{"v1"}}, func(ctx context.Context, in *struct {
		ID uuid.UUID `path:"id"`
	}) (*ABROut, error) {
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("invalid_api_key")
		}
		if deps.Paid == nil {
			return nil, huma.Error503ServiceUnavailable("loc_unavailable")
		}
		return deps.Paid.ViewABR(ctx, in.ID, ak.ID)
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

type LiveIn struct {
	IdempotencyKey string `header:"Idempotency-Key" maxLength:"255"`
	Body           struct {
		Name          string     `json:"name,omitempty" maxLength:"200"`
		OutputProfile string     `json:"output_profile,omitempty"`
		Ladder        *ABRLadder `json:"ladder,omitempty"`
	}
}

type LiveIngest struct {
	RTMPURL   string `json:"rtmp_url"`
	StreamKey string `json:"stream_key,omitempty" doc:"Returned on create and identical idempotent create replay; omitted from GET."`
}

type LivePlayback struct {
	HLSURL string `json:"hls_url"`
}

type LiveSessionView struct {
	SettlementPending bool         `json:"settlement_pending"`
	AccountingState   string       `json:"accounting_state,omitempty"`
	OutputState       string       `json:"output_state,omitempty"`
	ActualUnits       int64        `json:"actual_units"`
	LastFailureCode   string       `json:"last_failure_code,omitempty"`
	ID                uuid.UUID    `json:"id"`
	Status            string       `json:"status"`
	Ingest            LiveIngest   `json:"ingest"`
	Playback          LivePlayback `json:"playback"`
	CloseReason       string       `json:"close_reason,omitempty"`
	CreatedAt         time.Time    `json:"created_at"`
	StartedAt         *time.Time   `json:"started_at,omitempty"`
	EndedAt           *time.Time   `json:"ended_at,omitempty"`
}

type LiveCreateOut struct {
	Body struct {
		Session LiveSessionView `json:"session"`
	}
}

func registerV1Live(api huma.API, deps Deps) {
	huma.Register(api, huma.Operation{OperationID: "v1-live-create", Method: http.MethodPost, Path: "/api/v1/live", Summary: "Create a paid live session", Tags: []string{"v1"}}, func(ctx context.Context, in *LiveIn) (*LiveCreateOut, error) {
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("invalid_api_key")
		}
		if deps.Paid == nil {
			return nil, huma.Error503ServiceUnavailable("loc_unavailable")
		}
		return deps.Paid.SubmitLive(ctx, ak, in)
	})
	huma.Register(api, huma.Operation{OperationID: "v1-live-get", Method: http.MethodGet, Path: "/api/v1/live/{id}", Summary: "Get live session state", Tags: []string{"v1"}}, func(ctx context.Context, in *struct {
		ID uuid.UUID `path:"id"`
	}) (*LiveCreateOut, error) {
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("invalid_api_key")
		}
		if deps.Paid == nil {
			return nil, huma.Error503ServiceUnavailable("loc_unavailable")
		}
		return deps.Paid.ViewLive(ctx, in.ID, ak.ID, false)
	})
	huma.Register(api, huma.Operation{OperationID: "v1-live-delete", Method: http.MethodDelete, Path: "/api/v1/live/{id}", DefaultStatus: 202, Summary: "Request durable termination and settlement", Tags: []string{"v1"}}, func(ctx context.Context, in *struct {
		ID uuid.UUID `path:"id"`
	}) (*GenericOK, error) {
		ak := APIKeyFromCtx(ctx)
		if ak == nil {
			return nil, huma.Error401Unauthorized("invalid_api_key")
		}
		if deps.Paid == nil {
			return nil, huma.Error503ServiceUnavailable("loc_unavailable")
		}
		if err := deps.Paid.StopLive(ctx, in.ID, ak.ID); err != nil {
			return nil, err
		}
		out := &GenericOK{}
		out.Body.OK = true
		return out, nil
	})
}

// abrOutputs is the shape the abr-runner consumes for output_urls.
type abrOutputs struct {
	Manifest   string                        `json:"manifest"`
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
