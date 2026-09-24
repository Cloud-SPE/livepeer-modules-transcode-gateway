# DESIGN

A single Go gateway exposes VOD ABR transcoding and RTMP→HLS live streaming
backed by Livepeer's decentralized runner network. It includes a waitlist,
API keys, user portal and admin shell, with no customer billing in this
release.

LOC supplies discovery, routing and spend authorizations. The gateway signs
delegated caller proofs and dispatches `paid-job/v1` or `paid-session/v1`
requests to the selected broker. Runners transcode media; broker-signed
measured evidence settles the LOC ledger. Durable operations retain identity
and encrypted recovery credentials across process crashes.

| Layer | Responsibility |
|---|---|
| HTTP surface | Validate customer input and expose asynchronous jobs / live sessions |
| Workload translation | Build `video-transcode-abr/v2` and `rtmp-hls-session/v1` requests |
| Wire clients | LOC authorization, caller proof, broker protocol and settlement |
| Durable operations | Stable idempotency, dispatch recovery, terminal evidence and settlement retries |
| SaaS shell | Waitlist, email verification, admin approval, keys and sessions |
| Storage / media | Postgres state, MinIO VOD/artifacts, gateway RTMP relay to runner |

The gateway performs no encoding or muxing. VOD objects and completed ABR
artifacts use object storage; live HLS playback follows the runner runtime
descriptor. A stable public RTMP endpoint allows clients to publish through
the gateway without learning private runner ingress credentials.

Discovery metadata is authoritative: protocol, work unit, estimator,
job/session axes and price denominator are preserved. Capability names do
not imply a protocol. LOC chooses the route; the gateway does not invent a
fallback broker or retry an ambiguous dispatch under a new identity.

See [Modules v2](docs/design-docs/modules-v2.md),
[core beliefs](docs/design-docs/core-beliefs.md), and
[deployment](DEPLOYMENT.md). Design questions and follow-ups live in Beads;
[PLANS.md](PLANS.md) describes the workflow.
