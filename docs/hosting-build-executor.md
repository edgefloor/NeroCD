# Git build executor

<a id="git-build-executor-component"></a>

`runnerruntime.BuildExecutor` connects the compiled runner claim path to source
checkout, local image builds, typed output, and durable replay. Public HTTPS
works with build configuration. Private HTTPS Basic/PAT checkout requires the
[private opt-in](hosting-build-runtime.md#enable-private-https-git) and an exact
source grant. Same-host deployment and routing use separate operations.
Typed output requires journal format 7 or later and stores no private credentials.

## Operator configuration and calling contract

Construct `BuildConfig` with a private owned mode 0700 `SourceRoot`, separate
engine state root, fixed Docker binary and Unix socket, verified Buildx binary,
immutable Railpack planner image and delegated cgroup root. All ancestors must
be safe administrator-owned paths. The engine retains its existing requirement
for Linux on the Docker daemon host, host PID and Docker storage visibility,
rootful Docker and an explicitly delegated cgroup v2 subtree. The Buildx
container is privileged. This is a trusted build host, not hostile-fork isolation.

`BuildTarget` fixes runner, server and destination. A nonempty binding restricts
execution to that exact binding. An empty binding explicitly permits multiple
valid bindings on the configured target. Each execution pins its complete
initial authenticated snapshot; journal replay verifies its exact snapshot hash.
Placement comes from trusted operator configuration and authenticated status.
`BuildConfig.Validate` shares constructor prerequisite checks without a journal
or build effects and may initialize the engine state root.
`BuildConfig.Policy` must equal the complete persisted operator policy, including
build and acquisition budgets. A matching policy label alone is insufficient.
No configuration or capability is advertised by default.

`ComputeBuildToolchainIdentity(ctx, engineConfig, platform)` returns the SHA256
of the versioned `hosting-executor-toolchain.v1` inventory. It includes protocol,
platform, the actual Docker binary hash with a 128 MiB read bound, configured
Buildx checksum, pinned BuildKit/frontend/static images and immutable planner
image. It excludes local paths. The engine independently verifies the actual
pinned Buildx binary. The operator provisions trusted binaries and planner image;
this hash does not establish the contents of arbitrary operator-built images.
Binary and ancestor replacement by the host administrator is outside isolation.

`NewBuildExecutor` validates those prerequisites. `Execute(ctx, authority,
attempt)` requires the exact fenced tuple and an overall context deadline no
later than the configured build duration. The caller owns lease renewal and must
cancel the parent on loss. Historical `AttemptIdentity.ExpiresAt` records the
initial lease interval. It does not stop a renewed same-tuple lease. Fresh
authenticated `MayExecute` and the joined status watcher govern permission;
renewal never extends the operation deadline.

Private checkout requires the explicit configuration and source grant described
above. A fixture may allow its loopback CIDR and isolated TLS CA through a
trusted test Git wrapper. Production has no request-controlled CA or TLS bypass.

## Effects and receipts

The executor validates the complete immutable snapshot, recomputes its input ID,
checks source and settings semantics, exact placement and complete operator
policy before retaining intent. It then:

1. Appends nonsecret attempt intent before beginning source acquisition.
2. Commits the source receipt only after appending its exact journal request.
   An exact tuple, receipt ID and body acknowledgment must precede engine use.
3. Lets the source adapter destroy Git transport files and credential bytes
   before use. The stable checkout path is `SourceRoot/BuildID`; an exclusive
   owned flock prevents simultaneous use of the same build ID.
4. Observes the fixed local daemon using a private empty Docker configuration,
   explicit Unix socket, minimal environment, bounded output and a cancellable
   process group. `BuildPrepare` atomically pins that observed daemon at the
   current revision before engine effects.
5. Calls the engine with the snapshot's settings and limits. Only a
   result with a nil error can become an artifact. Source cleanup must also
   finish successfully before any success completion is retained or sent.
6. Appends the exact completion, including its revision, before POST. The
   artifact copies source, input, request, context, daemon, image, OCI index,
   manifest, config and platform identities directly from validated results.
   It binds the committed source receipt hash and never invents a RepoDigest.
7. Removes records only after matching exact completion proof. The controller
   must settle the generic task atomically with terminal build completion;
   this executor never sends a separate generic completion.

The joined watcher checks status at the configured interval. Any failed probe,
withdrawn permission, cancellation or changed snapshot, committed source or
pinned daemon cancels work. The engine and checkout monitors join before return.
A known engine failure with confirmed cleanup sends only fixed `build_failed`
metadata and no artifact. Raw source/build diagnostics never enter completion
metadata or executor errors.

<a id="replay-and-deliberate-uncertainty"></a>

## Replay and unresolved work

Call `Reconcile` before normal claims. It resends only exact retained source or
completion requests and preserves their IDs, bodies, tuples and revisions.
Status acknowledgment hashes are proof metadata; they settle only matching
retained bodies. Historical proof can be read after the initial lease expiry,
but never grants execution permission. A fresh uncommitted completion requires
current authenticated permission; a cleanup receipt additionally requires
verified owned cleanup and is not automatically retried here.

A completion record is retained only after local source and engine cleanup.
Replay refuses residual checkouts or the engine's durable
`cleanup_unconfirmed.json` marker. It never deletes images, source directories,
unknown cgroups or engine state to make replay succeed.

An intent without an exact retained completion is deliberately unresolved,
including a crash after an engine receipt was written but before the runtime
journal append. Reconcile does not adopt that receipt or rebuild. A lost source
response can settle the exact source request, but cannot reconstruct the missing
engine execution boundary. The executor refuses automatic post-source execution after restart. Private refetch and credential
redelivery are unsupported. A crash between acknowledging a result record and
its intent can conservatively retain intent for operator reconciliation.

`cleanup_required` is never success or execution permission. Lease loss and
source/transport uncertainty retain intent. Later controller cleanup must use
current fenced authority and independently verified exact owned cleanup. No
expired dispatched build may be generically requeued. Unresolved records block normal startup.

## Validation and limits

`acceptance/runtime-hosting-build/gate.sh` runs a separate owned disposable DinD
fixture with no host port publication. It uses Docker 29.4.0 DinD and Alpine
3.23.2 by digest, Buildx 0.38.1, BuildKit 0.34.1, Dockerfile frontend 1.28.0,
Railpack 0.40.1, Mise 2026.9.15 and nginx 1.29.3 Alpine, with the existing engine
image pins and binary checksums. Git and git-daemon are 2.52.0-r0. The fixture
asserts authenticated loopback phase traffic, actual TLS Git acquisition and
Dockerfile/static/Railpack HTTP content; it injects response loss, cancellation,
lease loss, daemon mismatch and failed builds, checks exact replay and owned
cleanup, and keeps an independent HTTP workload alive through failures.

Unit/race tests cover snapshot and target rejection, journal append ordering,
exact replay, mismatched proof, no result adoption, nil-error success, cleanup
failure, operation deadlines and same-tuple renewal beyond initial expiry.
Linux tests also exercise source flock/symlink denial and observer cancellation.
The engine's separate runtime gate checks
aggregate CPU throttling, descendant memory enforcement, bounded OCI output and
its broader strategy/path cases. Run the engine gate separately for those cases.

Disk acquisition and build usage are monitored with explicit byte/count/depth
budgets, free-space reserves and cancellation. Sampling can overshoot; these are
not filesystem quotas. The engine separately enforces cgroup CPU, memory, and swap limits. Server
admission and runtime configuration are described in [build control](hosting-build-control.md)
and [agent setup](hosting-build-runtime.md).
