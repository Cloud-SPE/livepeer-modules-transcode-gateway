package registry

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/loc"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"
)

// Refresher periodically syncs the capabilities table from LOC's
// discovery catalog (GET /v1/capabilities).
//
// LOC's catalog is intentionally narrower than the old resolver feed:
// it carries capability + offering + price + work unit, but not
// eth_address / broker_url / constraints — LOC owns route selection
// now, so advertising a specific broker in the gateway's catalog would
// be misleading. Those columns stay NULL for LOC-era rows.
type Refresher struct {
	loc      *loc.Client
	repo     *repo.CapabilityRepo
	interval time.Duration
	filter   []string // capability names we care about (empty = all)
	log      *slog.Logger
}

func NewRefresher(
	c *loc.Client,
	caps *repo.CapabilityRepo,
	interval time.Duration,
	filterCapabilities []string,
	log *slog.Logger,
) *Refresher {
	return &Refresher{loc: c, repo: caps, interval: interval, filter: filterCapabilities, log: log}
}

// Start runs the refresh loop until ctx is canceled. The first tick
// runs synchronously so /v1/capabilities is non-empty by the time we
// start serving traffic.
func (rf *Refresher) Start(ctx context.Context) {
	if rf.loc == nil {
		rf.log.Warn("registry refresh: LOC not configured; capabilities table stays empty")
		return
	}
	if err := rf.Once(ctx); err != nil {
		rf.log.Warn("registry refresh: initial tick failed", "err", err)
	}
	t := time.NewTicker(rf.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := rf.Once(ctx); err != nil {
				rf.log.Warn("registry refresh: tick failed", "err", err)
			}
		}
	}
}

// Once executes a single refresh cycle.
func (rf *Refresher) Once(ctx context.Context) error {
	caps, err := rf.loc.ListCapabilities(ctx)
	if err != nil {
		_ = rf.repo.RecordRefresh(ctx, "error", err.Error(), 0, rf.filter)
		return fmt.Errorf("loc list capabilities: %w", err)
	}
	rows := buildRows(caps, rf.filter)
	if err := rf.repo.ReplaceSnapshot(ctx, rows); err != nil {
		_ = rf.repo.RecordRefresh(ctx, "error", err.Error(), len(rows), rf.filter)
		return fmt.Errorf("upsert snapshot: %w", err)
	}
	if err := rf.repo.RecordRefresh(ctx, "ok", "", len(rows), rf.filter); err != nil {
		rf.log.Warn("registry refresh: meta update failed", "err", err)
	}
	rf.log.Debug("registry refresh: snapshot updated", "rows", len(rows), "filter", rf.filter)
	return nil
}

// buildRows maps LOC's catalog onto capability-table rows, applying the
// capability-name filter. Pure — split out for testability.
func buildRows(caps []loc.Capability, filter []string) []repo.UpsertCapability {
	var rows []repo.UpsertCapability
	for _, capability := range caps {
		if !matchesFilter(capability.Name, filter) {
			continue
		}
		for _, offering := range capability.Offerings {
			rows = append(rows, repo.UpsertCapability{
				CapabilityID:          capability.Name + ":" + offering.ID,
				Capability:            capability.Name,
				Offering:              offering.ID,
				Protocol:              offering.Protocol,
				WorkUnit:              offering.WorkUnit,
				UnitsPerPrice:         offering.UnitsPerPrice.BigInt(),
				WorkUnitEstimatorJSON: offering.WorkUnitEstimator,
				JobJSON:               offering.Job,
				SessionJSON:           offering.Session,
				ExtraJSON:             offering.Extra,
				Name:                  capability.Name,
				Category:              "transcode",
				PricePerWorkUnitWei:   offering.PricePerWorkUnitWei.BigInt(),
			})
		}
	}
	return rows
}

func matchesFilter(capability string, filter []string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, c := range filter {
		if c == capability {
			return true
		}
	}
	return false
}
