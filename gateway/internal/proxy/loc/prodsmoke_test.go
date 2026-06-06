package loc

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestProdCatalogSmoke is a manual smoke test against a real LOC.
// Skipped unless LOC_SMOKE_API_KEY is set.
func TestProdCatalogSmoke(t *testing.T) {
	key := os.Getenv("LOC_SMOKE_API_KEY")
	if key == "" {
		t.Skip("LOC_SMOKE_API_KEY not set")
	}
	c := NewClient("https://loc.cloudspe.com", key, "transcode-gateway/0.1.0/dev", 15*time.Second)
	caps, err := c.ListCapabilities(context.Background())
	if err != nil {
		t.Fatalf("ListCapabilities: %v", err)
	}
	for _, cap := range caps {
		for _, o := range cap.Offerings {
			fmt.Printf("%s:%s price=%s wei/%s\n", cap.Name, o.ID, o.PricePerWorkUnitWei.String(), o.WorkUnit)
		}
	}
}

// TestProdSessionRoundtripSmoke opens a minimal LOC session and
// immediately closes it with 0 units (full refund) — exercises route
// selection, envelope minting, and close settlement against a real
// LOC without dispatching to a broker. Skipped unless
// LOC_SMOKE_API_KEY is set.
func TestProdSessionRoundtripSmoke(t *testing.T) {
	key := os.Getenv("LOC_SMOKE_API_KEY")
	if key == "" {
		t.Skip("LOC_SMOKE_API_KEY not set")
	}
	c := NewClient("https://loc.cloudspe.com", key, "transcode-gateway/0.1.0/dev", 15*time.Second)
	ctx := context.Background()
	sess, err := c.OpenSession(ctx, CreateSessionRequest{
		Capability:           "video:transcode.live",
		Offering:             "gateway-ingest",
		EstimatedRunwayUnits: 1,
		MaxTotalUnits:        1,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	fmt.Printf("opened session=%s work_id=%s broker=%s mode=%s funded=%s wei expected=%s wei\n",
		sess.SessionID, sess.WorkID, sess.BrokerURL, sess.Mode,
		sess.FundedValueWei.String(), sess.ExpectedValueWei.String())
	if pb, perr := sess.PaymentBytes(); perr != nil || len(pb) == 0 {
		t.Errorf("payment envelope decode: len=%d err=%v", len(pb), perr)
	}
	closed, err := c.CloseSession(ctx, sess.SessionID, CloseSessionRequest{
		ActualUnits: 0, Outcome: "smoke_test",
	})
	if err != nil {
		t.Fatalf("CloseSession: %v (session %s may stay encumbered)", err, sess.SessionID)
	}
	fmt.Printf("closed: billed=%s wei refund=%s wei outcome=%s\n",
		closed.BilledValueWei.String(), closed.RefundWei.String(), closed.Outcome)
}
