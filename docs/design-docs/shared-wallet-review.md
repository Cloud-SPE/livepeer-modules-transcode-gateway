# Shared-wallet compatibility review

Reviewed 2026-09-26 under Bead `vgw-vat`. This is source and local-test
evidence, not a production deployment certification. Work status and acceptance
remain in Beads.

## Follow-up verification and local fixes

Under `vgw-0p3`, the LOC working-tree fixes over `fe55795` passed 534 unit
cases (110 legacy skips), the disposable PostgreSQL migration rehearsal with
both terminal states, and all 13 real-stack scenarios. The latter includes
five-minute authorization expiry, signed non-admission release, broker restart,
refill, and terminal close. The original migration, canceled-unused schema,
active usage, and stale-harness findings below are resolved in that working
copy; this does not certify a deployed image.

Gateway `vgw-b3b` adds eight authentic signed v3 fixtures across two accounts,
ABR/live/refill/refused-successor wire tests, exact caller-proof vectors,
encrypted journal restart and full evidence preservation. The cross-repository
`scripts/shared-wallet-conformance.py` verifies post-roundtrip evidence with
LOC's actual verifier and checks wrong-account rejection. Deployment guidance
is in `DEPLOYMENT.md`. No gateway runtime protocol change was necessary.

Runner `runners-ivr` now resolves the upstream rendition master into direct
video media-playlist references and uniquely named audio groups. It preserves
audio metadata, restricts child URIs to local playlist assets, and publishes
only variants with finalized audio/video segments. The real MediaMTX test
successfully decodes audio and video from the public root using FFmpeg.
The local live smoke follows the new flat master structure.

During this verification, Modules advanced to `f39740c`, adding
`Health.ticket_stream_id` and `CreatePaymentRequest.expected_ticket_stream_id`.
LOC has not yet vendored or persisted that new recovery guard (`vgw-osr`).
This is additional work introduced after the original review: retain the payer
stream ID with mint intent and resend it on recovery to prevent a replacement
sender database from reminting uncertain funding. It requires LOC integration,
not gateway or media-runner wallet logic. The real-stack pass used this Modules
revision and the LOC working tree, but does not exercise sender DB replacement.

## Scope

| Repository | Reviewed revision |
|---|---|
| transcode-gateway | `dbdf686` |
| transcode-runners | `a03492a` |
| livepeer-network-modules | `b15ff35` |
| open-clearinghouse | `fe55795` |

The sweep covers the merged shared-wallet changes, their gateway and runner
boundaries, retained recovery state, migrations, configuration, and the earlier
live-playback/accounting issues. Modules comparison base: `5958706`; LOC:
`5d1446c`. It is not a claim that every unrelated line in four repositories has
been formally verified.

## Original findings at the reviewed revisions

**LOC migration blocks terminal grants (`vgw-7pr`, high priority).**
`migrations/versions/0029_wholesale_account_namespace.py:23` excludes only
`settled`, `expired_unused`, and `superseded`. LOC's
`domains/sessions/service.py:421` explicitly retires grants as
`operator_resolved`; its receiver reconciliation also persists
`canceled_unused` as terminal at lines 2289–2302. Both still count as active
to the migration. Operator resolution therefore does not unblock this upgrade.
Reproduced against disposable PostgreSQL using LOC's existing migration
rehearsal with `operator_resolved` substituted for its settled fixture. Funding
was acknowledged. Align the migration with the reviewed terminal-state policy
and add real-database cases for both states. Do not relabel unresolved grants
or bypass required receiver reconciliation merely to pass migration.

**Gateway lacks authentic v3 integration coverage (`vgw-b3b`).**
`gateway/internal/server/paid_engine_integration_test.go` still supplies
`job-authorization`, `session-authorization`, and `refill-authorization` as
opaque test bytes. Proxy settlement fixtures use dummy signatures. These are
useful lifecycle tests but do not prove acceptance by the new broker and LOC.
Add authentic v3 fixtures and verify ABR, live open/refill/close, refused
successors, encrypted-journal restart, and wrong-account rejection by LOC.
Include a coordinated deployment runbook in this repo.

**LOC vendored schema lags merged Modules (`vgw-5jk`).**
LOC `proto/livepeer/payments/v1/types.proto` omits
`SPEND_AUTHORIZATION_CANCELED_UNUSED = 7` and retains older terminal-settlement
comments. `payer_daemon.proto` matches. Refresh the vendored schema, generated
Python bindings, provenance hashes, and add descriptor parity verification.
Current HTTP reconciliation already recognizes the string `canceled_unused`;
this discrepancy is not evidence that that HTTP path currently fails.

**LOC real-stack conformance has a stale non-admission expectation (`vgw-81o`).**
`conformance/live/stack_harness.py:621` waits ten seconds for an undispatched
job to obtain non-admission evidence and then requires it to remain open.
Current `domains/jobs/service.py:267` issues five-minute authorizations;
`_request_non_admission` refuses queries before expiry, and verified expired
non-admission now closes/releases the engagement. The real-process test fails
at line 634 with `open / unresolved / NO_RECORD`. This is a reproduced test
contract mismatch, not proof that production should release earlier. Update
the harness to verify retention before expiry and signed release afterward,
then rerun the remaining live/refill/restart scenarios. Until then the complete
real-process release gate is not green.

**Runner HLS still nests master playlists (`runners-ivr`).**
`live-runner/hls.go:112` writes each root variant as `<rendition>/index.m3u8`.
The readiness check below it treats that index as a master and follows its
child media playlist. The advertised root therefore still points to a master,
which explains the previously observed playback problem. Flatten the root
variants to media playlists and preserve audio-group relationships. Verify
with realistic MediaMTX playlists and a real player. This predates and is
independent of shared-wallet isolation.

**LOC active billing display still uses legacy payment rows (`vgw-s44`).**
`domains/sessions/service.py:1478` sums `Payment.expected_value_wei`;
`get_session_status` uses it until final billing is persisted (line 2506).
Wholesale authorizations do not create those per-session ticket rows. Active
sessions can consequently show zero despite receiver debits. Report verified
wholesale usage separately from retail holds and finalized retail charges;
do not equate pooled funding or a reservation with customer spend. This is
also independent of shared-wallet isolation.

## Compatible boundaries

Gateway caller proofs sign the exact authorization bytes using the unchanged
`livepeer-invocation-proof/v1` algorithm (`proxy/livepeer/caller.go`). They do
not parse or require spend-authorization/v2. LOC response wrappers still use
`paid-job/v1`, `paid-session/v1`, and `accounting_mode=wholesale_account`.

The gateway retains the full signed settlement envelope and forwards it to
LOC. It does not reconstruct the payload from its typed summary; the new
`wholesale_account_id` therefore survives forwarding and journal persistence.
Protobuf uint64 settlement fields are JSON strings, preserving their precision
through journal decoding. LOC owns signature and account-scope validation.

Modules propagates account identity through funding, admission, saved open
intent, pending debits, revisions, cancellation, and terminal evidence. LOC
compares settlement account identity against the persisted grant, rather than
substituting the current configuration. Funding receipts identify the exact
payment bytes with SHA-256 and replay the original credit/snapshot.

Runners receive media workload/session contracts and do not mint or verify
spend authorizations. No shared-wallet-specific runner protocol or wallet
configuration change was identified. The gateway also needs no payer wallet
or account label for its existing single-LOC integration.

## Deployment implications

Use a stable, unique `WHOLESALE_ACCOUNT_ID` on each LOC environment. LOC
configuration and both compose files now require it with the real payer daemon.
The label identifies the LOC installation's wholesale account, not each portal
user. Separate products using the same LOC instance still share that account;
separate product accounts require separate LOC configuration or an explicit
future routing design.

Drain and reconcile legacy grants and pending funding using the old compatible
stack before the namespace migration. Preserve gateway operation encryption
and caller keys, LOC database state, broker state, and daemon ledgers. A payer
database owns its persistent random `ticket_stream_id`; do not clone it into
two simultaneously active independent payer instances.

Deploy compatible LOC, payer daemon, brokers, and receiver daemons as one
coordinated cutover. Mixed versions fail closed. Pin actual image digests or
build revisions: this source change does not itself bump all image tags.
The migration retains old empty-namespace credit for audit; it does not move
that credit into a new account. Do not merge namespaces on rollback.

Shared labels isolate accounting and nonce streams; sharing a private key
still shares signer authority and on-chain deposit exposure.

## Verification

Gateway: `go -C gateway test -count=1 -race ./...` with a fresh disposable
PostgreSQL database passed. Portal: all four Node tests passed. Runner:
`./build-images.sh test` passed (Go race tests and vet).

LOC: unit suite passed with `WHOLESALE_ACCOUNT_ID=loc-review`: 530 passed,
110 explicitly skipped legacy cases, 37 deselected. The skips are not coverage
of replacement behavior. Default test startup initially failed because this
checkout's real-daemon configuration lacked the newly required account ID;
an invocation-only override was used, with no environment-file edits.
Conformance lint/format and three Python fixture tests passed.

LOC's standard disposable PostgreSQL migration rehearsal passed; the additional
operator-resolved probe reproduced the migration defect above. Modules'
Docker `make test-revisions` passed, including broker recovery and real-receiver
tests with the race detector.

Modules payment-daemon Docker `make test` passed all packages, including the
shared-wallet independent-stream and exact-funding-receipt tests. LOC
`make test-live-stack` failed at the stale non-admission case described above;
later scenarios were not completed. A passing unit suite is not a substitute
for this missing end-to-end release evidence.

The original review changed no implementation or production service. The
follow-up changes described above remain local and uncommitted; no production
database or service was modified.
