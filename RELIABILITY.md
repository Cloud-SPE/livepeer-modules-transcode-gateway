# RELIABILITY

Paid operations are durable before network side effects. A stable workload
identity and idempotency key survive retries; recovery reuses them. A timeout
or dropped SSE connection is an ambiguous outcome, not proof of zero work.
Do not refund, discard evidence, or submit a new identity to hide that state.

LOC owns funding and routing. The gateway authorizes bounded work, executes
through the selected v2 broker, and settles broker-signed measured evidence.
ABR uses video-frame-megapixel; live uses finalized whole output seconds.
Estimates size authorization and never become settlement evidence. Settlement
and reconciliation failures remain persisted for recovery.

Customer requests require API keys and use per-key in-process rate limits.
There is no customer billing, but duplicate work still spends gateway credit;
clients must follow the API idempotency contract rather than treating all
retries as harmless. The generated OpenAPI schema documents request fields.

Live sessions are authorization-bound. The gateway exposes public RTMP,
relays with a broker-issued grant, and closes customer ingress when ending
a session. Losing a relay does not imply automatic migration to another
runner; output continuity across orchestrators is outside this deployment.

The LOC catalog refresh runs in the background and retains a previous
snapshot on transient failure. Authorizing new work still requires LOC.
The `/health` endpoint checks database, S3, LOC and RTMP; a healthy catalog
probe does not establish funded credit or a functioning runner. Validate
actual transcodes and playback separately.

Postgres, operation encryption/signing keys, and retained objects must have
coordinated backups. The v2 binary refuses legacy active paid state: drain it
with the legacy binary before upgrade. Do not remove database guards to force
an incompatible restart.

Operational signals include HTTP errors, structured logs, usage/live state,
paid-operation recovery and settlement backlogs, and `/metrics`. Repeated
recovery failures require investigation of the broker and LOC evidence, not
a reset to zero consumption. Exact metrics and response schemas come from
the running binary.

Single-instance deployment is the supported operational baseline. Rate
limits are not shared across replicas; distributed live ownership is not
claimed. Local unit and contract tests establish code behavior, while funded
end-to-end ABR/live, refill, teardown and restart tests validate a deployed
network. Track remaining validation in Beads.
