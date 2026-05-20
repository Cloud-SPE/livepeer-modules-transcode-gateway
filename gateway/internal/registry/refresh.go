package registry

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/proxy/service"
	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"

	registryv1 "github.com/Cloud-SPE/livepeer-network-modules/proto-contracts/livepeer/registry/v1"
)

// Refresher periodically syncs the capabilities table from the
// resolver daemon.
type Refresher struct {
	resolver *service.RouteSelector
	repo     *repo.CapabilityRepo
	interval time.Duration
	filter   []string // capability names we care about (empty = all)
	log      *slog.Logger
}

func NewRefresher(
	r *service.RouteSelector,
	c *repo.CapabilityRepo,
	interval time.Duration,
	filterCapabilities []string,
	log *slog.Logger,
) *Refresher {
	return &Refresher{resolver: r, repo: c, interval: interval, filter: filterCapabilities, log: log}
}

// Start runs the refresh loop until ctx is canceled. The first tick
// runs synchronously so /v1/capabilities is non-empty by the time we
// start serving traffic.
func (rf *Refresher) Start(ctx context.Context) {
	if rf.resolver == nil {
		rf.log.Warn("registry refresh: resolver not configured; capabilities table stays empty")
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
	known, err := rf.resolver.ListAll(ctx)
	if err != nil {
		_ = rf.repo.RecordRefresh(ctx, "error", err.Error(), 0, rf.filter)
		return fmt.Errorf("list known: %w", err)
	}
	var rows []repo.UpsertCapability
	for _, entry := range known.GetEntries() {
		res, err := rf.resolver.ResolveByAddress(ctx, entry.GetEthAddress())
		if err != nil {
			rf.log.Debug("resolve by address failed", "addr", entry.GetEthAddress(), "err", err)
			continue
		}
		for _, node := range res.GetNodes() {
			for _, capability := range node.GetCapabilities() {
				if !rf.matchesFilter(capability.GetName()) {
					continue
				}
				for _, offering := range capability.GetOfferings() {
					price := parseBig(offering.GetPricePerWorkUnitWei())
					// Some orchs publish a worker eth address; others
					// only register the operator address. Fall back to
					// operator_address so the catalog isn't blank.
					ethAddr := node.GetWorkerEthAddress()
					if ethAddr == "" {
						ethAddr = node.GetOperatorAddress()
					}
					rows = append(rows, repo.UpsertCapability{
						CapabilityID:        capability.GetName() + ":" + offering.GetId(),
						Capability:          capability.GetName(),
						Offering:            offering.GetId(),
						InteractionMode:     guessInteractionMode(capability.GetName()),
						Name:                capability.GetName(),
						Provider:            node.GetOperatorAddress(),
						Category:            "transcode",
						EthAddress:          ethAddr,
						PricePerWorkUnitWei: price,
						BrokerURL:           node.GetUrl(),
						ExtraJSON:           capability.GetExtraJson(),
						ConstraintsJSON:     offering.GetConstraintsJson(),
					})
				}
			}
		}
	}
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

func (rf *Refresher) matchesFilter(capability string) bool {
	if len(rf.filter) == 0 {
		return true
	}
	for _, c := range rf.filter {
		if c == capability {
			return true
		}
	}
	return false
}

func parseBig(s string) *big.Int {
	if s == "" {
		return nil
	}
	b := new(big.Int)
	if _, ok := b.SetString(s, 10); ok {
		return b
	}
	return nil
}

// guessInteractionMode returns the canonical mode for a known capability
// name. Unknown capabilities fall back to http-reqresp.
//
// Used for catalog metadata only — the gateway dispatches via the
// CapMap (livepeer.NewDefault), which is the actual source of truth
// for the Livepeer-Mode header. Keeping this heuristic aligned with
// the CapMap defaults avoids confusing the admin Registry view.
func guessInteractionMode(capability string) string {
	switch {
	case isLive(capability):
		return "live-session-remote-runner@v0"
	default:
		return "http-reqresp@v0"
	}
}

func isLive(capability string) bool {
	for i := 0; i+4 <= len(capability); i++ {
		if capability[i:i+4] == "live" {
			return true
		}
	}
	return false
}

var _ = registryv1.ResolveMode_RESOLVE_MODE_UNSPECIFIED // keep import live across edits
