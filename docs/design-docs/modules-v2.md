# Modules v2 compatibility boundary

The migration is tracked by Beads epic `vgw-av1`; tracking adoption and
catalog preservation are recorded in `vgw-b25`. Beads owns work status.
This document records the compatibility decisions and source context.

The source audit used gateway `3856089`, LOC `0eabff5`, network modules
`08f5985`, and newer transcode implementations `18eaafa` from the separate
`livepeer-modules-transcode` checkout. The maintained runner target is the
user's `livepeer-modules-transcode-runners` repository, upgraded to the same
v2 interfaces. These are audited source references, not assertions that a
particular remote deployment has those revisions.

The gateway remains a Go application with its existing customer API and
SaaS shell. The wire layer changes to LOC spend authorizations, persistent
delegated caller identity, `paid-job/v1` and `paid-session/v1`. ABR uses the
v2 workload and SSE terminal flow. Live uses a prepared session, runtime
descriptor and stream-key grant. Live metering is finalized output seconds;
ABR metering is video-frame-megapixel. Broker-signed settlement evidence is
the authority for billed usage.

A stable operation identity is persisted before side effects. Recovery
credentials are encrypted with a persistent key. Repeated requests and
process restarts must reuse the operation identity and reconcile observed
broker/LOC state. A timeout means the outcome is unknown; it does not prove
that no work happened. Legacy in-flight work must drain using the legacy
binary before the schema/protocol transition.

Discovery stores the advertised protocol, work unit, price denominator,
estimator, job/session axes and opaque extra metadata. Protocol is never
inferred from a capability name. The former interaction-mode column remains
for historical schema compatibility but newly refreshed rows do not invent
a legacy mode. The catalog migration invalidates old active snapshots until
LOC supplies current metadata. Decimal price denominators retain full uint64
precision through Go and Postgres.

The media boundary is explicit: VOD inputs and ABR artifacts use object
storage, while gateway-owned RTMP ingress relays to the runner endpoint.
Live playback follows the runner HLS descriptor rather than constructing a
MinIO URL. The gateway has no FFmpeg dependency.

Beads replaces Markdown execution plans and the debt table as the only task
tracker. The user-supplied skill and references are vendored with attribution,
and Codex session hooks restore context. Historical plan documents remain
archived rationale; debt was migrated to actual Beads with the `legacy-debt`
label. Issue synchronization uses Dolt independently of Git code history.
