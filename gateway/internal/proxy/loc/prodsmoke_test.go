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
