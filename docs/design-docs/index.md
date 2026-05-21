# design-docs

The system-of-record for architectural decisions. Each doc here is a
short, focused write-up of a single design concern. Cross-link
liberally.

| Doc | What it covers | Status |
|---|---|---|
| [`core-beliefs.md`](./core-beliefs.md) | The non-negotiable invariants — anything that violates these is a bug. | live |
| [`boot-sequence.md`](./boot-sequence.md) | What the gateway binary does between `main()` and "listening on :4000". | live |
| [`payment-flow.md`](./payment-flow.md) | How `Livepeer-Payment` envelopes get minted, both for VOD attempts and live sessions. | live |
| [`route-selector.md`](./route-selector.md) | How the gateway turns a capability request into a ranked broker list. | live |
| [`abr-pipeline.md`](./abr-pipeline.md) | The `/api/v1/abr` flow end-to-end: upload → presign → dispatch → poll. | live |
| [`live-stream-pipeline.md`](./live-stream-pipeline.md) | The `/api/v1/live` flow end-to-end: allocate → RTMP push → playback → teardown. | live |
| [`capability-catalog.md`](./capability-catalog.md) | How the registry-backed `capabilities` table stays fresh + what `/api/v1/capabilities` returns. | live |

Status meanings: **live** = reflects current code; **draft** = aspirational;
**superseded** = kept for history, see "Replaces" header.
