package livepeer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

func signedFixture(t *testing.T, payload map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"payload": payload, "signature": map[string]any{"algorithm": "secp256k1", "canonicalization": "jcs", "value": "0x" + strings.Repeat("ab", 65)}})
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}
func jobFixture(t *testing.T) string {
	return signedFixture(t, map[string]any{"job_id": "job-1", "request_id": "request-1", "authorization_id": "auth-1", "work_unit_name": "video-frame-megapixel", "actual_units": "9007199254740993"})
}
func testAuth() BrokerAuth {
	return BrokerAuth{Capability: "test-capability", Offering: "test-offering", RequestID: "request-1", Authorization: "authorization", CallerProof: "proof", WorkID: "auth-1"}
}
func TestCallerProofRecoverAndScope(t *testing.T) {
	signer, err := NewCallerSigner(strings.Repeat("0", 63) + "1")
	if err != nil {
		t.Fatal(err)
	}
	if signer.PublicKey() != "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798" {
		t.Fatalf("unexpected compressed key %s", signer.PublicKey())
	}
	authorization := []byte("single-purpose-authorization")
	encoded, err := signer.Proof(base64.StdEncoding.EncodeToString(authorization))
	if err != nil {
		t.Fatal(err)
	}
	// Cross-implementation vector generated with the broker's go-ethereum
	// crypto.Sign path, rather than this client's signing implementation.
	const brokerVector = "fCTSlK2fRyhL8ckc7pqP2vEMxxzTgr6GviwGm44En+IMuvL0JrzBk6+DSfq0li+ZMmxhFcQl3eVQhywHcTjRPRs="
	if encoded != brokerVector {
		t.Fatalf("caller proof differs from broker vector: %s", encoded)
	}
	sig, _ := base64.StdEncoding.DecodeString(encoded)
	if len(sig) != 65 || (sig[64] != 27 && sig[64] != 28) {
		t.Fatal("noncanonical recovery signature")
	}
	compact := append([]byte{sig[64]}, sig[:64]...)
	digest := keccak(append([]byte("\x19Ethereum Signed Message:\n32"), keccak(append([]byte("livepeer-invocation-proof/v1\x00"), authorization...))...))
	recovered, _, err := ecdsa.RecoverCompact(compact, digest)
	if err != nil || hex.EncodeToString(recovered.SerializeCompressed()) != signer.PublicKey() {
		t.Fatalf("caller proof does not recover: %v", err)
	}
	altered, _ := signer.Proof(base64.StdEncoding.EncodeToString(append(authorization, 'x')))
	if altered == encoded {
		t.Fatal("proof not bound to authorization")
	}
	for _, bad := range []string{"", strings.Repeat("0", 64), strings.Repeat("f", 64)} {
		if _, err := NewCallerSigner(bad); err == nil {
			t.Fatal("accepted invalid private key")
		}
	}
}
func TestSubmitStreamExactBytesAndSignedTrailer(t *testing.T) {
	body := []byte("{ \"scope\": 1 }\n")
	settlement := jobFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/job" {
			t.Errorf("wrong path %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if !bytes.Equal(b, body) {
			t.Error("mutated committed request body")
		}
		if r.Header.Get(HeaderAuthorization) != "authorization" || r.Header.Get(HeaderCallerProof) != "proof" || r.Header.Get(HeaderProtocol) != ProtocolJobV1 || r.Header.Get("Livepeer-Mode") != "" || r.Header.Get("Livepeer-Payment") != "" {
			t.Error("incorrect authorization headers")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Livepeer-Job-Id", "job-1")
		w.Header().Set("Trailer", HeaderSettlement)
		fmt.Fprint(w, "id: 1\nevent: progress\ndata: {\"progress\":1}\n\nid: 2\nevent: result\ndata: {\"status\":\"completed\"}\n\n")
		w.Header().Set(HeaderSettlement, settlement)
	}))
	defer server.Close()
	var events []SSEEvent
	claim, err := NewHTTPClient(time.Second).SubmitJobV2(context.Background(), server.URL, testAuth(), body, "video-frame-megapixel", func(e SSEEvent) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if claim.ActualUnits != 9007199254740993 || len(events) != 2 || events[1].Event != "result" {
		t.Fatalf("bad stream result %#v %#v", claim, events)
	}
}
func TestMissingTrailerRecoversByRequestID(t *testing.T) {
	settlement := jobFixture(t)
	lookup := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/job" {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: result\ndata: {}\n\n")
			return
		}
		if r.URL.Path != "/v1/exchange/request-1" {
			t.Errorf("unexpected lookup %s", r.URL.Path)
		}
		lookup = true
		json.NewEncoder(w).Encode(map[string]any{"request_id": "request-1", "job_id": "job-1", "outcome": "SETTLED", "unit": "video-frame-megapixel", "work_units": "9007199254740993", "settlement": settlement})
	}))
	defer server.Close()
	claim, err := NewHTTPClient(time.Second).SubmitJobV2(context.Background(), server.URL, testAuth(), []byte("{}"), "video-frame-megapixel", nil)
	if err != nil || !lookup || claim == nil {
		t.Fatalf("recovery failed: %v", err)
	}
}
func TestLookupNeverSettlesUnknownOrUncorrelatedEvidence(t *testing.T) {
	for _, scenario := range []string{"pending", "not-admitted", "wrong-job", "wrong-request", "unsigned"} {
		t.Run(scenario, func(t *testing.T) {
			payload := map[string]any{"job_id": "job-1", "request_id": "request-1", "authorization_id": "auth-1", "work_unit_name": "units", "actual_units": "4"}
			if scenario == "wrong-job" {
				payload["job_id"] = "other"
			}
			if scenario == "wrong-request" {
				payload["request_id"] = "other"
			}
			settlement := signedFixture(t, payload)
			if scenario == "unsigned" {
				b, _ := json.Marshal(map[string]any{"payload": payload})
				settlement = base64.StdEncoding.EncodeToString(b)
			}
			outcome := "SETTLED"
			if scenario == "pending" {
				outcome = "ACCOUNTING_PENDING"
			}
			if scenario == "not-admitted" {
				outcome = "NOT_ADMITTED"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"request_id": "request-1", "job_id": "job-1", "unit": "units", "work_units": 4, "outcome": outcome, "settlement": settlement})
			}))
			defer server.Close()
			claim, err := NewHTTPClient(time.Second).LookupJobV2(context.Background(), server.URL, "request-1", "job-1", "units", "auth-1")
			if err == nil || claim != nil {
				t.Fatalf("accepted %s", scenario)
			}
			if (scenario == "pending" || scenario == "not-admitted") && !errors.Is(err, ErrAccountingPending) {
				t.Errorf("must retain unresolved encumbrance: %v", err)
			}
		})
	}
}
func TestSessionCredentialAndTerminalSettlement(t *testing.T) {
	closed := false
	claim := signedFixture(t, map[string]any{"session_id": "session-1", "gateway_session_id": "gateway-1", "work_id": "auth-1", "work_unit_name": "output_seconds", "state": "closed", "debited_units": "42"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/settlement/") {
			if !closed {
				w.Header().Set(HeaderSettlement, signedFixture(t, map[string]any{"session_id": "session-1", "gateway_session_id": "gateway-1", "work_id": "auth-1", "work_unit_name": "output_seconds", "state": "open"}))
			} else {
				w.Header().Set(HeaderSettlement, claim)
			}
			fmt.Fprint(w, "{}")
			return
		}
		if r.Header.Get("Authorization") != "Bearer credential" {
			t.Error("session credential omitted")
		}
		if strings.HasSuffix(r.URL.Path, "/end") {
			closed = true
		}
		fmt.Fprint(w, `{"session_id":"session-1","state":"closed"}`)
	}))
	defer server.Close()
	client := NewHTTPClient(time.Second)
	if _, err := client.StatusSessionV2(context.Background(), server.URL, "session-1", ""); err == nil {
		t.Fatal("accepted empty credential")
	}
	if _, err := client.StatusSessionV2(context.Background(), server.URL, "session-1", "credential"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.LookupSessionSettlementV2(context.Background(), server.URL, "gateway-1", "auth-1", "output_seconds"); !errors.Is(err, ErrAccountingPending) {
		t.Fatalf("interim settlement accepted %v", err)
	}
	if _, err := client.EndSessionV2(context.Background(), server.URL, "session-1", "credential", "gateway_close"); err != nil {
		t.Fatal(err)
	}
	out, err := client.LookupSessionSettlementV2(context.Background(), server.URL, "gateway-1", "auth-1", "output_seconds")
	if err != nil || out.ActualUnits != 42 {
		t.Fatalf("terminal settlement %v %v", out, err)
	}
}

func TestScopedInvocationDoesNotFollowRedirect(t *testing.T) {
	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(http.StatusOK) }))
	defer target.Close()
	route := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer route.Close()
	_, err := NewHTTPClient(time.Second).SubmitJobV2(context.Background(), route.URL, testAuth(), []byte("{}"), "units", nil)
	if err == nil || reached {
		t.Fatal("scope-bound invocation followed route redirect")
	}
}

func TestRuntimeGrantEndpointAndCredentialIsolation(t *testing.T) {
	client := NewHTTPClient(time.Second)
	for _, bad := range []string{"ftp://runner/key", "http://user:pass@runner/key", "//runner/key", "http://runner/key#fragment", "http:///key"} {
		if err := client.IssueRuntimeKeyV2(context.Background(), bad, "grant", []byte("{}"), nil); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer grant" || r.Header.Get("X-API-Key") != "" || r.Header.Get(HeaderAuthorization) != "" || r.Header.Get(HeaderCallerProof) != "" {
			t.Error("runtime grant forwarded unrelated credentials")
		}
		fmt.Fprint(w, `{"request_id":"key-1"}`)
	}))
	defer server.Close()
	var out map[string]string
	if err := client.IssueRuntimeKeyV2(context.Background(), server.URL, "grant", []byte(`{"request_id":"key-1"}`), &out); err != nil || out["request_id"] != "key-1" {
		t.Fatalf("grant exchange failed: %v", err)
	}
}
