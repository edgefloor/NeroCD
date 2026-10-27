# Hosting build control

<a id="source-and-build-persistence-checkpoint"></a>

PostgreSQL stores Git source versions, purpose-specific credential grants, build
requests, fenced receipts, and local image artifacts. The server dispatches builds
to an explicitly configured runner through River. See [agent setup](hosting-build-runtime.md)
and [Git application configuration](hosting-git-resources.md) for the complete workflow.

## Configuration and authority

`HostingGitSource` is an immutable version referenced by a Git application. The
[public Git configuration workflow](hosting-git-resources.md) atomically writes
resource configuration and source history. The standalone sidecar writer is
retired. Each version records an
exact resource/project, repository URL, requested ref, explicit repository policy,
credential kind, and engine settings. Settings cover Dockerfile,
Railpack, and already prepared static files, including bounded relative context,
Dockerfile, target, publish directory, and optional SPA fallback. Paths are
validated semantically here; the checkout adapter and engine must still verify
actual filesystem confinement and file types when used. This store neither
compiles a frontend nor supplies build arguments or dependency credentials.

Repository policy is trusted operator admission data. Public resource input never
accepts caller schemes, hosts, internal CIDRs, or SSH fingerprints as policy. The
server supplies a bounded public HTTPS host allowlist, defaulting to github.com.
See the configuration guide for `NEROCD_HOSTING_GIT_ALLOWED_HOSTS`.

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
ID; grants cannot be retargeted. The protected credential endpoint delivers
credentials to an explicitly configured private runner.
Resource configuration admits public HTTPS or `https_basic`; it never accepts a
credential value or caller-supplied policy.
No credential recall is promised after transport delivery.

`HostingBuildPolicy` is trusted in-process operator configuration, never public
caller authority. It freezes toolchain identity, protocol, Linux architecture,
engine budgets, and distinct source acquisition budgets. Source acquisition
bounds tree bytes, workspace bytes, free-space reserve, entry count, depth and
polling. Engine limits retain monitored disk semantics: there is no hard quota
claim. The runner bridge must give both adapters deadlines and the exact frozen
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
The enqueue seam must insert real durable work. The optional River build adapter
registers its worker only for a handler implementing `BuildHandler`; the server installs the DB-only controller only with explicit trusted build policy. The dispatch seam must create the new canonical task ID
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
ref must match. The runner adapter bridge must durably acknowledge this receipt
before checkout use, destroy credential/transport material before engine use,
and replay using the committed frozen commit rather than resolving a moving
branch. The [build executor](hosting-build-executor.md) enforces this order.

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

Fresh cleanup reports require the current creator, source/resource, target,
enrollment, source authorization and live lease gates. Revoked runner, target or
actor recovery is unsupported. Cleanup-required history remains retained.

<a id="remaining-integration-and-evidence"></a>

## Verification

Compiled runners support public and granted private HTTPS execution, typed build
output, and same-host artifact deployment. Generic public and nested RunSpec
admission denies internal build tasks. Startup and readiness validate the current
embedded schema predicates. See [private operator setup](hosting-build-runtime.md#enable-private-https-git).

The isolated PostgreSQL tests apply all embedded migrations and exercise CAS,
request replay, DB-only callback rollback, project authorization and cross-project
denial, owner grants, immutable secret-version rotation, revocation, exact source
and artifact receipts, daemon pinning, stale attempts, terminal replay, old source
generation rejection, atomic audit failure, and lease expiry across an observed
row-lock wait. The controller tests additionally use the real River worker and
all five authenticated HTTP phases. Test-only callback fixtures retain their
existing rollback/security coverage. The [compiled controller gate](../acceptance/runtime-hosting-build-controller/README.md)
checks source and engine execution together.

## Function schema binding

Migration 0048 additively binds the seven functions introduced by immutable
migration 0047 to their owning schema. Six trigger functions resolve through
`pg_catalog`, that trusted schema, then explicit `pg_temp` last. The compatibility
predicate puts the owning schema first because its `current_schema()` checks must
inspect its own tables and triggers; trusted catalogs and `pg_temp` follow. Neither
path inherits caller-selected schemas or `public` unless `public` is itself the
migration's owning schema. Functions retain invoker permissions and all existing
lease target, canonical task, build phase, receipt and artifact predicates.

A schema-qualified lease update can therefore come from an administrator with an
unrelated search path without breaking ordinary nonbuild leases. Same-name tables
and functions in another schema, including temporary shadow tables, cannot bypass
build target admission. The missing-secret and missing-destination migration
fixtures explicitly remove the additive build dependencies and restore the full
layers before checking recovery; their readiness and startup assertions remain.

The PostgreSQL regression runs in a nondefault schema with unrelated, same-name
shadow and temporary caller objects. It checks ordinary lease updates, correct
build admission, wrong-target denial and owning-schema compatibility. A real
`pg_dump`/`pg_restore` round trip and a replay of 0048 repeat the admission and
compatibility assertions against the restored schema.

## Server controller configuration

The server wires public and owner-granted private HTTPS Git requests through
the existing River adapter and canonical exact-target dispatch. The separate
credential endpoint requires protected transport and live private-capable runner
authority. Older agents gain no execution support from server wiring alone.
Image deployment and Git build configuration remain separate commands. Follow
the [private runner setup](hosting-build-runtime.md#enable-private-https-git)
before enabling its persisted capability.

Build admission is disabled by default. Set
`NEROCD_HOSTING_BUILD_POLICY_FILE=/absolute/operator/build-policy.json` to enable
it with PostgreSQL and migrated River/native schemas. The file must be a regular
file of at most 64 KiB containing one strict `HostingBuildPolicy` JSON document.
Every identity, platform, engine limit, and source acquisition limit is explicit.
Unknown fields, malformed or multiple documents, unsupported budgets and missing
values fail startup. The server clones the validated policy before starting its
workers. Configuration performs no Git, Docker, or registry IO. The platform
never defaults to the control-plane host architecture.

Durations use integer nanoseconds, matching Go JSON encoding of `time.Duration`.
For example, 60 seconds is `60000000000` and one second is `1000000000`. A bounded
example follows. Replace the two hashes with the operator's actual
policy and toolchain identities rather than treating these sample hashes as
verified toolchain evidence.

```json
{
  "identity": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "toolchain_identity": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "protocol": "local-build.v1",
  "platform": "linux/amd64",
  "limits": {
    "cpu_quota": 100000, "memory_bytes": 536870912, "swap_bytes": 0,
    "deadline": 60000000000, "context_bytes": 1048576, "context_files": 100,
    "plan_bytes": 1048576, "log_bytes": 1048576, "export_bytes": 10485760,
    "unpacked_bytes": 10485760, "usage_bytes": 20971520,
    "reserve_bytes": 10485760, "poll_interval": 1000000000,
    "disk_mode": "monitored"
  },
  "source_limits": {
    "max_tree_bytes": 1048576, "max_workspace_bytes": 10485760,
    "min_free_bytes": 1048576, "max_entries": 100,
    "max_depth": 32, "poll_interval": 1000000000
  }
}
```

The dedicated enrolled destination runner must explicitly support
`hosting_build_v1`, in addition to the existing server admission requirements.
The server registry recognizes the type but never advertises it as a default
agent capability. A build claim is authorized inside the lease transaction by
its immutable task relation and exact runner, current creator authority, current
resource/source generations, destination grant, enrollment and restore gate.
Matching tags alone cannot grant the task. Internal types and authority fields
are forbidden in all public workflow shapes, including nested steps.

## Build lifecycle settlement

Terminal `succeeded`, `failed`, or `canceled` phase completion atomically commits
the build state, exact receipt, optional artifact, audit, task status and exact
lease status. Agents must not send generic run completion for a build task.
Generic run approval, cancellation, status changes and generic requeue cannot
settle this work. A first completion needs live authority after every blocking
lock. An already committed receipt remains historical proof after expiry.

`cleanup_required` records durable cleanup evidence without publishing success
or granting engine execution. It leaves only the existing bounded lease available
for an explicit later cleanup acknowledgment. It never renews that lease. A
canceled dispatched task with no attempt is canceled atomically while its task
lock excludes claim. An active attempt receives cleanup intent. An expired
dispatched build becomes `cleanup_required` with `authority_lost`, and its task
fails instead of being blindly requeued. There is no automatic destructive cleanup,
engine retry, or operator uncertainty override. Historical metadata and the exact
lease tuple remain retained.
