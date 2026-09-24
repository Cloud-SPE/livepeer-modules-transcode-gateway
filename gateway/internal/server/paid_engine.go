package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/crypto"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/livepeer"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/loc"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"
	"github.com/google/uuid"
)

// PaidEngine drives only durably recorded operations. Exact request bytes,
// authorizations and credentials survive restart encrypted under an external
// key. Product success is never inferred from an HTTP accept or an S3 object.
// Every issuance/dispatch is replayed with its original idempotency identity.
type PaidEngine struct {
	deps   Deps
	ops    *repo.OperationRepo
	box    *crypto.SecretBox
	signer *livepeer.CallerSigner
	wake   chan struct{}
	slots  chan struct{}
}

type operationSecrets struct {
	Capability           string
	Offering             string
	Body                 json.RawMessage
	Params               map[string]any
	CallerPublicKey      string
	EstimatedUnits       int64
	LimitUnits           int64
	CurrentCap           int64
	CustomerKey          string
	UpstreamURL          string
	UpstreamKeyExpiresAt time.Time
	KeyIssueRequestID    string
	Job                  *loc.CreateJobResponseV2
	Preparation          *loc.PrepareSessionResponseV2
	Session              *loc.CreateSessionResponseV2
	Broker               *brokerSession
	Claim                *livepeer.TerminalClaim
	DispatchAttempted    bool
	RefillID             string
	RefillBody           json.RawMessage
	RefillCap            int64
	Refill               *loc.RefillSessionResponseV2
	EndReason            string
}

// Private journal shape intentionally includes only fields required for
// recovery. Never marshal it into a public API response.
type brokerSession struct {
	SessionID  string `json:"session_id"`
	Credential string `json:"credential"`
	WorkID     string `json:"work_id"`
	State      string `json:"state"`
	Runtime    struct {
		Schema string `json:"schema"`
		Public struct {
			RTMPURL     string `json:"rtmp_url"`
			HLSURL      string `json:"hls_url"`
			KeyIssueURL string `json:"key_issue_url"`
		} `json:"public"`
		Grants []struct {
			ID         string   `json:"id"`
			Operations []string `json:"operations"`
			Secret     string   `json:"secret"`
			ExpiresAt  string   `json:"expires_at"`
		} `json:"grants"`
	} `json:"runtime"`
	Balance struct {
		Status        string `json:"status"`
		RunwaySeconds int64  `json:"runway_seconds_estimate"`
		ClaimedUnits  int64  `json:"claimed_units"`
		Remaining     int64  `json:"authorization_cap_remaining_units"`
		WillRefuse    bool   `json:"will_refuse_next_refill"`
	} `json:"balance"`
	Usage struct {
		Unit         string `json:"unit"`
		ClaimedTotal int64  `json:"claimed_total"`
	} `json:"usage"`
	Lease struct {
		ExpiresAt string `json:"expires_at"`
	} `json:"lease"`
	OutputState     string `json:"output_state"`
	LastFailureCode string `json:"last_failure_code"`
	CloseReason     string `json:"close_reason"`
}

type operationView struct {
	Status          string         `json:"status"`
	Phase           string         `json:"phase,omitempty"`
	Progress        float64        `json:"progress,omitempty"`
	Preset          string         `json:"preset,omitempty"`
	MasterURL       string         `json:"master_url,omitempty"`
	Renditions      []ABRRendition `json:"renditions,omitempty"`
	WorkUnit        string         `json:"work_unit"`
	ActualUnits     *int64         `json:"actual_units,omitempty"`
	OutputState     string         `json:"output_state,omitempty"`
	FailureCode     string         `json:"failure_code,omitempty"`
	CloseReason     string         `json:"close_reason,omitempty"`
	TerminalOutcome string         `json:"terminal_outcome,omitempty"`
}

func NewPaidEngine(deps Deps, ops *repo.OperationRepo) (*PaidEngine, error) {
	if deps.LOC == nil {
		return nil, nil
	}
	signer, err := livepeer.NewCallerSigner(deps.Cfg.CallerPrivateKey)
	if err != nil {
		return nil, err
	}
	box, err := crypto.NewSecretBox(deps.Cfg.OperationSecretsKey)
	if err != nil {
		return nil, err
	}
	return &PaidEngine{deps: deps, ops: ops, box: box, signer: signer, wake: make(chan struct{}, 1), slots: make(chan struct{}, 8)}, nil
}
func (e *PaidEngine) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}
func (e *PaidEngine) Run(ctx context.Context) {
	// Live control has its own lane so long ABR streams cannot starve refills.
	var wg sync.WaitGroup
	var mu sync.Mutex
	busy := map[uuid.UUID]bool{}
	queues := map[string]chan uuid.UUID{"abr": make(chan uuid.UUID, 2), "live": make(chan uuid.UUID, 16)}
	for _, queue := range queues {
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(q chan uuid.UUID) {
				defer wg.Done()
				for id := range q {
					_ = e.Process(ctx, id)
					mu.Lock()
					delete(busy, id)
					mu.Unlock()
				}
			}(queue)
		}
	}
	defer func() {
		for _, queue := range queues {
			close(queue)
		}
		wg.Wait()
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		for _, kind := range []string{"live", "abr"} {
			ids, err := e.ops.Pending(ctx, kind, 32)
			if err != nil && ctx.Err() == nil {
				e.deps.Log.Warn("paid operation scan failed", "kind", kind)
			}
			for _, id := range ids {
				mu.Lock()
				if busy[id] {
					mu.Unlock()
					continue
				}
				busy[id] = true
				mu.Unlock()
				select {
				case queues[kind] <- id:
				default:
					mu.Lock()
					delete(busy, id)
					mu.Unlock()
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-e.wake:
		}
	}
}
func (e *PaidEngine) decode(o *repo.PaidOperation) (*operationSecrets, *operationView, error) {
	b, err := e.box.Open(o.ID.String(), o.Secrets)
	if err != nil {
		return nil, nil, errors.New("operation secrets cannot be decrypted; restore original wrapping key")
	}
	s := new(operationSecrets)
	v := new(operationView)
	if err = json.Unmarshal(b, s); err != nil {
		return nil, nil, err
	}
	if err = json.Unmarshal(o.PublicJSON, v); err != nil {
		return nil, nil, err
	}
	if s.CallerPublicKey != e.signer.PublicKey() {
		return nil, nil, errors.New("operation caller key changed; restore original caller key")
	}
	return s, v, nil
}
func (e *PaidEngine) seal(o *repo.PaidOperation, s *operationSecrets, v *operationView) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	o.Secrets, err = e.box.Seal(o.ID.String(), b)
	if err != nil {
		return err
	}
	o.PublicJSON, err = json.Marshal(v)
	return err
}
func (e *PaidEngine) save(ctx context.Context, l *repo.OperationLock, o *repo.PaidOperation, s *operationSecrets, v *operationView) error {
	if err := e.seal(o, s, v); err != nil {
		return err
	}
	return l.Save(ctx, o)
}
func (e *PaidEngine) Process(ctx context.Context, id uuid.UUID) error {
	// Leave pool connections available for projections while advisory locks are
	// held. This also bounds simultaneous synchronous live admissions.
	select {
	case e.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-e.slots }()
	lock, err := e.ops.Lock(ctx, id)
	if err != nil || lock == nil {
		return err
	}
	defer lock.Close()
	o, err := lock.Get(ctx)
	if err != nil || o == nil || o.FinishedAt != nil {
		return err
	}
	s, v, err := e.decode(o)
	if err != nil {
		code := "recovery_key_unavailable"
		o.LastError = &code
		o.NextAttemptAt = time.Now().Add(time.Minute)
		e.deps.Log.Error("paid operation recovery key unavailable", "operation_id", o.ID)
		// Preserve the unreadable ciphertext verbatim for key restoration.
		if saveErr := lock.Save(ctx, o); saveErr != nil {
			return saveErr
		}
		return err
	}
	o.Attempts++
	o.LastError = nil
	if o.Kind == "abr" {
		err = e.runABR(ctx, lock, o, s, v)
	} else {
		err = e.runLive(ctx, lock, o, s, v)
	}
	if err != nil {
		if o.State != "issuance_refused" {
			o.FinishedAt = nil
		}
		// Raw upstream errors may contain signed URLs or bearer material. Expose
		// only bounded stable codes, never request bodies or third-party messages.
		code := safePaidError(err)
		o.LastError = &code
		delay := time.Duration(min(60, 2*o.Attempts)) * time.Second
		o.NextAttemptAt = time.Now().Add(delay)
		e.deps.Log.Warn("paid operation pending retry", "operation_id", o.ID, "state", o.State, "code", code)
	}
	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if saveErr := e.save(persistCtx, lock, o, s, v); saveErr != nil {
		return saveErr
	}
	return err
}
func safePaidError(err error) string {
	var ae *loc.APIError
	if errors.As(err, &ae) {
		return fmt.Sprintf("loc_http_%d", ae.StatusCode)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "upstream_timeout"
	}
	return "upstream_or_accounting_pending"
}
func (e *PaidEngine) auth(s *operationSecrets, request, authorization, workID string) (livepeer.BrokerAuth, error) {
	proof, err := e.signer.Proof(authorization)
	return livepeer.BrokerAuth{Capability: s.Capability, Offering: s.Offering, RequestID: request, Authorization: authorization, CallerProof: proof, WorkID: workID}, err
}
func (e *PaidEngine) runABR(ctx context.Context, l *repo.OperationLock, o *repo.PaidOperation, s *operationSecrets, v *operationView) error {
	if s.Job == nil {
		o.State = "authorizing"
		if err := e.save(ctx, l, o, s, v); err != nil {
			return err
		}
		job, err := e.deps.LOC.CreateJobV2(ctx, loc.CreateJobRequestV2{RequestID: o.ID.String(), Capability: s.Capability, Offering: s.Offering, Transport: "stream", EstimatedUnits: s.EstimatedUnits, MaxTotalUnits: s.LimitUnits, CallerPublicKey: s.CallerPublicKey, WorkloadRequestDigest: livepeer.RequestDigest(s.Body)})
		if err != nil {
			return e.issuanceFailure(ctx, o, v, err)
		}
		s.Job = job
		o.State = "authorized"
		if err = e.save(ctx, l, o, s, v); err != nil {
			return err
		}
	}
	if s.Job.WorkUnit != "video-frame-megapixel" {
		return errors.New("ABR offering work unit is incompatible")
	}
	if err := e.deps.Usage.SetLOCJob(ctx, o.ID, s.Job.JobID, s.Job.WorkID, s.Job.FundedValueWei.BigInt(), s.Job.ExpectedValueWei.BigInt()); err != nil {
		return err
	}
	if s.Claim == nil && s.DispatchAttempted {
		claim, err := e.deps.HTTP.LookupJobV2(ctx, s.Job.BrokerURL, s.Job.RequestID, "", s.Job.WorkUnit, s.Job.WorkID)
		if err == nil {
			s.Claim = claim
		}
		// LOC's terminal accounting outcome is authoritative even when the broker
		// is unavailable. Never label a conservative charge as broker settlement.
		if s.Claim == nil {
			st, serr := e.deps.LOC.GetJobV2(ctx, s.Job.JobID)
			if serr == nil && st.JobID == s.Job.JobID && st.ClosedAt != nil && !st.ClosedAt.IsZero() && st.BilledValueWei != nil && st.AccountingOutcome != "" {
				o.State = st.AccountingOutcome
				v.Status = "failed"
				v.FailureCode = st.AccountingOutcome
				o.FinishedAt = st.ClosedAt
				if st.ActualUnits != nil {
					v.ActualUnits = st.ActualUnits
				}
				if st.AccountingOutcome == "broker_settled" && v.TerminalOutcome == "succeeded" {
					v.Status, v.Progress, v.FailureCode = "succeeded", 100, ""
				}
				if err := e.deps.Usage.MarkSettled(ctx, o.ReservationID, st.BilledValueWei.BigInt(), repo.SettleSettled); err != nil {
					return err
				}
				if err := e.deps.Usage.Commit(ctx, o.ReservationID, repo.CommitInput{BrokerURL: s.Job.BrokerURL, CommittedWorkUnits: st.ActualUnits}); err != nil {
					return err
				}
				return e.deps.Usage.RecordRunnerWebhook(ctx, o.ID, repo.RunnerStateUpdate{Status: map[bool]string{true: "complete", false: "error"}[v.Status == "succeeded"], Phase: v.Phase, Progress: v.Progress, ErrorCode: v.FailureCode, CompletedAt: st.ClosedAt})
			}
		}
	}
	if s.Claim == nil {
		auth, err := e.auth(s, s.Job.RequestID, s.Job.SpendAuthorization, s.Job.WorkID)
		if err != nil {
			return err
		}
		s.DispatchAttempted = true
		o.State = "dispatching"
		v.Status = "processing"
		if err = e.save(ctx, l, o, s, v); err != nil {
			return err
		}
		jobCtx, cancel := context.WithTimeout(ctx, time.Duration(e.deps.Cfg.ABRJobTimeoutSecs)*time.Second)
		defer cancel()
		lastSaved := time.Time{}
		claim, err := e.deps.HTTP.SubmitJobV2(jobCtx, s.Job.BrokerURL, auth, s.Body, s.Job.WorkUnit, func(event livepeer.SSEEvent) error {
			var data struct {
				Schema          string  `json:"schema"`
				WorkloadID      string  `json:"workload_id"`
				Phase           string  `json:"phase"`
				OverallProgress float64 `json:"overall_progress"`
				Outcome         string  `json:"outcome"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return err
			}
			if data.WorkloadID != o.ID.String() {
				return errors.New("runner workload identity mismatch")
			}
			switch data.Schema {
			case "video-transcode-abr-progress/v2":
				if data.OverallProgress < 0 || data.OverallProgress > 100 {
					return errors.New("invalid progress")
				}
				v.Phase = data.Phase
				v.Progress = data.OverallProgress
			case "video-transcode-abr-result/v2":
				if data.Outcome != "succeeded" && data.Outcome != "failed" {
					return errors.New("invalid runner terminal outcome")
				}
				v.TerminalOutcome = data.Outcome
			default:
				return errors.New("unsupported runner event schema")
			}
			if time.Since(lastSaved) > time.Second || v.TerminalOutcome != "" {
				lastSaved = time.Now()
				return e.save(jobCtx, l, o, s, v)
			}
			return nil
		})
		if err != nil {
			return err
		}
		s.Claim = claim
		o.State = "accounting_pending"
		if err = e.save(ctx, l, o, s, v); err != nil {
			return err
		}
	}
	claim := s.Claim
	settled, err := e.deps.LOC.SettleJobV2(ctx, s.Job.JobID, loc.SettleJobRequestV2{ActualUnits: claim.ActualUnits, BrokerJobID: claim.BrokerJobID, WorkUnit: claim.WorkUnit, Settlement: claim.Settlement})
	if err != nil {
		if !loc.IsAlreadySettled(err) {
			return err
		}
		st, serr := e.deps.LOC.GetJobV2(ctx, s.Job.JobID)
		if serr != nil || st.JobID != s.Job.JobID || st.ClosedAt == nil || st.ActualUnits == nil || *st.ActualUnits != claim.ActualUnits || st.BilledValueWei == nil || st.AccountingOutcome != "broker_settled" {
			return err
		}
		if err = e.deps.Usage.MarkSettled(ctx, o.ReservationID, st.BilledValueWei.BigInt(), repo.SettleSettled); err != nil {
			return err
		}
	} else {
		if settled.JobID != s.Job.JobID || settled.ActualUnits != claim.ActualUnits || settled.BilledValueWei == nil || settled.ClosedAt.IsZero() {
			return errors.New("LOC settlement response does not match job claim")
		}
		if err = e.deps.Usage.MarkSettled(ctx, o.ReservationID, settled.BilledValueWei.BigInt(), repo.SettleSettled); err != nil {
			return err
		}
	}
	v.ActualUnits = &claim.ActualUnits
	v.Status = "failed"
	// Lost content is not fabricated from settlement alone. Safe artifact URLs
	// are known locally, but the typed terminal result is still required.
	if v.TerminalOutcome == "succeeded" {
		v.Status = "succeeded"
		v.Progress = 100
	}
	o.State = "broker_settled"
	now := time.Now()
	o.FinishedAt = &now
	if err = e.deps.Usage.Commit(ctx, o.ReservationID, repo.CommitInput{BrokerURL: s.Job.BrokerURL, CommittedWorkUnits: &claim.ActualUnits}); err != nil {
		return err
	}
	return e.deps.Usage.RecordRunnerWebhook(ctx, o.ID, repo.RunnerStateUpdate{Status: map[bool]string{true: "complete", false: "error"}[v.Status == "succeeded"], Phase: v.Phase, Progress: v.Progress, CompletedAt: &now})
}

// A definitive issuance refusal has no broker effect. Timeouts, 409 and 5xx
// remain recoverable using the same request id; they cannot imply a refund.
func (e *PaidEngine) issuanceFailure(ctx context.Context, o *repo.PaidOperation, v *operationView, err error) error {
	var ae *loc.APIError
	if (o.Attempts == 1 || o.State == "issuance_refused") && errors.As(err, &ae) && ae.StatusCode >= 400 && ae.StatusCode < 500 && ae.StatusCode != 408 && ae.StatusCode != 409 && ae.StatusCode != 429 {
		o.State = "issuance_refused"
		v.Status = "failed"
		v.FailureCode = safePaidError(err)
		now := time.Now()
		if ferr := e.deps.Usage.Refund(ctx, o.ReservationID, ae.StatusCode, v.FailureCode); ferr != nil {
			return ferr
		}
		if o.LiveStreamID != nil {
			if ferr := e.deps.Live.Fail(ctx, *o.LiveStreamID, v.FailureCode); ferr != nil {
				return ferr
			}
		}
		o.FinishedAt = &now
	}
	return err
}
func convertBroker(in any) (*brokerSession, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	out := new(brokerSession)
	err = json.Unmarshal(b, out)
	return out, err
}
func brokerTerminal(state string) bool { return state == "ended" || state == "failed" }
func (e *PaidEngine) runLive(ctx context.Context, l *repo.OperationLock, o *repo.PaidOperation, s *operationSecrets, v *operationView) error {
	if s.Preparation == nil {
		p, err := e.deps.LOC.PrepareSessionV2(ctx, loc.PrepareSessionRequestV2{RequestID: o.ID.String() + ":prepare", Capability: s.Capability, Offering: s.Offering, DescriptorSchema: "rtmp-hls/v1"})
		if err != nil {
			return e.issuanceFailure(ctx, o, v, err)
		}
		s.Preparation = p
		s.Body, err = json.Marshal(map[string]any{"gateway_session_id": p.GatewaySessionID.String(), "session_params": s.Params})
		if err != nil {
			return err
		}
		o.State = "prepared"
		if err = e.save(ctx, l, o, s, v); err != nil {
			return err
		}
	}
	if s.Session == nil {
		sess, err := e.deps.LOC.OpenSessionV2(ctx, loc.CreateSessionRequestV2{RequestID: o.ID.String(), Capability: s.Capability, Offering: s.Offering, DescriptorSchema: "rtmp-hls/v1", SessionParams: s.Params, EstimatedRunwayUnits: s.CurrentCap, MaxTotalUnits: s.CurrentCap, GatewaySessionID: s.Preparation.GatewaySessionID, PreparationToken: s.Preparation.PreparationToken, RouteBinding: s.Preparation.RouteBinding, WorkloadRequestDigest: livepeer.RequestDigest(s.Body), CallerPublicKey: s.CallerPublicKey})
		if err != nil {
			return e.issuanceFailure(ctx, o, v, err)
		}
		s.Session = sess
		o.State = "authorized"
		if err = e.save(ctx, l, o, s, v); err != nil {
			return err
		}
	}
	if s.Claim != nil {
		return e.finishLive(ctx, o, s, v)
	}
	if s.Broker == nil {
		auth, err := e.auth(s, s.Session.RequestID, s.Session.SpendAuthorization, s.Session.WorkID)
		if err != nil {
			return err
		}
		s.DispatchAttempted = true
		o.State = "dispatching"
		if err = e.save(ctx, l, o, s, v); err != nil {
			return err
		}
		response, err := e.deps.HTTP.OpenSessionV2(ctx, s.Session.BrokerURL, auth, s.Body)
		if err != nil {
			// A capacity refusal can be admitted and zero-settled. Recover its signed
			// evidence by the prepared gateway id rather than inventing a close.
			claim, cerr := e.deps.HTTP.LookupSessionSettlementV2(ctx, s.Session.BrokerURL, s.Preparation.GatewaySessionID.String(), s.Session.WorkID, "output_seconds")
			if cerr == nil {
				s.Claim = claim
				s.EndReason = "open_failed"
				if saveErr := e.save(ctx, l, o, s, v); saveErr != nil {
					return saveErr
				}
				return e.finishLive(ctx, o, s, v)
			}
			if recovered, recoveryErr := e.recoverClosedLive(ctx, o, s, v); recovered || recoveryErr != nil {
				return recoveryErr
			}
			return err
		}
		s.Broker, err = convertBroker(response)
		if err != nil {
			return err
		}
		if s.Broker.Credential == "" || s.Broker.Runtime.Schema != "rtmp-hls/v1" {
			return errors.New("invalid live descriptor")
		}
		o.State = "provisioning"
		if err = e.save(ctx, l, o, s, v); err != nil {
			return err
		}
	}
	// Delete can arrive during admission. Re-read only its monotonic stop flag;
	// never overwrite the journal state we own under this advisory lock.
	current, err := l.Get(ctx)
	if err != nil {
		return err
	}
	if current.StopRequested {
		o.StopRequested = true
	}
	if o.StopRequested && s.EndReason == "" {
		s.EndReason = "gateway_close"
	}
	// A lost top-up response may already have replaced the broker's active
	// authorization. Replay its persisted identity before ending the session,
	// so settlement is bound to the actual predecessor or successor grant.
	if o.StopRequested && e.deps.RTMPProbe != nil {
		e.deps.RTMPProbe.CloseSession(o.ID.String())
	}
	if s.RefillID != "" {
		if err = e.refillLive(ctx, l, o, s, v); err != nil {
			return err
		}
		if o.FinishedAt != nil {
			return nil
		}
	}
	if s.EndReason != "" {
		return e.endLive(ctx, l, o, s, v)
	}
	if s.UpstreamURL == "" {
		if err = e.issueLiveStreamKey(ctx, l, o, s, v); err != nil {
			return err
		}
		v.MasterURL = s.Broker.Runtime.Public.HLSURL
		o.State = "active"
		v.Status = "live"
		if err = e.save(ctx, l, o, s, v); err != nil {
			return err
		}
	}

	if err = e.projectLive(ctx, o, s, v); err != nil {
		return err
	}
	response, err := e.deps.HTTP.StatusSessionV2(ctx, s.Session.BrokerURL, s.Broker.SessionID, s.Broker.Credential)
	if err != nil {
		if recovered, recoveryErr := e.recoverClosedLive(ctx, o, s, v); recovered || recoveryErr != nil {
			return recoveryErr
		}
		return err
	}
	status, err := convertBroker(response)
	if err != nil {
		return err
	}
	if status.Usage.Unit != "output_seconds" || status.Usage.ClaimedTotal < 0 {
		return errors.New("invalid live usage unit")
	}
	v.OutputState = status.OutputState
	v.FailureCode = status.LastFailureCode
	v.ActualUnits = &status.Usage.ClaimedTotal
	if brokerTerminal(status.State) {
		s.EndReason = status.CloseReason
		if s.EndReason == "" {
			s.EndReason = "runner_ended"
		}
		return e.endLive(ctx, l, o, s, v)
	}
	if status.State == "winding_down" {
		s.EndReason = "runner_ended"
		return e.endLive(ctx, l, o, s, v)
	}
	if status.Balance.RunwaySeconds < int64(e.deps.Cfg.LiveTopupRunwayThresholdSecs) && s.Session.Session.Refill == "extensible" && s.CurrentCap < s.LimitUnits && !status.Balance.WillRefuse {
		s.RefillCap = min(s.LimitUnits, s.CurrentCap+int64(e.deps.Cfg.LiveTopupFundSecs))
		s.RefillID = uuid.NewString()
		// Match the SDK's exact empty-object top-up commitment; the signed
		// authorization itself binds gateway identity and cumulative cap.
		s.RefillBody, err = json.Marshal(map[string]any{})
		if err != nil {
			return err
		}
		if err = e.save(ctx, l, o, s, v); err != nil {
			return err
		}
		if err = e.refillLive(ctx, l, o, s, v); err != nil {
			return err
		}
	}
	o.NextAttemptAt = time.Now().Add(time.Duration(max(1, e.deps.Cfg.LiveReconcileIntervalSecs)) * time.Second)
	return e.projectLive(ctx, o, s, v)
}
func (e *PaidEngine) refillLive(ctx context.Context, l *repo.OperationLock, o *repo.PaidOperation, s *operationSecrets, v *operationView) error {
	if s.Refill == nil {
		refill, err := e.deps.LOC.RefillSessionV2(ctx, s.Session.SessionID, loc.RefillSessionRequestV2{RequestID: s.RefillID, MaxTotalUnits: s.RefillCap, ObservedConsumedUnits: v.ActualUnits, WorkloadRequestDigest: livepeer.RequestDigest(s.RefillBody)})
		if err != nil {
			if loc.IsInsufficientCredit(err) {
				clearPendingLiveRefill(s)
				s.EndReason = "refill_refused"
				return e.endLive(ctx, l, o, s, v)
			}
			if recovered, recoveryErr := e.recoverClosedLive(ctx, o, s, v); recovered || recoveryErr != nil {
				return recoveryErr
			}
			return err
		}
		s.Refill = refill
		if err = e.save(ctx, l, o, s, v); err != nil {
			return err
		}
	}
	auth, err := e.auth(s, s.Refill.RequestID, s.Refill.SpendAuthorization, s.Refill.WorkID)
	if err != nil {
		return err
	}
	response, err := e.deps.HTTP.TopUpSessionV2(ctx, s.Session.BrokerURL, s.Broker.SessionID, s.Broker.Credential, auth, s.RefillBody)
	if err != nil {
		// A runner can terminate before a newly issued revision is delivered.
		// Its signed predecessor claim is still valid LOC settlement evidence.
		if claim, claimErr := e.lookupLiveClaim(ctx, s); claimErr == nil {
			s.Claim = claim
			if saveErr := e.save(ctx, l, o, s, v); saveErr != nil {
				return saveErr
			}
			return e.finishLive(ctx, o, s, v)
		}
		if recovered, recoveryErr := e.recoverClosedLive(ctx, o, s, v); recovered || recoveryErr != nil {
			return recoveryErr
		}
		return err
	}
	if response.WorkID != s.Refill.WorkID {
		return errors.New("refill authorization identity mismatch")
	}
	s.Session.WorkID = s.Refill.WorkID
	s.Broker.WorkID = s.Refill.WorkID
	s.CurrentCap = s.RefillCap
	clearPendingLiveRefill(s)
	if err = e.save(ctx, l, o, s, v); err != nil {
		return err
	}
	return e.deps.Live.IncrementLOCRefill(ctx, *o.LiveStreamID)
}
func clearPendingLiveRefill(s *operationSecrets) {
	s.RefillID = ""
	s.RefillBody = nil
	s.Refill = nil
	s.RefillCap = 0
}
func (e *PaidEngine) lookupLiveClaim(ctx context.Context, s *operationSecrets) (*livepeer.TerminalClaim, error) {
	id := s.Preparation.GatewaySessionID.String()
	if s.Broker != nil {
		id = s.Broker.SessionID
	}
	claim, err := e.deps.HTTP.LookupSessionSettlementV2(ctx, s.Session.BrokerURL, id, "", "output_seconds")
	if err != nil {
		return nil, err
	}
	// Scope remains the exact persisted session plus one of its locally known
	// grants. Never accept an unrelated authorization merely because it is signed.
	if claim.WorkID != s.Session.WorkID && (s.Refill == nil || claim.WorkID != s.Refill.WorkID) {
		return nil, errors.New("unknown live settlement authorization")
	}
	if claim.GatewaySessionID != s.Preparation.GatewaySessionID.String() {
		return nil, errors.New("live settlement gateway identity mismatch")
	}
	return claim, nil
}
func (e *PaidEngine) endLive(ctx context.Context, l *repo.OperationLock, o *repo.PaidOperation, s *operationSecrets, v *operationView) error {
	o.State = "ending"
	v.Status = "ending"
	v.CloseReason = s.EndReason
	if err := e.save(ctx, l, o, s, v); err != nil {
		return err
	}
	if e.deps.RTMPProbe != nil {
		e.deps.RTMPProbe.CloseSession(o.ID.String())
	}
	if s.Claim == nil {
		var endErr error
		if s.Broker != nil {
			_, endErr = e.deps.HTTP.EndSessionV2(ctx, s.Session.BrokerURL, s.Broker.SessionID, s.Broker.Credential, s.EndReason)
		}
		// End can succeed remotely and lose its response, or the broker can become
		// unavailable after LOC's janitor has already verified its signed record.
		claim, err := e.lookupLiveClaim(ctx, s)
		if err != nil {
			if recovered, recoveryErr := e.recoverClosedLive(ctx, o, s, v); recovered || recoveryErr != nil {
				return recoveryErr
			}
			if endErr != nil {
				return endErr
			}
			return err
		}
		s.Claim = claim
		o.State = "accounting_pending"
		if err = e.save(ctx, l, o, s, v); err != nil {
			return err
		}
	}
	return e.finishLive(ctx, o, s, v)
}

// recoverClosedLive uses LOC's authenticated final accounting, which the LOC
// session janitor only produces after verifying a signed terminal settlement.
// An open or unknown session remains pending; absence never implies a refund.
func (e *PaidEngine) recoverClosedLive(ctx context.Context, o *repo.PaidOperation, s *operationSecrets, v *operationView) (bool, error) {
	status, err := e.deps.LOC.GetSessionV2(ctx, s.Session.SessionID)
	if err != nil || status.ClosedAt == nil {
		return false, nil
	}
	if status.SessionID != s.Session.SessionID || status.ActualUnits == nil || *status.ActualUnits < 0 || status.BilledValueWei == nil || status.ClosedAt.IsZero() {
		return false, errors.New("incomplete LOC terminal session accounting")
	}
	if err = e.deps.Usage.MarkSettled(ctx, o.ReservationID, status.BilledValueWei.BigInt(), repo.SettleSettled); err != nil {
		return false, err
	}
	o.State = "loc_settled"
	if s.Broker == nil && s.EndReason == "" {
		s.EndReason = "open_failed"
	}
	return true, e.finalizeLiveProjection(ctx, o, s, v, *status.ActualUnits, *status.ClosedAt)
}
func (e *PaidEngine) finishLive(ctx context.Context, o *repo.PaidOperation, s *operationSecrets, v *operationView) error {
	claim := s.Claim
	if claim == nil {
		return errors.New("live terminal claim required")
	}
	closed, err := e.deps.LOC.CloseSessionV2(ctx, s.Session.SessionID, loc.CloseSessionRequestV2{ActualUnits: claim.ActualUnits, Settlement: claim.Settlement})
	if err != nil {
		// Covers a duplicate close and an ambiguous transport/5xx after LOC committed.
		if recovered, recoveryErr := e.recoverClosedLive(ctx, o, s, v); recovered || recoveryErr != nil {
			return recoveryErr
		}
		return err
	}
	if closed.SessionID != s.Session.SessionID || closed.ActualUnits != claim.ActualUnits || closed.BilledValueWei == nil || closed.ClosedAt.IsZero() {
		return errors.New("LOC close accounting identity mismatch")
	}
	if err = e.deps.Usage.MarkSettled(ctx, o.ReservationID, closed.BilledValueWei.BigInt(), repo.SettleSettled); err != nil {
		return err
	}
	o.State = "broker_settled"
	return e.finalizeLiveProjection(ctx, o, s, v, claim.ActualUnits, closed.ClosedAt)
}
func applyLiveTerminalView(s *operationSecrets, v *operationView, actualUnits int64) {
	v.ActualUnits = &actualUnits
	v.Status = "ended"
	v.CloseReason = s.EndReason
	if s.Claim != nil {
		// Signed runtime evidence outranks a local request to close gracefully.
		if s.Claim.CloseReason != "" {
			v.CloseReason = s.Claim.CloseReason
		}
		if s.Claim.OutputState != "" {
			v.OutputState = s.Claim.OutputState
		}
		if s.Claim.FailureCode != "" || v.FailureCode != "stream_key_grant_expired" {
			v.FailureCode = s.Claim.FailureCode
		}
	}
	switch v.CloseReason {
	case "output_failed", "runner_failed", "open_failed", "backend_failed", "capacity_exhausted", "recovery_failed", "ingest_failed", "payment_unrecoverable":
		v.Status = "failed"
	}
	if v.FailureCode != "" || v.OutputState == "stalled" {
		v.Status = "failed"
	}
}
func (e *PaidEngine) finalizeLiveProjection(ctx context.Context, o *repo.PaidOperation, s *operationSecrets, v *operationView, actualUnits int64, closedAt time.Time) error {
	applyLiveTerminalView(s, v, actualUnits)
	o.FinishedAt = &closedAt
	if err := e.projectLive(ctx, o, s, v); err != nil {
		// Final accounting may already have committed, but failed product updates
		// must remain in the worker queue until the projection is repaired.
		o.FinishedAt = nil
		o.State = "accounting_pending"
		return err
	}
	return nil
}
func (e *PaidEngine) projectLive(ctx context.Context, o *repo.PaidOperation, s *operationSecrets, v *operationView) error {
	// Product state is a recoverable projection. Secret ingest material remains
	// only in the encrypted journal; RTMP auth resolves it through UpstreamURL.
	status := "live"
	if v.Status == "provisioning" {
		status = "provisioning"
	}
	if o.FinishedAt != nil {
		status = v.Status
	}
	var brokerID string
	if s.Broker != nil {
		brokerID = s.Broker.SessionID
	}
	_, err := e.deps.Pool.Exec(ctx, `UPDATE live_streams SET status=$2,broker_url=$3,broker_session_id=NULLIF($4,''),loc_session_id=$5,loc_work_id=$6,playback_url=NULLIF($7,''),ingest_url=$8,started_at=COALESCE(started_at,now()),last_broker_sync_at=now(),close_reason=NULLIF($9,''),ended_at=$10,loc_closed_at=$10,runner_status_json=$11 WHERE id=$1`, *o.LiveStreamID, status, s.Session.BrokerURL, brokerID, s.Session.SessionID, s.Session.WorkID, v.MasterURL, e.publicRTMPURL(), v.CloseReason, o.FinishedAt, mustPublicJSON(v))
	if err != nil {
		return err
	}
	if o.FinishedAt != nil {
		return e.deps.Usage.Commit(ctx, o.ReservationID, repo.CommitInput{BrokerURL: s.Session.BrokerURL, CommittedWorkUnits: v.ActualUnits})
	}
	return nil
}

// UpstreamURL runs during RTMP publish admission. Keys are renewed lazily on
// reconnect: runner key activation kicks the current publisher, so periodic
// renewal would interrupt healthy live streams whose initial auth remains valid.
func (e *PaidEngine) UpstreamURL(ctx context.Context, id uuid.UUID) (string, error) {
	o, err := e.ops.GetLive(ctx, id)
	if err != nil || o == nil {
		return "", err
	}
	if o.StopRequested || o.FinishedAt != nil || o.State != "active" {
		return "", nil
	}
	s, _, err := e.decode(o)
	if err != nil {
		return "", err
	}
	if liveKeyUsable(s, time.Now()) {
		return s.UpstreamURL, nil
	}
	select {
	case e.slots <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-e.slots }()
	var lock *repo.OperationLock
	for lock == nil {
		lock, err = e.ops.Lock(ctx, o.ID)
		if err != nil {
			return "", err
		}
		if lock == nil {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	defer lock.Close()
	// Another reconnect or the control worker may have completed while waiting.
	o, err = lock.Get(ctx)
	if err != nil || o == nil {
		return "", err
	}
	if o.StopRequested || o.FinishedAt != nil || o.State != "active" {
		return "", nil
	}
	s, v, err := e.decode(o)
	if err != nil {
		return "", err
	}
	if !liveKeyUsable(s, time.Now()) {
		if err = e.issueLiveStreamKey(ctx, lock, o, s, v); err != nil {
			return "", err
		}
	}
	// Stop can be requested without taking this advisory lock.
	current, err := lock.Get(ctx)
	if err != nil {
		return "", err
	}
	if current.StopRequested || current.FinishedAt != nil {
		return "", nil
	}
	return s.UpstreamURL, nil
}
func liveKeyUsable(s *operationSecrets, now time.Time) bool {
	return s.UpstreamURL != "" && s.KeyIssueRequestID == "" && s.UpstreamKeyExpiresAt.After(now.Add(15*time.Second))
}
func (e *PaidEngine) issueLiveStreamKey(ctx context.Context, l *repo.OperationLock, o *repo.PaidOperation, s *operationSecrets, v *operationView) error {
	if s.Broker == nil {
		return errors.New("live broker descriptor missing")
	}
	if err := validateRuntimeURLs(s.Broker); err != nil {
		return err
	}
	var grant string
	for _, g := range s.Broker.Runtime.Grants {
		expires, err := time.Parse(time.RFC3339, g.ExpiresAt)
		if err != nil || !expires.After(time.Now().Add(15*time.Second)) || g.Secret == "" {
			continue
		}
		for _, op := range g.Operations {
			if op == "stream-key-issue" {
				grant = g.Secret
			}
		}
	}
	if grant == "" {
		s.EndReason = "recovery_failed"
		v.FailureCode = "stream_key_grant_expired"
		o.State = "ending"
		v.Status = "ending"
		v.CloseReason = s.EndReason
		o.NextAttemptAt = time.Now()
		if err := e.save(ctx, l, o, s, v); err != nil {
			return err
		}
		e.Wake()
		return errors.New("stream-key grant expired; session winddown requested")
	}
	// A definitive replay of an expired key can safely advance to a new
	// issuance identity. Ambiguous HTTP failures retain the existing identity.
	for attempt := 0; attempt < 2; attempt++ {
		if s.KeyIssueRequestID == "" {
			if s.UpstreamURL == "" && s.UpstreamKeyExpiresAt.IsZero() {
				s.KeyIssueRequestID = o.ID.String() + ":key"
			} else {
				s.KeyIssueRequestID = o.ID.String() + ":key:" + uuid.NewString()
			}
			if err := e.save(ctx, l, o, s, v); err != nil {
				return err
			}
		}
		body, _ := json.Marshal(map[string]string{"request_id": s.KeyIssueRequestID, "audience": "gateway-relay"})
		var key struct {
			StreamKey string `json:"stream_key"`
			RequestID string `json:"request_id"`
			ExpiresAt string `json:"expires_at"`
		}
		if err := e.deps.HTTP.IssueRuntimeKeyV2(ctx, s.Broker.Runtime.Public.KeyIssueURL, grant, body, &key); err != nil {
			return err
		}
		expires, err := time.Parse(time.RFC3339, key.ExpiresAt)
		if key.RequestID != s.KeyIssueRequestID || key.StreamKey == "" || err != nil {
			return errors.New("invalid stream-key issuance response")
		}
		upstream := strings.TrimRight(s.Broker.Runtime.Public.RTMPURL, "/") + "/" + key.StreamKey
		if parsed, parseErr := url.Parse(upstream); parseErr != nil || parsed.User != nil || parsed.Fragment != "" {
			return errors.New("invalid runner ingest URL")
		}
		s.UpstreamURL = upstream
		s.UpstreamKeyExpiresAt = expires
		s.KeyIssueRequestID = ""
		if err = e.save(ctx, l, o, s, v); err != nil {
			return err
		}
		if liveKeyUsable(s, time.Now()) {
			return nil
		}
	}
	return errors.New("runner issued an expired stream key")
}
func (e *PaidEngine) publicRTMPURL() string {
	if e.deps.Cfg.LiveExternalRTMPURL != "" {
		return strings.TrimRight(e.deps.Cfg.LiveExternalRTMPURL, "/")
	}
	u, err := url.Parse(e.deps.Cfg.GatewayPublicURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return "rtmp://" + net.JoinHostPort(u.Hostname(), fmt.Sprint(e.deps.Cfg.LiveRTMPPort)) + "/live"
}

func mustPublicJSON(v *operationView) []byte { b, _ := json.Marshal(v); return b }
func validateRuntimeURLs(b *brokerSession) error {
	for _, raw := range []string{b.Runtime.Public.HLSURL, b.Runtime.Public.KeyIssueURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
			return errors.New("invalid runtime HTTP URL")
		}
	}
	u, err := url.Parse(b.Runtime.Public.RTMPURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "rtmp" && u.Scheme != "rtmps") {
		return errors.New("invalid runtime ingest URL")
	}
	return nil
}
