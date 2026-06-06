package registry

import (
	"encoding/json"
	"testing"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/loc"
)

// catalogFixture mirrors the prod LOC /v1/capabilities shape observed
// 2026-06 (quoted decimal prices, multiple offerings per capability).
const catalogFixture = `{"items":[
  {"name":"openai:chat-completions","work_unit":"tokens","offerings":[
    {"id":"vllm-default","price_per_work_unit_wei":"25000000","work_unit":"tokens"}]},
  {"name":"video:transcode.live","work_unit":"output_seconds","offerings":[
    {"id":"gateway-ingest","price_per_work_unit_wei":"1000000000000","work_unit":"output_seconds"}]},
  {"name":"video:transcode.abr","work_unit":"seconds","offerings":[
    {"id":"default","price_per_work_unit_wei":"1000","work_unit":"seconds"},
    {"id":"premium","price_per_work_unit_wei":"2000","work_unit":"seconds"}]}
]}`

func fixtureCaps(t *testing.T) []loc.Capability {
	t.Helper()
	var out struct {
		Items []loc.Capability `json:"items"`
	}
	if err := json.Unmarshal([]byte(catalogFixture), &out); err != nil {
		t.Fatalf("fixture decode: %v", err)
	}
	return out.Items
}

func TestBuildRowsFiltersAndMaps(t *testing.T) {
	rows := buildRows(fixtureCaps(t), []string{"video:transcode.abr", "video:transcode.live"})
	if len(rows) != 3 { // live:gateway-ingest + abr:default + abr:premium; chat filtered out
		t.Fatalf("expected 3 rows, got %d: %+v", len(rows), rows)
	}
	byID := map[string]int{}
	for i, r := range rows {
		byID[r.CapabilityID] = i
	}
	live, ok := byID["video:transcode.live:gateway-ingest"]
	if !ok {
		t.Fatal("missing live:gateway-ingest row")
	}
	if got := rows[live].InteractionMode; got != "live-session-gateway-ingest@v0" {
		t.Errorf("live interaction mode: %s", got)
	}
	if got := rows[live].PricePerWorkUnitWei.String(); got != "1000000000000" {
		t.Errorf("live price: %s", got)
	}
	abr, ok := byID["video:transcode.abr:default"]
	if !ok {
		t.Fatal("missing abr:default row")
	}
	if got := rows[abr].InteractionMode; got != "http-reqresp@v0" {
		t.Errorf("abr interaction mode: %s", got)
	}
	// LOC's catalog carries no broker/eth identity — rows must leave
	// those blank rather than inventing values.
	if rows[abr].BrokerURL != "" || rows[abr].EthAddress != "" {
		t.Errorf("abr row should have empty broker/eth, got %q/%q",
			rows[abr].BrokerURL, rows[abr].EthAddress)
	}
}

func TestBuildRowsEmptyFilterKeepsAll(t *testing.T) {
	rows := buildRows(fixtureCaps(t), nil)
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows with no filter, got %d", len(rows))
	}
}
