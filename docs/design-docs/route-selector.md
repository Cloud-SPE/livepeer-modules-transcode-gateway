# Route selector

How the gateway turns "user wants an ABR transcode" into a ranked
broker list.

## Shape

```go
type RouteSelector interface {
    SelectMany(ctx context.Context, req SelectRequest) ([]Candidate, error)
}

type SelectRequest struct {
    Capability string   // e.g. "livepeer:transcode/abr-ladder"
    Offering   string   // e.g. "default" or a custom ladder id
    Tier       string   // optional
    MinWeight  int32    // optional
}

type Candidate struct {
    WorkerURL          string
    EthAddress         string
    Capability         string
    Offering           string
    PricePerUnitWei    *big.Int
    WorkUnit           string
    Extra              json.RawMessage
    Constraints        json.RawMessage
    QuoteID            string
    QuoteVersion       uint64
    RouteFingerprint   []byte
    ConstraintFingerprint []byte
    UnitsPerPrice      uint64
}
```

## How candidates rank

The resolver (`service-registry-daemon`) already orders by weight,
freshness, and signature status. The gateway accepts that order and
adds:

- **Route health.** A candidate that's in cooldown (recent 5xx /
  timeout streak) is deprioritized — pushed to the back of the queue.
- **Constraint match.** If a request specifies extras the candidate
  doesn't advertise (e.g. AV1 output when the candidate only does
  H.264), it's filtered out before the ranking step.

## Failover loop

`gateway/internal/proxy/service/route_dispatch.go` iterates
candidates:

```
for cand in candidates:
    health.beginAttempt(cand)
    payment = mint(cand)
    res, err = broker.dispatch(cand, payment, body)
    if isRetryable(err):
        health.recordFailure(cand, err)
        continue
    return res, err
return last_err
```

`isRetryable` returns true for connection errors, 5xx, and 429.
4xx (other than 429) is returned to the client verbatim.

## Why not just use the first candidate

The resolver's order is *prediction*, not *reality*. A broker can go
down between a refresh cycle and a request. Failover gives us
single-digit-percent error rates on top of supply that fluctuates.

## Open questions

- **Live route selection on re-allocation.** `/api/v1/live` does not
  failover within a session; a future plan should explore whether
  a "preferred broker" hint can let clients re-allocate against the
  same orchestrator when ingest drops.
- **Quote-aware ABR ladder pricing.** Today face value is estimated
  from input duration. A future plan should let the runner respond
  with `units_per_price` so subsequent attempts mint exactly the
  right value.
