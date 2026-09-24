package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/abr"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/crypto"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/livepeer"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/loc"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"
	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

func (e *PaidEngine) existing(ctx context.Context, key uuid.UUID, kind, request, hash string) (*repo.PaidOperation, error) {
	o, err := e.ops.FindRequest(ctx, key, kind, request)
	if err != nil {
		return nil, err
	}
	if o != nil && o.RequestHash != hash {
		return nil, huma.Error409Conflict("idempotency_key_reuse")
	}
	return o, nil
}
func requestKey(s string) string {
	if s == "" {
		return uuid.NewString()
	}
	return s
}
func requestHash(body any) string { b, _ := json.Marshal(body); return livepeer.RequestDigest(b) }
func (e *PaidEngine) SubmitABR(ctx context.Context, ak *repo.APIKey, in *ABRIn) (*ABROut, error) {
	if e.deps.S3 == nil {
		return nil, huma.Error503ServiceUnavailable("s3_unavailable")
	}
	if in.Body.Ladder != nil {
		return nil, huma.Error400BadRequest("Modules v2 ABR accepts a named preset; inline ladders are unsupported")
	}
	input, err := url.Parse(in.Body.InputURL)
	if err != nil || (input.Scheme != "https" && input.Scheme != "http") || input.Host == "" {
		return nil, huma.Error400BadRequest("input_url must be an HTTP(S) URL")
	}
	presetName := in.Body.Preset
	if presetName == "" {
		presetName = "abr-standard"
	}
	preset, ok := abr.Get(presetName)
	if !ok {
		return nil, huma.Error400BadRequest("unknown preset")
	}
	key, hash := requestKey(in.IdempotencyKey), requestHash(in.Body)
	if prior, err := e.existing(ctx, ak.ID, "abr", key, hash); err != nil {
		return nil, err
	} else if prior != nil {
		return e.ViewABR(ctx, prior.ID, ak.ID)
	}
	// Confirm the catalog's protocol/transport and offering before reserving any
	// funds. LOC still chooses and locks the concrete route during issuance.
	if err := e.checkOffering(ctx, e.deps.Cfg.ABRCapability, e.deps.Cfg.ABROffering, "paid-job/v1", "video-frame-megapixel"); err != nil {
		return nil, err
	}
	id := uuid.New()
	outputs, master, err := mintABROutputs(ctx, e.deps, ak.ID.String(), id.String(), preset)
	if err != nil {
		return nil, huma.Error502BadGateway("output_presign_failed")
	}
	type artifact struct {
		ArtifactURI string `json:"artifact_uri"`
		UploadURL   string `json:"upload_url"`
	}
	renditions := map[string]any{}
	var listed []ABRRendition
	for _, r := range preset.Renditions {
		refs := outputs.Renditions[r.Name]
		prefix := fmt.Sprintf("abr-out/%s/%s/%s", ak.ID, id, r.Name)
		playlist := e.deps.S3.PublicObjectURL(prefix + "/playlist.m3u8")
		renditions[r.Name] = map[string]any{"playlist": artifact{playlist, refs.Playlist}, "stream": artifact{e.deps.S3.PublicObjectURL(prefix + "/stream.mp4"), refs.Stream}}
		listed = append(listed, ABRRendition{Name: r.Name, PlaylistURL: playlist})
	}
	body, err := json.Marshal(map[string]any{"schema": "video-transcode-abr/v2", "workload_id": id.String(), "input": map[string]any{"download_url": in.Body.InputURL}, "ladder": map[string]any{"preset": presetName}, "output": map[string]any{"manifest": artifact{master, outputs.Manifest}, "renditions": renditions}})
	if err != nil {
		return nil, err
	}
	estimated := estimateABRUnits(preset, in.Body.EstimatedSecs)
	if estimated > e.deps.Cfg.ABRMaxTotalUnits {
		return nil, huma.Error400BadRequest("estimated video-frame-megapixel exceeds ABR_MAX_TOTAL_UNITS")
	}
	s := &operationSecrets{Capability: e.deps.Cfg.ABRCapability, Offering: e.deps.Cfg.ABROffering, Body: body, CallerPublicKey: e.signer.PublicKey(), EstimatedUnits: estimated, LimitUnits: e.deps.Cfg.ABRMaxTotalUnits}
	v := &operationView{Status: "queued", Preset: presetName, MasterURL: master, Renditions: listed, WorkUnit: "video-frame-megapixel"}
	o := &repo.PaidOperation{ID: id, APIKeyID: ak.ID, ReservationID: uuid.New(), Kind: "abr", RequestKey: key, RequestHash: hash, State: "queued"}
	if err = e.seal(o, s, v); err != nil {
		return nil, err
	}
	if err = e.ops.CreateIntent(ctx, o, s.Capability, s.Offering, "", "", "", estimated); err != nil {
		if prior, perr := e.existing(ctx, ak.ID, "abr", key, hash); perr != nil {
			return nil, perr
		} else if prior != nil {
			return e.ViewABR(ctx, prior.ID, ak.ID)
		}
		return nil, huma.Error500InternalServerError("operation_create_failed")
	}
	e.Wake()
	return e.ViewABR(ctx, id, ak.ID)
}
func estimateABRUnits(preset abr.Preset, seconds int) int64 {
	// Estimates size admission only; signed measured units determine billing.
	// Sixty fps is a conservative estimate when clients only know duration.
	if seconds <= 0 {
		seconds = 60
	}
	pixels := float64(0)
	for _, r := range preset.Renditions {
		if r.Video != nil {
			pixels += float64(r.Video.Width) * float64(r.Video.Height)
		}
	}
	units := math.Ceil(float64(seconds) * 60 * pixels / 1e6)
	if units >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return max(1, int64(units))
}
func (e *PaidEngine) ViewABR(ctx context.Context, id, key uuid.UUID) (*ABROut, error) {
	o, err := e.ops.Get(ctx, id)
	if err != nil {
		return nil, huma.Error500InternalServerError("operation_read_failed")
	}
	if o == nil || o.APIKeyID != key || o.Kind != "abr" {
		return nil, huma.Error404NotFound("not_found")
	}
	v := new(operationView)
	if err = json.Unmarshal(o.PublicJSON, v); err != nil {
		return nil, err
	}
	out := &ABROut{}
	out.Body.Job = ABRJob{ID: id, Status: v.Status, Phase: v.Phase, OverallProgress: v.Progress, ErrorCode: v.FailureCode, MasterPlaylistURL: v.MasterURL, Renditions: v.Renditions, CreatedAt: o.CreatedAt, AccountingState: o.State, ActualUnits: v.ActualUnits, WorkUnit: v.WorkUnit}
	// URLs are prepared destinations, not success signals. Keep playback hidden
	// until the runner result and verified accounting both completed.
	if v.Status != "succeeded" {
		out.Body.Job.MasterPlaylistURL = ""
		out.Body.Job.Renditions = nil
	}
	return out, nil
}
func (e *PaidEngine) SubmitLive(ctx context.Context, ak *repo.APIKey, in *LiveIn) (*LiveCreateOut, error) {
	if e.deps.Cfg.LiveRTMPPort <= 0 {
		return nil, huma.Error503ServiceUnavailable("live_ingest_disabled")
	}
	if e.publicRTMPURL() == "" {
		return nil, huma.Error503ServiceUnavailable("LIVE_EXTERNAL_RTMP_URL or GATEWAY_PUBLIC_URL is required")
	}
	if in.Body.Ladder != nil {
		return nil, huma.Error400BadRequest("Modules v2 live accepts output_profile; inline ladders are unsupported")
	}
	key, hash := requestKey(in.IdempotencyKey), requestHash(in.Body)
	if prior, err := e.existing(ctx, ak.ID, "live", key, hash); err != nil {
		return nil, err
	} else if prior != nil {
		return e.ViewLive(ctx, prior.ID, ak.ID, true)
	}
	if err := e.checkOffering(ctx, e.deps.Cfg.LiveCapability, e.deps.Cfg.LiveGatewayIngestOffering, "paid-session/v1", "output_seconds"); err != nil {
		return nil, err
	}
	id := uuid.New()
	customerKey, err := crypto.RandomToken(32)
	if err != nil {
		return nil, err
	}
	profile := in.Body.OutputProfile
	if profile == "" {
		profile = e.deps.Cfg.LiveOutputProfile
	}
	params := map[string]any{"schema": "rtmp-hls-session/v1", "publisher_mode": "gateway-relay", "output_profile": profile, "metering_rendition": e.deps.Cfg.LiveMeteringRendition, "storage": map[string]any{"kind": "runner-local"}}
	cap := min(e.deps.Cfg.LiveMaxTotalUnits, int64(max(120, e.deps.Cfg.LiveTopupFundSecs*2)))
	s := &operationSecrets{Capability: e.deps.Cfg.LiveCapability, Offering: e.deps.Cfg.LiveGatewayIngestOffering, Params: params, CallerPublicKey: e.signer.PublicKey(), LimitUnits: e.deps.Cfg.LiveMaxTotalUnits, CurrentCap: cap, CustomerKey: customerKey}
	v := &operationView{Status: "provisioning", WorkUnit: "output_seconds", OutputState: "waiting"}
	o := &repo.PaidOperation{ID: id, LiveStreamID: &id, APIKeyID: ak.ID, ReservationID: uuid.New(), Kind: "live", RequestKey: key, RequestHash: hash, State: "queued"}
	if err = e.seal(o, s, v); err != nil {
		return nil, err
	}
	if err = e.ops.CreateIntent(ctx, o, s.Capability, s.Offering, in.Body.Name, crypto.HashWithPepper(customerKey, e.deps.Cfg.IPHashPepper), customerKey[len(customerKey)-4:], cap); err != nil {
		if prior, perr := e.existing(ctx, ak.ID, "live", key, hash); perr != nil {
			return nil, perr
		} else if prior != nil {
			return e.ViewLive(ctx, prior.ID, ak.ID, true)
		}
		return nil, huma.Error500InternalServerError("operation_create_failed")
	}
	// Attempt admission synchronously for the common ready-on-create UX. Any
	// interruption leaves a recoverable journal processed by the worker loop.
	if err = e.Process(ctx, id); err != nil && loc.IsInsufficientCredit(err) {
		return nil, paymentRequiredError(err)
	}
	e.Wake()
	return e.ViewLive(ctx, id, ak.ID, true)
}
func (e *PaidEngine) ViewLive(ctx context.Context, id, key uuid.UUID, includeKey bool) (*LiveCreateOut, error) {
	o, err := e.ops.Get(ctx, id)
	if err != nil {
		return nil, huma.Error500InternalServerError("operation_read_failed")
	}
	if o == nil || o.APIKeyID != key || o.Kind != "live" {
		return nil, huma.Error404NotFound("not_found")
	}
	s, v, err := e.decode(o)
	if err != nil {
		return nil, huma.Error500InternalServerError("operation_recovery_key_unavailable")
	}
	out := &LiveCreateOut{}
	out.Body.Session = LiveSessionView{ID: id, Status: v.Status, Ingest: LiveIngest{RTMPURL: e.publicRTMPURL()}, Playback: LivePlayback{HLSURL: v.MasterURL}, CreatedAt: o.CreatedAt, EndedAt: o.FinishedAt, CloseReason: v.CloseReason, AccountingState: o.State, OutputState: v.OutputState, LastFailureCode: v.FailureCode}
	if o.StopRequested && o.FinishedAt == nil {
		out.Body.Session.Status = "ending"
	}
	if includeKey && o.FinishedAt == nil && !o.StopRequested {
		out.Body.Session.Ingest.StreamKey = s.CustomerKey
	}
	if v.ActualUnits != nil {
		out.Body.Session.ActualUnits = *v.ActualUnits
	}
	return out, nil
}
func (e *PaidEngine) StopLive(ctx context.Context, id, key uuid.UUID) error {
	o, err := e.ops.Get(ctx, id)
	if err != nil {
		return err
	}
	if o == nil || o.APIKeyID != key || o.Kind != "live" {
		return huma.Error404NotFound("not_found")
	}
	if err = e.ops.RequestStop(ctx, id); err != nil {
		return err
	}
	if e.deps.RTMPProbe != nil {
		e.deps.RTMPProbe.CloseSession(id.String())
	}
	e.Wake()
	return nil
}
func (e *PaidEngine) checkOffering(ctx context.Context, capability, offering, protocol, unit string) error {
	rows, err := e.deps.Caps.ListActive(ctx)
	if err != nil {
		return huma.Error503ServiceUnavailable("catalog_unavailable")
	}
	for _, c := range rows {
		if c.Capability != capability || c.Offering != offering {
			continue
		}
		if c.Protocol != protocol || c.WorkUnit != unit {
			return huma.Error503ServiceUnavailable("offering_contract_mismatch")
		}
		if protocol == "paid-job/v1" {
			var job struct {
				Transports []string `json:"transports"`
			}
			if json.Unmarshal(c.JobJSON, &job) != nil {
				return huma.Error503ServiceUnavailable("offering_transport_missing")
			}
			for _, t := range job.Transports {
				if t == "stream" {
					return nil
				}
			}
			return huma.Error503ServiceUnavailable("offering_stream_unsupported")
		}
		var session struct {
			DescriptorSchema string `json:"descriptor_schema"`
		}
		if json.Unmarshal(c.SessionJSON, &session) != nil || session.DescriptorSchema != "rtmp-hls/v1" {
			return huma.Error503ServiceUnavailable("offering_descriptor_unsupported")
		}
		return nil
	}
	return huma.Error503ServiceUnavailable("no_capable_offering")
}
