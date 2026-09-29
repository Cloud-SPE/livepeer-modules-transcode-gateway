package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/crypto"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/livepeer"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/loc"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"
	"github.com/google/uuid"
)

type sharedWalletFixture struct {
	Name          string         `json:"name"`
	Kind          string         `json:"kind"`
	Account       string         `json:"account"`
	WorkID        string         `json:"work_id"`
	RequestID     string         `json:"request_id"`
	Unit          string         `json:"unit"`
	Authorization string         `json:"authorization"`
	CallerProof   string         `json:"caller_proof"`
	Settlement    map[string]any `json:"settlement"`
}

// The checked-in corpus is signed with public test keys using LOC protobufs.
// scripts/shared-wallet-conformance.py also verifies these exact post-restart
// envelopes with LOC's production verifier and rejects another account.
func TestSharedWalletV3JournalAndWire(t *testing.T) {
	raw, err := os.ReadFile("testdata/shared-wallet-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []sharedWalletFixture
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	newEngine := func() *PaidEngine {
		box, err := crypto.NewSecretBox(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
		if err != nil {
			t.Fatal(err)
		}
		signer, err := livepeer.NewCallerSigner(strings.Repeat("0", 63) + "1")
		if err != nil {
			t.Fatal(err)
		}
		return &PaidEngine{box: box, signer: signer}
	}
	var results []sharedWalletFixture
	for _, fixture := range cases {
		t.Run(fixture.Name, func(t *testing.T) {
			e := newEngine()
			s := &operationSecrets{CallerPublicKey: e.signer.PublicKey(), Capability: "video:transcode.live", Offering: "test"}
			if fixture.Kind == "job" {
				s.Capability = "video:transcode.abr"
			}
			sessionAuthorization := fixture.Authorization
			sessionWorkID := fixture.WorkID
			if fixture.Kind == "refill" || fixture.Kind == "refill_refused" {
				for _, prior := range cases {
					if prior.Account == fixture.Account && prior.Kind == "session" {
						sessionAuthorization = prior.Authorization
						sessionWorkID = prior.WorkID
					}
				}
			}
			s.Job = &loc.CreateJobResponseV2{SpendAuthorization: fixture.Authorization}
			s.Session = &loc.CreateSessionResponseV2{SpendAuthorization: sessionAuthorization, WorkID: sessionWorkID}
			s.Preparation = &loc.PrepareSessionResponseV2{GatewaySessionID: uuid.MustParse("11111111-1111-4111-8111-111111111111")}
			s.Broker = &brokerSession{SessionID: "broker-session-1"}
			s.Refill = &loc.RefillSessionResponseV2{SpendAuthorization: fixture.Authorization, WorkID: fixture.WorkID}
			o := &repo.PaidOperation{ID: uuid.New()}
			if err := e.seal(o, s, &operationView{}); err != nil {
				t.Fatal(err)
			}
			e = newEngine() // Recover with only the external keys and encrypted journal.
			s, _, err = e.decode(o)
			if err != nil {
				t.Fatal(err)
			}
			if s.Job.SpendAuthorization != fixture.Authorization || s.Session.SpendAuthorization != sessionAuthorization || s.Refill.SpendAuthorization != fixture.Authorization {
				t.Fatal("journal changed authorization bytes")
			}
			auth, err := e.auth(s, fixture.RequestID, fixture.Authorization, fixture.WorkID)
			if err != nil {
				t.Fatal(err)
			}
			if auth.CallerProof != fixture.CallerProof {
				t.Fatal("caller proof differs from independent Ethereum signer")
			}
			wire, _ := json.Marshal(fixture.Settlement)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					if r.Header.Get(livepeer.HeaderAuthorization) != fixture.Authorization || r.Header.Get(livepeer.HeaderCallerProof) != fixture.CallerProof {
						t.Error("wire mutated authorization or caller proof")
					}
					if fixture.Kind == "job" {
						w.Header().Set("Content-Type", "text/event-stream")
						w.Header().Set("Livepeer-Job-Id", "broker-job")
						w.Header().Set(livepeer.HeaderSettlement, base64.StdEncoding.EncodeToString(wire))
						return
					}
					if fixture.Kind == "refill_refused" {
						fixtureError(w, 409, "refill_refused")
						return
					}
					fixtureJSON(w, map[string]any{"session_id": "broker-session-1", "credential": "test-credential", "work_id": fixture.WorkID, "runtime": map[string]any{"schema": "rtmp-hls/v1"}})
					return
				}
				w.Header().Set(livepeer.HeaderSettlement, base64.StdEncoding.EncodeToString(wire))
				fixtureJSON(w, map[string]any{})
			}))
			defer upstream.Close()
			client := livepeer.NewHTTPClient(time.Second)
			var claim *livepeer.TerminalClaim
			if fixture.Kind == "job" {
				claim, err = client.SubmitJobV2(context.Background(), upstream.URL, auth, []byte(`{}`), fixture.Unit, func(livepeer.SSEEvent) error { return nil })
			} else {
				if fixture.Kind == "refill" || fixture.Kind == "refill_refused" {
					_, err = client.TopUpSessionV2(context.Background(), upstream.URL, "broker-session-1", "test-credential", auth, []byte(`{}`))
				} else {
					_, err = client.OpenSessionV2(context.Background(), upstream.URL, auth, []byte(`{}`))
				}
				if fixture.Kind == "refill_refused" {
					if !livepeer.IsRefillRefusedError(err) {
						t.Fatalf("expected definitive refusal: %v", err)
					}
					s.Session.WorkID = fixture.Account + "-session"
					s.Session.BrokerURL = upstream.URL
					e.deps.HTTP = client
					claim, err = e.lookupLiveClaim(context.Background(), s)
				} else {
					if err != nil {
						t.Fatal(err)
					}
					claim, err = client.LookupSessionSettlementV2(context.Background(), upstream.URL, "broker-session-1", fixture.WorkID, fixture.Unit)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			s.Claim = claim
			if fixture.Kind == "refill_refused" {
				s.RefillRefused = true
			}
			if err = e.seal(o, s, &operationView{}); err != nil {
				t.Fatal(err)
			}
			e = newEngine()
			s, _, err = e.decode(o)
			if err != nil {
				t.Fatal(err)
			}
			if fixture.Kind == "refill_refused" && (!s.RefillRefused || s.Session.WorkID != fixture.Account+"-session" || s.Refill.WorkID != fixture.WorkID) {
				t.Fatal("lost refused successor or predecessor on restart")
			}
			var forwardBody any = loc.CloseSessionRequestV2{ActualUnits: s.Claim.ActualUnits, Settlement: s.Claim.Settlement}
			if fixture.Kind == "job" {
				forwardBody = loc.SettleJobRequestV2{ActualUnits: s.Claim.ActualUnits, BrokerJobID: s.Claim.BrokerJobID, WorkUnit: s.Claim.WorkUnit, Settlement: s.Claim.Settlement}
			}
			forwarded, err := json.Marshal(forwardBody)
			if err != nil {
				t.Fatal(err)
			}
			var request loc.CloseSessionRequestV2
			if err = json.Unmarshal(forwarded, &request); err != nil {
				t.Fatal(err)
			}
			roundtrip, _ := json.Marshal(request.Settlement)
			if !bytes.Equal(wire, roundtrip) {
				t.Fatal("signed envelope changed across wire, encrypted recovery and LOC request")
			}
			fixture.Settlement = request.Settlement
			fixture.Authorization = s.Refill.SpendAuthorization
			fixture.CallerProof = auth.CallerProof
			results = append(results, fixture)
		})
	}
	if output := os.Getenv("SHARED_WALLET_ROUNDTRIP"); output != "" {
		raw, err := json.Marshal(results)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(output, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
