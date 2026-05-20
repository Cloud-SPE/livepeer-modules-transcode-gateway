package service

import (
	"context"
	"fmt"
	"math/big"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	registryv1 "github.com/Cloud-SPE/livepeer-network-modules/proto-contracts/livepeer/registry/v1"
)

// Candidate is a single broker option for a request, rich with
// price + metadata.
type Candidate struct {
	WorkerURL             string
	EthAddress            string
	Capability            string
	Offering              string
	PricePerWorkUnitWei   *big.Int
	WorkUnit              string
	Extra                 []byte
	Constraints           []byte
	QuoteID               string
	QuoteVersion          uint64
	ConstraintFingerprint []byte
	RouteFingerprint      []byte
	UnitsPerPrice         uint64
}

// SelectRequest is the lookup intent the gateway forms per HTTP request.
type SelectRequest struct {
	Capability string
	Offering   string
	Tier       string
	MinWeight  int32
}

// RouteSelector talks to service-registry-daemon over UDS.
type RouteSelector struct {
	conn *grpc.ClientConn
	cli  registryv1.ResolverClient
}

func DialResolver(ctx context.Context, socketPath string) (*RouteSelector, error) {
	if socketPath == "" {
		return nil, nil
	}
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socketPath)
	}
	conn, err := grpc.NewClient("passthrough:///"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		return nil, fmt.Errorf("resolver: dial %s: %w", socketPath, err)
	}
	return &RouteSelector{conn: conn, cli: registryv1.NewResolverClient(conn)}, nil
}

func (s *RouteSelector) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

// SelectMany returns ranked candidates for the request.
func (s *RouteSelector) SelectMany(ctx context.Context, req SelectRequest) ([]Candidate, error) {
	if s == nil {
		return nil, fmt.Errorf("resolver not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resp, err := s.cli.SelectMany(ctx, &registryv1.SelectRequest{
		Capability: req.Capability,
		Offering:   req.Offering,
		Tier:       req.Tier,
		MinWeight:  req.MinWeight,
	})
	if err != nil {
		return nil, fmt.Errorf("resolver: SelectMany: %w", err)
	}
	out := make([]Candidate, 0, len(resp.GetRoutes()))
	for _, r := range resp.GetRoutes() {
		c := Candidate{
			WorkerURL:             r.GetWorkerUrl(),
			EthAddress:            r.GetEthAddress(),
			Capability:            r.GetCapability(),
			Offering:              r.GetOffering(),
			WorkUnit:              r.GetWorkUnit(),
			Extra:                 r.GetExtraJson(),
			Constraints:           r.GetConstraintsJson(),
			QuoteID:               r.GetQuoteId(),
			QuoteVersion:          r.GetQuoteVersion(),
			ConstraintFingerprint: r.GetConstraintFingerprint(),
			RouteFingerprint:      r.GetRouteFingerprint(),
			UnitsPerPrice:         r.GetUnitsPerPrice(),
		}
		if p := r.GetPricePerWorkUnitWei(); p != "" {
			b := new(big.Int)
			if _, ok := b.SetString(p, 10); ok {
				c.PricePerWorkUnitWei = b
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// ListAll returns all currently-known nodes (used by the catalog refresh).
func (s *RouteSelector) ListAll(ctx context.Context) (*registryv1.ListKnownResult, error) {
	if s == nil {
		return nil, fmt.Errorf("resolver not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.cli.ListKnown(ctx, &registryv1.ListKnownRequest{})
}

// ResolveByAddress fetches the live capability set for a single orch.
func (s *RouteSelector) ResolveByAddress(ctx context.Context, ethAddress string) (*registryv1.ResolveResult, error) {
	if s == nil {
		return nil, fmt.Errorf("resolver not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.cli.ResolveByAddress(ctx, &registryv1.ResolveByAddressRequest{
		EthAddress: ethAddress,
	})
}

// Health is the readiness probe for /health.
func (s *RouteSelector) Health(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("resolver not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	_, err := s.cli.Health(ctx, &emptyMsg{})
	return err
}
