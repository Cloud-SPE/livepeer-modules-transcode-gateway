package registry

import (
	"encoding/json"
	"testing"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/loc"
)

// LOC discovery serializes prices and uint64 denominators as decimal strings.
// Unknown capability names deliberately exercise metadata-driven dispatch.
const catalogFixture = `{"items":[
 {"name":"openai:chat-completions","work_unit":"tokens","offerings":[
  {"id":"vllm-default","protocol":"paid-job/v1","units_per_price":"1000","price_per_work_unit_wei":"25000000","work_unit":"tokens"}]},
 {"name":"video:transcode.live","work_unit":"output_seconds","offerings":[
  {"id":"gateway-ingest","protocol":"paid-session/v1","units_per_price":"60","price_per_work_unit_wei":"1000000000000","work_unit":"output_seconds","session":{"transport":"rtmp","descriptor_schema":"rtmp-hls/v1"}}]},
 {"name":"video:transcode.abr","work_unit":"video-frame-megapixel","offerings":[
  {"id":"abr-default","protocol":"paid-job/v1","units_per_price":"1000000","price_per_work_unit_wei":"1000","work_unit":"video-frame-megapixel","job":{"request":"http","response":"sse"},"work_unit_estimator":{"kind":"video-frame-megapixel/v1"},"extra":{"workload_schema":"video-transcode-abr/v2"}},
  {"id":"premium","protocol":"paid-job/v1","units_per_price":"18446744073709551615","price_per_work_unit_wei":"2000","work_unit":"video-frame-megapixel"}]}
]}`

func fixtureCaps(t *testing.T) []loc.Capability {
	t.Helper()
	var out struct {
		Items []loc.Capability `json:"items"`
	}
	if err := json.Unmarshal([]byte(catalogFixture), &out); err != nil {
		t.Fatal(err)
	}
	return out.Items
}

func TestBuildRowsFiltersAndPreservesV2Metadata(t *testing.T) {
	rows := buildRows(fixtureCaps(t), []string{"video:transcode.abr", "video:transcode.live"})
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	live, abr, premium := rows[0], rows[1], rows[2]
	if live.Protocol != "paid-session/v1" || live.WorkUnit != "output_seconds" || live.UnitsPerPrice.String() != "60" {
		t.Fatalf("live metadata lost: %+v", live)
	}
	if live.PricePerWorkUnitWei.String() != "1000000000000" || len(live.SessionJSON) == 0 {
		t.Fatalf("live quote/axes lost: %+v", live)
	}
	if abr.Protocol != "paid-job/v1" || abr.WorkUnit != "video-frame-megapixel" || abr.UnitsPerPrice.String() != "1000000" {
		t.Fatalf("ABR metadata lost: %+v", abr)
	}
	if len(abr.JobJSON) == 0 || len(abr.ExtraJSON) == 0 || len(abr.WorkUnitEstimatorJSON) == 0 {
		t.Fatalf("ABR opaque metadata lost: %+v", abr)
	}
	if premium.UnitsPerPrice.String() != "18446744073709551615" {
		t.Fatal("uint64 denominator lost precision")
	}
	for _, row := range rows {
		if row.InteractionMode != "" || row.BrokerURL != "" || row.EthAddress != "" {
			t.Fatalf("invented catalog identity/mode: %+v", row)
		}
	}
}

func TestBuildRowsEmptyFilterKeepsAll(t *testing.T) {
	if rows := buildRows(fixtureCaps(t), nil); len(rows) != 4 {
		t.Fatalf("got %d rows", len(rows))
	}
}

func TestBuildRowsDoesNotGuessProtocolFromCapabilityName(t *testing.T) {
	rows := buildRows([]loc.Capability{{Name: "custom:live-looking-name", Offerings: []loc.Offering{{ID: "custom", Protocol: "paid-job/v1"}}}}, nil)
	if rows[0].Protocol != "paid-job/v1" || rows[0].InteractionMode != "" || rows[0].UnitsPerPrice != nil {
		t.Fatalf("invented catalog metadata: %+v", rows[0])
	}
}
