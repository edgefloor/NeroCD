# Source and build persistence checkpoint

This checkpoint adds an internal PostgreSQL contract for Git source configuration,
purpose-specific credential grants, build requests, fenced receipts, and local
image artifacts. It does not provide a usable Git deployment flow. Existing
image resources keep their existing source and generation behavior.

## Configuration and authority

`HostingGitSource` is a sidecar with its own immutable generations. It records an
exact resource/project, repository URL, requested ref, explicit repository policy,
credential kind, and accepted engine settings. Settings cover Dockerfile,
Railpack, and already prepared static files, including bounded relative context,
Dockerfile, target, publish directory, and optional SPA fallback. Paths are
validated semantically here; the checkout adapter and engine must still verify
actual filesystem confinement and file types when used. This store neither
compiles a frontend nor supplies build arguments or dependency credentials.

Repository policy is trusted operator admission data. A later public API must
resolve/authorize that policy through operator configuration rather than accept
arbitrary caller-supplied schemes, hosts, internal CIDRs or SSH fingerprints.
The internal repository method is not a public network-policy admission API.

Current project maintainers may configure sources and request builds. Current
project owners create and revoke source grants. Human membership, managed service
account scope/current role/owner status, and active unexpired legacy global-admin
tokens are checked from persisted records in the same transaction. Legacy
non-admin tokens do not gain project membership through this contract.

A private-source grant binds the exact source/resource/project, repository URL,
repository policy hash, Git credential kind, managed secret ID, and immutable
secret version. It contains no credential value or encryption envelope. Public
sources require no grant and reject an attached grant. A new secret version does
not revoke an older selected version while the managed secret remains active.
Secret metadata revocation revokes all versions. Grant rotation uses a new grant
ID; grants cannot be retargeted. This checkpoint does not deliver a secret to a
runner or recall a PAT already delivered by some future controller.

`HostingBuildPolicy` is trusted in-process operator configuration, never public
caller authority. It freezes toolchain identity, protocol, Linux architecture,
engine budgets, and distinct source acquisition budgets. Source acquisition
bounds tree bytes, workspace bytes, free-space reserve, entry count, depth and
polling. Engine limits retain monitored disk semantics: there is no hard quota
claim. A later controller must give both adapters deadlines and the exact frozen
budgets, trusted toolchain and stable owned checkout path.

## Requests and dispatch

`RequestHostingBuild` first locks the destination advisory key, then current
resource, authorization, source, active
project destination grant, immutable target, enrolled runner and source grant /
secret metadata. The runner needs `hosting_build_v1`, must be active with current
enrollment credential evidence, and must not have shell capability. The existing
server prerequisite still applies. The immutable snapshot includes resource and
source generations, settings, binding/destination/server/runner, policy, limits,
and exact grant version. Input identity hashes that snapshot before its own
input-ID field is assigned.

A queued request can have an empty expected daemon. That means **unprepared**;
it does not invent daemon registration. An existing trusted binding daemon
observation constrains later preparation. `PrepareHostingBuild` requires a live
full fenced tuple and committed source receipt, then atomically pins the
runner-observed daemon before image build/import. The pin cannot change and
artifacts must match it, including platform and validated OCI identity.

Request idempotency is resource-scoped. The same key and exact input/policy/actor
returns the original retained build without invoking the enqueuer again; changed
input conflicts. Replay still checks current actor, placement and grant/secret
revocation. An older source generation may finish historically, but cannot be
resolved as the current deployable artifact after configuration/resource changes.

Both controller seams are trusted **DB-only** callbacks receiving the existing
`pgx.Tx`. They must perform no external effects. A nil callback fails unavailable.
The enqueue seam must insert real durable work; no River kind or consumer is
installed here. The dispatch seam must create the new canonical task ID
`build_run_<build-id>` with exactly
`{"type":"hosting_build_v1","hosting_build_id":"<build-id>"}`, the original
project/actor, queued state and no preassigned runner. The repository verifies
that row was inserted by this transaction, then binds its immutable target and
commits the audit atomically. Callback failure or task mismatch rolls everything
back. Returning an arbitrary existing public run is rejected.

## Runner receipts and cleanup

The states are `queued`, `sourcing`, `building`, `succeeded`, `failed`, `canceled`
and `cleanup_required`. Builds never acquire a workload mutation hold and never
update desired deployment state. Undispatched cancellation becomes canceled;
dispatched cancellation requests cleanup. Cleanup uncertainty stays durable and
can later be acknowledged as a terminal failure/cancellation by live authority.

Every runner transition uses build/run/lease/attempt/fence plus exact pinned
runner, task body and project. Transactions lock task and historical lease first,
then destination advisory key, resource, current authorization, source, placement,
credentials and build. All placement paths, including request, dispatch and current
artifact resolution, use the existing image admission order of destination before
resource. An unlocked immutable binding read selects the advisory key only; the
resource/target join is re-read after locking and every binding/destination/server/
runner identity must still match. That initial read never authorizes relocation.
Source/grant configuration and cancellation take no destination lock.
Fresh authority is checked with `clock_timestamp()` after blocking locks and
again after state/receipt/artifact/audit writes. An expired lease that waited on a
row lock cannot commit a new receipt. The additive lease trigger denies raw
unbound build tasks and wrong target runners.

The source receipt freezes full Git commit, repository policy hash and canonical
tree digest against build/source/input identity. A literal full-commit requested
ref must match. The future adapter bridge must durably acknowledge this receipt
before checkout use, destroy credential/transport material before engine use,
and replay using the committed frozen commit rather than resolving a moving
branch. That bridge is not installed by this checkpoint.

Artifact success atomically commits its receipt, artifact, state and audit. It
stores origin `local_build`, source/input/source-receipt identity, context/request
digests, daemon ImageID, distinct OCI index/manifest/config digests, platform and
static context policy. It does not fabricate a repository digest. Exact committed
receipt replay requires the same receipt ID, body and full authority tuple;
expiry of that historical tuple permits acknowledgment only, not a new mutation.
New receipt bodies, old fences and cross-build artifacts conflict.

The current-artifact resolver deliberately requires current maintainer authority,
current source/resource generations, target enrollment/grant and live source
grant/secret metadata, even after successful historical completion. This stricter
revocation policy prevents a completed private-source artifact from becoming new
deployment authority after credential authorization is withdrawn. Historical
metadata remains available under current viewer authorization.

Non-success cleanup reports do not require an active source credential grant,
but still require current creator authorization, target/enrollment and a live
lease. Revoked runner/target/actor recovery therefore needs a future trusted
controller recovery contract; this slice does not bypass those checks or erase
cleanup-required history.

## Remaining integration and evidence

There is no public source-choice/API/UI, managed-secret delivery, atomic River
enqueuer/controller, typed runner executor, source-to-engine receipt bridge,
artifact-to-deployment resolver wiring, or end-to-end Git-to-HTTPS acceptance.
Generic public and nested RunSpec admission must explicitly deny this internal
type before any executor is registered. Enrollment/claim capability registry,
internal task decoding, deadline/lease renewal, retry and cleanup recovery must be
reviewed together. The schema compatibility predicate is available, but adding it
to global startup/readiness checks is outside this slice's ownership.

The isolated PostgreSQL tests apply all embedded migrations and exercise CAS,
request replay, DB-only callback rollback, project authorization and cross-project
denial, owner grants, immutable secret-version rotation, revocation, exact source
and artifact receipts, daemon pinning, stale attempts, terminal replay, old source
generation rejection, atomic audit failure, and lease expiry across an observed
row-lock wait. Fixture queues and task creation exist only in tests; they do not
claim a product worker. Existing source/engine transport and Docker acceptance
remain separate component evidence.
