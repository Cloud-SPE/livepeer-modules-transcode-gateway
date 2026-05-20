package livepeer

import (
	"context"
	"fmt"
	"math/big"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	paymentsv1 "github.com/Cloud-SPE/livepeer-network-modules/livepeer-network-protocol/proto-go/livepeer/payments/v1"
)

// PayerClient wraps the gRPC client to payment-daemon v1.3.0. Safe for
// concurrent use; the underlying connection is shared.
type PayerClient struct {
	conn *grpc.ClientConn
	cli  paymentsv1.PayerDaemonClient
}

// DialPayer dials the payer-daemon over UDS. Returns (nil, nil) when
// socketPath is empty (callers treat this as "payer disabled").
func DialPayer(ctx context.Context, socketPath string) (*PayerClient, error) {
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
		return nil, fmt.Errorf("payer: dial %s: %w", socketPath, err)
	}
	return &PayerClient{conn: conn, cli: paymentsv1.NewPayerDaemonClient(conn)}, nil
}

func (c *PayerClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// MintRequest carries everything the v1.3.0 PayerDaemon needs to mint
// a Livepeer-Payment envelope for one broker attempt.
//
// The fields mirror the route metadata the gateway already has from
// the resolver's SelectMany response: the gateway selected a route +
// quote; the payer-daemon binds the payment to that exact quote.
type MintRequest struct {
	RecipientEthAddrHex   string
	BrokerURL             string
	Capability            string
	Offering              string
	PricePerUnitWei       *big.Int
	UnitsPerPrice         uint64
	WorkUnitName          string
	QuoteID               string
	QuoteVersion          uint64
	ConstraintFingerprint []byte
	RouteFingerprint      []byte

	EstimatedUnits uint64
	FundedValueWei *big.Int
	MaxTotalUnits  uint64
	TopUpAllowed   bool
}

// MintEnvelopeResult bundles the wire-format Payment bytes with the
// daemon-assigned work_id. work_id is the hex-encoded recipient_rand_hash
// the payer-daemon bound this minted payment to; the dispatcher feeds it
// back to ReportPaymentResult when the receiver rejects the session.
type MintEnvelopeResult struct {
	PaymentBytes []byte
	WorkID       string
}

// MintEnvelope builds the v1.3.1 CreatePaymentRequest from the route
// metadata + funding intent. Returns the wire-format `Payment` bytes
// (attached to the broker request as `Livepeer-Payment`) plus the
// daemon-assigned work_id.
func (c *PayerClient) MintEnvelope(ctx context.Context, req MintRequest) (MintEnvelopeResult, error) {
	if c == nil {
		return MintEnvelopeResult{}, ErrPayerUnavailable
	}
	if req.PricePerUnitWei == nil || req.PricePerUnitWei.Sign() < 0 {
		return MintEnvelopeResult{}, fmt.Errorf("payment: price_per_unit_wei must be non-negative")
	}
	if req.FundedValueWei == nil || req.FundedValueWei.Sign() <= 0 {
		return MintEnvelopeResult{}, fmt.Errorf("payment: funded_value_wei must be positive")
	}
	recipient, err := decodeHexAddress(req.RecipientEthAddrHex)
	if err != nil {
		return MintEnvelopeResult{}, fmt.Errorf("payment: recipient: %w", err)
	}
	maxTotal := req.MaxTotalUnits
	if maxTotal == 0 {
		maxTotal = req.EstimatedUnits
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := c.cli.CreatePayment(ctx, &paymentsv1.CreatePaymentRequest{
		Recipient:           recipient,
		TicketParamsBaseUrl: req.BrokerURL,
		AcceptedPrice: &paymentsv1.AcceptedPrice{
			PricePerUnitWei: &paymentsv1.BigUInt{Value: bigBytes(req.PricePerUnitWei)},
			UnitsPerPrice:   req.UnitsPerPrice,
			WorkUnitName:    req.WorkUnitName,
			Capability:      req.Capability,
			Offering:        req.Offering,
			QuoteRef: &paymentsv1.QuoteRef{
				QuoteId:               req.QuoteID,
				QuoteVersion:          req.QuoteVersion,
				ConstraintFingerprint: req.ConstraintFingerprint,
				RouteFingerprint:      req.RouteFingerprint,
			},
		},
		Funding: &paymentsv1.FundingIntent{
			EstimatedUnits: req.EstimatedUnits,
			FundedValueWei: &paymentsv1.BigUInt{Value: bigBytes(req.FundedValueWei)},
			MaxTotalUnits:  maxTotal,
			TopUpAllowed:   req.TopUpAllowed,
		},
	})
	if err != nil {
		return MintEnvelopeResult{}, fmt.Errorf("payment: CreatePayment: %w", err)
	}
	return MintEnvelopeResult{
		PaymentBytes: resp.GetPaymentBytes(),
		WorkID:       resp.GetWorkId(),
	}, nil
}

// ReportPaymentResult tells the payer-daemon that a previously-minted
// payment was rejected by the payee with the given reason. For the
// INVALID_RECIPIENT_RAND case this evicts the cached sender session
// (so the next MintEnvelope re-fetches TicketParams against a fresh
// recipient_rand_hash). The daemon implements this RPC by returning
// Aborted + ErrorInfo when it actually evicted — we treat Aborted as
// success, since success-of-eviction is the whole point of calling.
func (c *PayerClient) ReportPaymentResult(ctx context.Context, workID, capability, offering string, reason paymentsv1.PaymentRejectionReason) error {
	if c == nil {
		return ErrPayerUnavailable
	}
	if workID == "" {
		return fmt.Errorf("payment: ReportPaymentResult: work_id required")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := c.cli.ReportPaymentResult(ctx, &paymentsv1.ReportPaymentResultRequest{
		WorkId:          workID,
		Capability:      capability,
		Offering:        offering,
		RejectionReason: reason,
	})
	if err == nil {
		return nil
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.Aborted {
		// Aborted is the v1.3.1 daemon's contract: it tells the caller
		// the session was rotated and the caller MUST retry the parent
		// CreatePayment exactly once. We've already done what this RPC
		// was for — eviction — so swallow Aborted as success.
		return nil
	}
	return fmt.Errorf("payment: ReportPaymentResult: %w", err)
}

// Health is a cheap readiness probe (used by /health).
func (c *PayerClient) Health(ctx context.Context) error {
	if c == nil {
		return ErrPayerUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	_, err := c.cli.Health(ctx, &paymentsv1.HealthRequest{})
	return err
}

func bigBytes(b *big.Int) []byte {
	if b == nil || b.Sign() == 0 {
		return nil
	}
	return b.Bytes()
}

func decodeHexAddress(s string) ([]byte, error) {
	if len(s) >= 2 && (s[0] == '0' && (s[1] == 'x' || s[1] == 'X')) {
		s = s[2:]
	}
	if len(s) != 40 {
		return nil, fmt.Errorf("hex address must be 20 bytes (40 hex chars)")
	}
	out := make([]byte, 20)
	for i := 0; i < 20; i++ {
		hi, err1 := fromHex(s[i*2])
		lo, err2 := fromHex(s[i*2+1])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("invalid hex byte at position %d", i*2)
		}
		out[i] = hi<<4 | lo
	}
	return out, nil
}

func fromHex(b byte) (byte, error) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', nil
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, nil
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, nil
	}
	return 0, fmt.Errorf("not hex: %q", b)
}
