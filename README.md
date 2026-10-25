# NeroCD

NeroCD is building a self-hosted application platform in Go that replaces the
hosting workflows of Coolify. The active journey is:

> Connect a server → select a repository or image → configure the application → deploy → open its HTTPS URL → operate and recover it.

Hosting requirements R01–R68, acceptance scenarios A01–A37 and the declared
compatibility matrices lead the [implementation plan](docs/plan.md). Direct
hosting remains the primary navigation and onboarding journey until the core
replacement is complete. Deploying or operating an application must not require
a Task, coding agent, workspace or operational template.

The [complete specification](docs/product-spec.md) also retains revision 3's
future shared-workspace vision: people and AI agents build, test, review and
deploy software on machines they control, with durable Tasks and retained work.
R69–R102/A38–A66 and D0–D3 remain deferred scope. They must not delay hosting.
The [hosting ledger](docs/compatibility.md) and
[workspace ledger](docs/workspace-capabilities.md) keep these claims separate.
Feature parity, migration compatibility and unchanged-client compatibility
are different outcomes.

## Available now

Hosting-first documentation baseline: `65311f30470f34d0d26165d1cd13325fbd05fca0`.
Accepted application/source assessment:
`9a69228f9a2a8324e25b1c366ee4eba913b8dba2`, 2026-10-25. Existing Go API/application services, PostgreSQL, River execution,
authorization, audit and the embedded React/TypeScript UI are the starting system.

- Hosting navigation leads with Home, Projects and Servers and runners, with
  direct image/Git application guidance and advanced workflows preserved.
  Accepted at `43e9c305f71a267b1d201f7849c12a31f8c532d6`; see the
  [navigation evidence and limits](docs/compatibility.md#accepted-foundations-and-evidence).
- PostgreSQL state, checked migrations through 0072, bootstrap administration,
  local-password sessions and one configured OIDC issuer.
- Projects, operational templates and execution runs, approvals, service
  accounts, API tokens, enrolled runners, fenced leases, replay and audit.
  Existing execution runs are distinct from the proposed durable collaboration Task.
- Shared project environments, typed hosting resources and explicit server,
  destination and project-grant bindings through API and UI. Stable placement
  does not imply host virtualization or broad machine qualification.
- Encrypted project-secret versions with create, rotate, protected owner reveal
  and revoke. [Broader execution delivery and value scopes](docs/secrets.md)
  remain planned; existing runner-file bindings have their own contract.
- Desired runtime configuration through resource GET/PUT APIs, with generation
  compare-and-swap, idempotent requests, explicit public nonsecret literals and
  exact managed-secret version references. Current owners write and viewers read.
  Migration 0052 stores desired input; migration 0053 adds accepted admission.
  Protected runner delivery checks exact prepared authority and current actor,
  configuration and secret versions, then audits metadata before decryption.
  Retries retain the original bounded delivery window.
  Journal format 8 stores only nonsecret references and delivery identity.
  Explicitly enabled CLI/API execution applies literals and exact managed-secret
  versions to Docker through protected stdin, with fixed delivery/replay deadlines.
  After authorization commits and before returning environment bytes, the endpoint
  rechecks the original runner credential and rejects delivery if it detects rotation.
  It cannot recall bytes already sent or values delivered to a runner or container.
  Docker administrators can inspect container environment values.
- Bounded runtime status challenge/report controls and viewer freshness reads,
  through migration 0054. The collector requires `--hosting-observation` with
  matching persisted `hosting_observation_v1` capability. It reads narrow Docker
  status and loopback readiness, selecting four targets fairly per 10-second cycle.
  This is not a 10-second sampling guarantee for each resource. The accepted
  read-only dashboard separates current, stale and historical target evidence,
  checks current viewer access and bounds reads with timeout and retry handling.
  Disk/CPU metrics remain pending.
- Runtime-log control through migration 0055: actor sessions, renewal, stop and
  bounded retained reads, plus exact runner claims/events, counters, replay gaps
  and purge. The accepted live adapter and embedded viewer connect fresh Docker stdout/stderr
  from a registered settled deployment through the compiled collector and API.
  Choose Follow to admit 30-second demand. Reads show source and retention gaps;
  access revocation clears sensitive state. Known bearer redaction is bounded,
  and raw application output can contain secrets. See the
  [collector](docs/hosting-runtime-logs.md) and
  [viewer](docs/hosting-runtime-log-viewer.md) limits.
- Enrollment commands use the exact persisted capabilities and identify runner
  configuration prerequisites, including `--hosting-runtime-logs` matched to
  persisted `hosting_runtime_logs_v1` in the manual hosting profile. This does not
  qualify SSH-assisted enrollment or
  complete host readiness.
- [Server-readiness diagnostic API](docs/server-readiness.md), accepted at
  `3cb62858e3d1e263d89d5e3c6aecd0a30792e938`, records bounded profile evidence
  and derives current authority/freshness status. It does not authorize deployment.
  [Manual UI enrollment and generated-command collection](docs/server-readiness.md#runtime-verification-fixture)
  are accepted at `0771f1b56befcd30b195c45121dba4d3efeec974`. The compiled native
  Linux arm64 agent uses fixture-CA-verified HTTPS, with current ready/blocked/stale
  recovery, viewer denial, role loss and persisted-credential restart checked.
  CLI availability is metadata-only; the profile requires Docker API 1.54 and
  1 GiB free per work/journal root. amd64 is compile-only. Production TLS,
  SSH, application deployment and complete A01 remain open.
- [Verified route-switch engine](docs/hosting-route-switch.md), accepted at
  `99250c2634109cdd2008c56dae1b1740d377c0a1`, checks old/new HTTP and trusted TLS
  before committing an opt-in existing-route switch. Failed verification requires
  verified rollback; cancellation or uncertain rollback retains `Uncertain`.
  The guarded controller is accepted at `5687b88465839b8e341181ba8b47f54dc17f006c`.
  Its resource POST selects exact healthy or retained-running releases, requires
  explicit agent opt-in and retains holds for cancellation, uncertain replay or
  metadata conflicts. Resource-scoped reads and operator controls are accepted at
  `1c6ddb414d984372bb1f081fb6ff23db8bd81f88`, with explicit review, frozen retries,
  pending-operation recovery and visible holds. The bounded
  [completed metadata-conflict recovery API](docs/hosting-proxy-recovery.md) is
  accepted at `e097ffa706325f55cab7a7b0416a235a0e9ad151`, with reviewed expiring
  plans and idempotent apply. Uncertain outcomes remain held. Recovery administrator
  UI is accepted at `d238d23e63ea2ae7f1bb0f7ed843f7b71863660f`, with explicit
  plan review, confirmation and scoped exact retry. Queued convergence is not
  completed convergence. Protected deployment approval, draining, data rollback and
  production ACME remain open.
- Runtime configuration UI for public literals and exact managed-secret versions,
  generation compare-and-swap, idempotent retries and fresh deployment generations.
  Accepted browser proof covers authenticated API, compiled runner and Docker,
  failed-readiness preservation, viewer authorization and responsive layouts.
  It uses seeded accounts, resources and placement plus a trusted persisted Git
  artifact; fresh Git builds, onboarding and public ACME are separate work.
- [Public registry-image deployment](docs/hosting-execution.md) through the
  API, transactional River dispatch and explicitly enabled agent. It records
  exact image/platform/daemon identity, ordinary HTTP readiness, per-release
  loopback ports and selected retained cleanup.
- [Public HTTPS Git configuration and builds](docs/hosting-git-resources.md)
  under trusted operator policy. The normal compiled agent builds Dockerfile,
  pinned Railpack and prepared static/SPA assets into immutable local artifacts.
  [Private HTTPS Git](docs/hosting-git-resources.md#request-a-private-https-git-build)
  supports explicit Basic/PAT source grants, compiled CLI Dockerfile and Railpack
  builds, and Dockerfile/Railpack static-provider WebUI journeys through output,
  same-host deployment and test-CA HTTPS. Private Railpack qualification covers
  resource creation/editing, exact-version grant retry after a lost response and
  preservation of the old HTTPS application after a genuine failed rebuild.
  Arbitrary language providers, provider breadth, broader frontend coverage and
  complete A03 remain separate work. See the [accepted limits](docs/compatibility.md#accepted-foundations-and-evidence).
- [Compiled Vite SPA qualification](acceptance/runtime-hosting-local-build/README.md)
  is accepted at `d29edb3bc8cde064108891f70713423bb57776a3` for the exact pinned
  Node/Bun/Vite/Caddy fixture. A real parser failure preserves the prior application.
  Earlier unexplained failures remain a reliability limit; full A04 and production
  ACME remain open.
- [Exact trigger revisions](docs/hosting-build-trigger-revision.md) admitted by a
  trusted internal caller are frozen before checkout and require the runner's
  revision capability. Public/private HTTPS checkout builds the selected commit
  even after a branch moves; unavailable commits fail without branch fallback.
  The accepted GitHub build-on-push integration is described below.
- [GitHub build-on-push](docs/hosting-github-push.md) is accepted at
  `be85bf4c4d42c0b2ddb9e9935f2fa74332656bde`.
  Signed GitHub.com pushes for one saved branch require independent current-head
  verification. Duplicate commits coalesce, delayed older deliveries are ignored,
  and a failed build preserves the serving release. Owners configure policy and
  recover deliveries; viewers read history. Deployment is manual in the default build-only policy.
  Local TLS simulation covers registered API/River, compiled CLI and Docker builds,
  not live GitHub webhook setup or complete A09. Optional automatic deployment
  is accepted separately below.
- [Trusted automatic deployment](docs/hosting-auto-deploy.md) is accepted at
  `86825ab17f2e2bb0b2b0a26e6e65e2e2769d8fa3`. Optional `automatic_unprotected`
  follows a verified fixed-branch GitHub push through build, fresh-port deployment
  and an existing trusted HTTPS route switch. It requires an initial healthy manual
  release and existing routes. Failed builds/readiness preserve the prior app and
  unrelated routes. Live GitHub setup, protected approval, remote builders,
  previews and public ACME remain unsupported.
- [SSH prerequisite installer](docs/hosting-ssh-bootstrap.md) and
  [durable control](docs/hosting-ssh-onboarding.md) have accepted bounded evidence
  on native arm64 Debian 12/systemd. The baseline installer advertises only four
  capabilities, not build/proxy/log capabilities. The full SSH API/UI journey is
  accepted at `b4951282aefb3ad63d525c9d10037d7120f1d4ba` for the prepared-host
  fixture, including response-loss inspection recovery. This does not qualify
  live-host/OS breadth, build/proxy/log/storage presets or complete A01.
- Owned-storage metadata, the private volume helper and durable materialization
  admission are accepted through `8e00631aac26e0787a43398332e31a35980f067d`.
  See the [foundation evidence and limits](docs/compatibility.md#accepted-foundations-and-evidence).
  The [uploaded-file API/runtime fixture](acceptance/runtime-hosting-storage/README.md)
  now qualifies an opt-in mounted workflow through replacement, lost acknowledgment
  and runner restart, and failed-readiness restoration. Bounded unqueued-continuity
  recovery is accepted at `284f45cf1659a559d55d54342a902d0eeb112685`.
  Metadata API/UI is accepted at `2f8beb9b555e651bcccb47d3c0dbc7ccfdb268db`;
  saving mount intent alone does not materialize or mount a volume.
  Storage deployment-review/recovery UI is accepted with UX-only limits below.
  Standalone database qualification and direct UI are recorded below. Metadata
  remains `adapter_unqualified`; connected application binding was unfinished at
  this foundation checkpoint. Later bounded qualification is recorded below.
  Database-native backup/restore, unknown-writer recovery, production ACME and
  complete A19/A26 remain open. Bounded uploaded-file recovery is accepted below.
- Storage deployment-review/recovery UI is accepted at
  `77be63a03c9caf3e95239a751045f0fe7b5918cf`. Its browser qualification covers
  connected review/retry UX with intercepted operation responses; physical upload
  persistence remains the separate compiled-runner fixture evidence.
- Database creation/domain, metadata UI, private controller/engine helpers and
  protected credential delivery are accepted through
  `33a7506f8b5f92f88f455e3cc4029629dfb6c7a9`. The engine and credentials are not
  runner-wired at that delivery checkpoint. Direct engine qualification is now
  accepted at `b9c4c93ef55d946ca67384599d4c6d6290e36d0e`; default-off runner
  coordination is accepted at `f4a54506f388339ffe8acdd6b8ec4133690188fa`.
  The original physical pass does not qualify the changed combined tree. Database
  direct deployment UI is accepted separately from its intercepted browser proof.
  The exact authenticated API/River/compiled-runner workflow at
  `c731f708e62e2376d174041957b8654fe83d5bcf` physically passed PostgreSQL/Redis
  write, redeploy/read and lost-completion-ACK restart/read in 29.75 seconds.
  That evidence stays at its original revision. Later shared database engine
  changes mean it does not qualify the current combined tree. See the
  [workflow fixture](acceptance/runtime-hosting-database-runner/README.md).
  Default-off opt-in and metadata `adapter_unqualified` remain. Application binding
  was absent at this checkpoint; later bounded proof is below. Database-native
  backup/restore, public ACME and complete A17 remain unfinished.
- Internal R42 prerequisites are accepted through
  `d3849bb572ce5b7df40ef7324ef6bc71e8e08c92`, including the read-only origin
  observer, authenticated capture API/typed client, format 13 journal and private
  capture coordinator. Direct private-engine origin capture and empty-v2 alias
  upgrades physically passed PostgreSQL 17.11 and Redis 8.2.10 A→B→C in 27.89
  seconds, preserving volume/network/credential references, private DNS reads
  and writes, retained origin and reopened UID 999 ownership. See the
  [fixture contract](acceptance/runtime-hosting-database-origin-upgrade/README.md)
  and [accepted components and R42 limits](docs/compatibility.md#r42-remains-an-unfinished-hosting-workflow).
  Materialization is fixture-local, without controller provenance. This does
  not qualify the connected API/River path; later capture proof is below.
  Application membership ledger/API/UI were absent at this checkpoint. Full R42 remains unfinished.
  Generic capture dispatch and the later upgrade controller are accepted
  separately below. Database-native backup/restore and rotation remain open.
- Compiled capture/replay and normal PostgreSQL/Redis recovery are accepted at
  `b7771468edf95f531c13d69b361ed901d9efbbc8`. Both engines passed without skips:
  capture/replay in 22.00 seconds and normal recovery in 23.40 seconds, using
  real API/River source deployment and the opt-in exact-ID capture CLI. Lost
  origin ACK replay preserves the three-phase format 13 journal, holds ordinary
  mode, then replays historical bytes with the owned Docker socket absent and
  no repeated claim or observation. Source, volume, network and credentials
  remain unchanged. Database completion now joins authority workers, and UTC
  capture round trips compare persisted JSON identity. See the
  [compiled-runner fixture](acceptance/runtime-hosting-database-runner/README.md).
  This qualifies native Linux arm64/trusted Docker with a verified test CA.
  Application binding and membership ledger/API/UI were absent at this checkpoint.
  Database-native backup/restore, rotation and full R42/A17/A26/A37 remain open;
  production ACME and untrusted workload isolation are unqualified.
- Private empty-v2 upgrade control and compiled execution are accepted through
  `79b50e245a756022b2fc0b8ac98428bb1acfcc3b`, with migration 0071.
  The owner-scoped controller derives proof from retained capture/origin records,
  admits initial upgrades and empty-v2 maintenance, and persists idempotency and
  historical ACK replay. The runner requires explicit upgrade opt-in and matching
  capability. Selected real PostgreSQL control tests passed without skips in
  104.040 seconds for store and 15.104 seconds for API, with exact cleanup.
  The compiled physical gate is accepted at
  `563fb88f707b75563d03f0408df0267e3af473e9`: capture/replay 38.22 seconds,
  normal recovery 23.40 seconds and empty-network upgrade 26.36 seconds, all
  without skips. PostgreSQL and Redis passed initial upgrade, maintenance,
  retained origin, alias reads/writes, exact Complete-ACK loss replay and
  single-writer checks. Owned cleanup and outer inventories were verified.
  This qualifies the native Linux arm64 disposable private fixture with a
  verified test CA. Public application binding was absent at this checkpoint;
  later bounded proof is below. Engine-version upgrades and full R42/A17 remain
  unfinished. See the
  [current evidence and limits](docs/compatibility.md#r42-remains-an-unfinished-hosting-workflow).
- Automatic database capture is accepted at
  `5ff5b7e114fc922e3c3f2a6e93670c133d205923`. The default-off
  `--hosting-database-network-capture` flag uses bounded claim-next selection
  under current authority and reuses the same claim after response loss.
  Both PostgreSQL and Redis passed exact-ID and automatic capture in 32.92
  seconds, normal recovery in 23.71 seconds and upgrade in 28.23 seconds,
  without skips. With Docker absent, retained origin ACK replay and journal
  retirement were proven before cancellation. This does not assert successful
  exit of the continuous runtime. Cleanup and outer inventories passed within
  the native Linux arm64 private fixture. Public application binding and durable
  application membership were absent at this checkpoint; bounded later proof is
  below. Complete R42/A17 remains unfinished. See the
  [qualification evidence](docs/compatibility.md#r42-remains-an-unfinished-hosting-workflow).
- [Application/database connections](docs/hosting-database-connections.md) with
  one owned upload volume are qualified at physical source
  `09a432407c41671517fa342d28c07e9c79978aea`, with documentation accepted at
  `3b192e4f9602d606f9f51c921adbce8870401588`, migration 0074 and journal 17.
  First Connect deploys without application storage or routes. Owners then attach
  one owned named volume and acknowledge interruption for replacement. Routed
  redeployment, fresh-authority recovery and later deployment retain database
  records and uploads. Seven Go groups passed in the 683.423-second full gate;
  six browser phases passed with 30 tests passing and six explicit profile skips.
  The trusted native Linux arm64/root-image fixture uses private-CA HTTPS.
  Production ACME, arbitrary nonroot initialization, source builds, rotation,
  native backup/restore, disk loss, cross-host/external storage and full R42/A17
  remain unqualified. Earlier storage-less receipts retain their original scope.
- [PostgreSQL logical backup and restore](docs/hosting-database-backups.md) is
  accepted at `56cce329e620472e3c82e9ed7ef0ae0618f124e4`. Owners request an
  immediate local backup and restore it into a new private database through the
  embedded UI. The compiled-runner qualification proves application reads and
  new writes, distinct target storage and credentials, completion replay and
  failure holds. The final physical gate passed at source `c1f7b153a963609336851f94a16b2eac96f86e91`
  in 130.140 seconds, retained by exact relevant-source correspondence.
  This qualifies native Linux arm64, trusted classic-store Docker and PostgreSQL
  17.11 on one destination. Later scheduled and remote qualification is below.
  Retention automation, in-place and cross-destination restore, other engines
  and full A25/A26/A37 remain open.
- [PostgreSQL backup schedules](docs/hosting-database-backup-schedules.md) are
  accepted at `60b86e47504533280170028cbc0b9b632c7b67db`, with physical proof at
  `c76019739ffbde0bcf7eb357c9740ddd35bf9a57` retained by exact source correspondence.
  Owners create, edit and disable schedules with an explicit IANA timezone.
  The real embedded UI and running controller prove scheduled dump admission,
  exact retry and restart identity, new-target restore and application data.
  The full 157.868-second gate also proves guest `pg_dump` timeout separately
  from restore timeout. The supported cron subset excludes weekday `7` and
  `L`/`W`/`#` forms. Retention, notifications and general scheduler parity
  remain open within the existing PostgreSQL-only physical profile.
- [PostgreSQL remote backups](docs/hosting-database-remote-backups.md#accepted-qualification-and-durable-evidence)
  are accepted at `6621204964e96039558c0f267dbac50475a7ad98`. Versioned encrypted
  S3 targets, separate local/upload results, retry of the same archive without
  another dump, committed-ACK replay and remote GET into a new private database
  passed with application reads and new writes. The native Linux arm64 fixture
  uses pinned PostgreSQL 17.11 and Silo on an explicitly trusted loopback HTTP
  origin. Target setup/validation uses the API; selection, warning/reload, retry
  and restore use the live UI. Production S3 HTTPS, retention/deletion,
  notifications, other engines and aggregate parity gates remain open. The
  existing user UI process is unchanged.
- The [internal upload archive helper](internal/storagearchive/README.md) is
  accepted at `3b34b3920378cc41613bc87ca69eea2d35d2f175`: a standalone fixed-root
  Linux helper for strict USTAR `nerocd-storage-v1`. The
  [archive controller/configuration/API/runner](docs/hosting-storage-archive.md)
  are accepted through `88887e92c0cf7b93b828d3696ec3a5af16547ea7`, alongside helper
  packaging. Helper config-ID qualification targets the classic Docker image
  store; containerd-store lookup failed and has no runtime fallback. The
  qualification image was removed; it is not an installed pin.
  Earlier PostgreSQL/River tests use a fake engine. Bounded physical uploaded-file
  recovery is now accepted at `45298ea4bbd85c91ee4b0d4d64cd72d9346e1faa`: backup/
  restore ACK loss recovers with one helper start each, and a new resource/volume
  proves restored reads/new writes with source metadata preserved. This qualifies
  native arm64/classic-store same-host uploads, not database backups, S3, host disk
  loss, in-place restore, containerd, production TLS or complete A26/A37. Archive UI is accepted at
  `a99a75517c018caa9aa9965cf451f9f826e7b3d6` with intercepted-browser evidence;
  that does not qualify physical application backup/restore.
- [Same-host artifact deployment](docs/hosting-local-build-runtime.md) and its
  [resource UI](docs/hosting-build-deployments.md#deploy-through-the-resource-ui).
  An explicitly enabled agent deploys the recorded artifact on the same daemon
  without rebuilding or pulling. Deployment and route selection are separate actions.
- [Durable typed build output](docs/hosting-build-log-delivery.md) with
  redaction, version 7 journal cursors, exact acknowledgment and response-loss
  replay before completion. The [opt-in build output viewer](docs/hosting-build-logs.md#resource-build-output-viewer)
  follows retained sequence pages, reports gaps and bounds local plaintext.
  This is build output, not general application-runtime logging.
- [Managed Traefik routing](docs/hosting-proxy-ui.md) with authorized
  generations and explicitly capable Linux agents. Real isolated fixtures
  verify HTTP and HTTPS using a trusted test CA, including local build
  artifacts. Public DNS and production ACME qualification remain separate.
- An embedded React/Vite/TanStack application and a versioned
  [API contract](openapi.yaml). Most list APIs use bounded offset pagination;
  the typed build log API uses its documented sequence cursor.

The active sequence is practical repository/image-to-HTTPS hosting and
operation/recovery, useful stateful hosting, remaining core parity, then migration
and extended compatibility. Automatic deployment and SSH onboarding are accepted within their stated limits.
Stateful hosting remains active. Bounded uploaded-file persistence/recovery and
standalone PostgreSQL/Redis hosting and bounded same-destination image-app
connections have physical evidence; broader R42 application binding, database-native recovery and broader stateful qualification
remain unfinished. Protected plans and production TLS remain open. Broader
credentials, Compose/storage, databases, backups, restores, previews, terminals,
schedules and notifications remain open.

Remote builders, registry delivery, caching, releases, approvals, permissions and
observations can advance through a named hosting contract. Before any workspace
change, record [which hosting requirement needs it now, the minimum scope and
its acceptance test](docs/plan.md#hosting-dependency-test). General future
usefulness and sunk effort are insufficient.

Durable collaboration Tasks, coding-agent adapters, attachment, portable
checkpoints, general reservations, model/GPU services and vendor packages stay
deferred unless a bounded part passes that test. Existing unaccepted Task
checkpoints are preserved, not integrated. Trusted-host deployment does not
wait for a VM backend; untrusted execution stays disabled until its declared
isolation requirements are satisfied.

Existing fixed-port replacement can interrupt service. No narrow deployment
demo is complete Coolify parity.

## Quick start

With Docker Compose installed, start the development stack:

```sh
docker compose up --build
```

Open <http://localhost:8080>. This starts PostgreSQL, migrates it, loads the
development fixture, and starts NeroCD. Stop it with `docker compose down`; add
`-v` only when you intend to discard the local database volume.

To run from source outside Compose, use the Go 1.26.10 compiler with the Go 1.25
language baseline and Bun 1.3.6 (the
locked version used by CI), then build the embedded web assets. An unconfigured
server is rejected. Disposable memory is available only with
`NEROCD_DEV_MEMORY=true` and the required bootstrap email and owner-only
password file. See
[Getting started](docs/getting-started.md).

## Build and verify

`make build` installs locked Bun dependencies, builds `web/dist`, and embeds it
in the Go binary. `make check` runs normal Go, web, policy, and contract checks.

## Operations

[Production](docs/production.md) is the canonical deployment guide. It covers
digest-pinned Compose, separate migration-owner/application database roles,
secret files, HTTPS and proxy boundaries, bootstrap, and OIDC overrides.

- [PostgreSQL operations](docs/postgres-operations.md)
- [OIDC provisioning and offboarding](docs/oidc.md)
- [Secrets and runner bindings](docs/secrets.md)
- [Metrics and operations status](docs/observability.md)

## Releases

Dispatch the release workflow from `main` with the intended version and exact
commit SHA. GitHub Actions validates the commit before tagging, then requests
protected-environment approval and publishes the multi-architecture image.
Failed attempts may reuse a version until an image, release, or release asset
is published. Published versions are immutable. Local release gates are unsigned
and do not publish. See [release trust](docs/release-trust.md).

## Documentation

[Documentation index](docs/index.md) · [Architecture](docs/architecture.md) ·
[Product specification](docs/product-spec.md) · [Product plan](docs/plan.md) ·
[Compatibility ledger](docs/compatibility.md) · [API contract](openapi.yaml)
