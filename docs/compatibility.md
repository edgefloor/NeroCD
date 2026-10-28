# Hosting compatibility

NeroCD implements parts of Coolify's hosting workflows. It does not yet provide
complete feature parity, a supported Coolify migration, or unchanged-client
compatibility. These are separate claims with separate tests.

The comparison uses Coolify
[`b12d1386f3d458618b4c579babcd1e339c6c40b3`](https://github.com/coollabsio/coolify/tree/b12d1386f3d458618b4c579babcd1e339c6c40b3).
The full target remains in the [product specification](product-spec.md).
The [implementation plan](plan.md) orders remaining work. Shared workspaces and
coding agents are [future scope](workspace-capabilities.md).

## Status and evidence

| Status | Meaning |
| --- | --- |
| planned | Required behavior without accepted implementation evidence |
| partial | Tested behavior covers a bounded part of the capability |
| implemented | Code exists, but acceptance of its declared scope is incomplete |
| validated | Named fixtures pass at a named revision within stated limits |
| intentionally different | A declared behavior difference has explicit support |

Fixture names in the matrices below are required checks, not a list of passing
tests. Historical results apply to their original revision and profile. Combining
separate passing fixtures does not establish a complete hosting workflow.

## Tested profiles

Most physical hosting qualification uses native Linux arm64, a trusted rootful
Docker daemon with the classic image store and overlay2, and one destination.
HTTPS fixtures use a private CA. Public DNS and production ACME remain
unqualified. Linux amd64 compilation does not establish native runtime support.

Docker administrators can inspect container environments. Project permissions
and rootful Docker do not isolate hostile workloads. Untrusted execution remains
disabled until its declared isolation requirements are met. Cross-host transfer,
external storage, host-disk loss, and engine-version upgrades require their own
qualification.

<a id="accepted-foundations-and-evidence"></a>
## Tested hosting workflows

The table links each tested workflow to its guide or executable fixture. Older component
tests remain in Git history and do not expand these limits.

| Workflow | Behavior and limits |
| --- | --- |
| Image deployment | [Public image execution](hosting-execution.md) and [bounded private pulls](hosting-image-deployments.md#private-registry-qualification): immutable image/platform/daemon receipts, ordinary loopback HTTP readiness, fenced apply, cancellation and exact completion replay. [The UI](hosting-image-deployments.md) adds history and retained-request recovery. Fixed-port replacement can interrupt service; allocating another port does not switch traffic. |
| Local Git builds | [Configured build runner](hosting-build-runtime.md): public HTTPS Dockerfile, Railpack and prepared static assets through API/River/compiled CLI. Explicit server policy, owned runner configuration and matching persisted capabilities are required. Disk use is monitored, not a quota. |
| Same-host artifact deployment | [Deploy the recorded local artifact](hosting-local-build-runtime.md) with exact ImageID, platform and daemon identity. No pull, rebuild or cross-host delivery. [UI qualification](hosting-build-deployments.md) is separate. |
| Private HTTPS Git | [Owner-created version grants](hosting-git-resources.md#request-a-private-https-git-build), protected credential transport, compiled CLI Dockerfile/Railpack and Dockerfile UI. Credentials are cleared before build use. No connected SSH-key workflow or arbitrary provider/language coverage. |
| Private Railpack static provider | [Fixture](../acceptance/runtime-hosting-local-build/README.md) covers grant retry, build/output, exact deployment, trusted HTTPS and preservation after failed rebuild. Earlier unexplained combined failures remain a reliability limit. Full A03 is open. |
| Compiled Vite SPA | [Pinned fixture](../acceptance/runtime-hosting-local-build/README.md) uses Node 22.22.0, Bun 1.3.6, Vite 8.2.2 and Caddy 2.10.2. A genuine parser failure preserves the old application. Earlier unexplained failures remain a reliability limit; broader frontend coverage and full A04 are open. |
| Build output | [Delivery](hosting-build-log-delivery.md) persists typed receipts and retention cursors before completion, with journal replay, cancellation and renewal. [Viewer](hosting-build-logs.md#resource-build-output-viewer) reads are bounded and revalidate access. Old pre-0051 history is unknown; known-material redaction does not remove arbitrary secrets. |
| Runtime variables and secrets | [Runtime environment setup](hosting-destinations.md) applies public literals and exact managed-secret versions through protected Docker stdin. Delivery rechecks original credentials after authorization, retains its bounded window, and cannot recall delivered bytes. The resource UI saves exact versions and deployment generations. |
| Runtime status | [Observation](hosting-runtime-observation.md) distinguishes current, stale and historical evidence. The opt-in collector selects four targets fairly per 10-second cycle; this is not a per-resource interval guarantee. Disk/CPU metrics remain planned. |
| Runtime logs | [Collector](hosting-runtime-logs.md) and [viewer](hosting-runtime-log-viewer.md) cover fresh Docker stdout/stderr, 30-second Follow demand, retention/source gaps and access revocation. Trusted Unix Docker with json-file/local drivers only; additional TTY proof uses synthetic provenance. Delivery is not exactly once and application output can contain secrets. |
| Manual enrollment and readiness | [Readiness fixture](server-readiness.md#runtime-verification-fixture) covers generated-command enrollment, ready/blocked/stale states, role loss and persisted-credential restart. CLI metadata only; Docker API 1.54 and 1 GiB per work/journal root. Full A01 is open. |
| SSH onboarding | [Prepared-host workflow](hosting-ssh-onboarding.md) uses native arm64 Debian 12, pinned SSH identity and response-loss inspection recovery. The four-capability baseline covers image hosting, not source builds, proxies, logs or storage. |
| Traefik routing | [Managed routing](hosting-proxy-ui.md) has actual HTTP and trusted HTTPS, hostname grants, generation checks and withdrawal. [Agent execution](hosting-proxy-control.md) retains holds/replay and autonomous renewal during control-plane outage. No public ACME, DNS-01, wildcard/custom certificates or Caddy qualification. |
| Verified route switching | [Route switching](hosting-route-switch.md) checks old/new trusted TLS and rollback under exact healthy/retained-running release authority. [Recovery](hosting-proxy-recovery.md) settles eligible completed metadata conflicts; uncertain effects remain held. Recorded metadata settlement does not prove convergence. |
| GitHub push builds | [Signed GitHub.com pushes](hosting-github-push.md) bind one saved branch and independently verified current head, deduplicate commits and ignore delayed older events. The provider is simulated over TLS in qualification; live webhook setup is manual. |
| Automatic deployment | [automatic_unprotected](hosting-auto-deploy.md) is opt-in after a healthy manual release and existing routes. Protected production-plan approval and PR/MR previews remain planned. |
| PostgreSQL/Redis capture and alias maintenance | [Compiled-runner fixture](../acceptance/runtime-hosting-database-runner/README.md) checks exact-ID/automatic origin capture, same-claim retry, recovery, retained data and alias reads/writes. Empty-network upgrade changes network/alias metadata, not the database engine version. Automatic ACK replay with Docker absent does not prove successful exit of the whole continuous runtime. |
| Connected application and uploads | [Connection guide](hosting-database-connections.md): one image application, PostgreSQL or Redis, then one owned named volume, through migration 0074 and journal 17. Routed replacement, reviewed interruption, recovery and fresh deployment retain records/uploads. Arbitrary nonroot initialization, cross-host/external storage and full R42/A17 remain open. |
| Local archive download | [Accepted single-controller download](hosting-storage-archive.md#local-download-qualification) returns exact USTAR bytes without another backup or writer interruption. Physical Compose proof and ordinary-image HTTP/runner proof have separate limits. Both opt-in flags and archive configuration are required. |
| Local owned-upload recovery | [Archive workflow](hosting-storage-archive.md) restores into a new resource/volume, verifies application reads/new writes and preserves the source. Committed ACK loss starts each helper once. Classic-store helper lookup only; the recorded containerd-store failure has no runtime fallback. Intercepted UI tests alone do not prove physical recovery. |
| PostgreSQL immediate recovery | [Logical backup and new-private-database restore](hosting-database-backups.md) with application reads/new writes and exact completion replay. PostgreSQL 17.11, one destination, migration 0075/journal 18; no in-place restore. |
| PostgreSQL schedules | [Schedule guide](hosting-database-backup-schedules.md) includes IANA timezones, durable occurrences, restart, and guest pg_dump deadline proof. Full cron grammar, including weekday 7, L, W and #, is not supported. |
| Remote PostgreSQL recovery | [Remote backup guide](hosting-database-remote-backups.md): versioned encrypted targets, local preservation after upload failure, same-archive retry, ACK replay and fresh remote GET before recovery. Pinned Silo on trusted loopback HTTP; production S3 HTTPS remains unqualified. |
| PostgreSQL retention | [Independent local/remote retention](hosting-database-backup-retention.md), immutable-batch replay, explicit continuation and recovery from retained copies. Same trusted-loopback Silo profile. |
| Backup failure notifications | [SMTP and HTTPS webhooks](hosting-backup-notifications.md) for PostgreSQL backup/upload failure, durable attempts/history and explicit recovery links. Dump-failure state can remain uncertain. Private-CA notification TLS; other events/channels and public-CA deployment remain open. |
| Remote owned-upload recovery | [Uploads-only remote targets](hosting-storage-remote-backups.md), separate local/upload results, same-archive retry, fresh GET before extraction and new-volume application reads/writes. Source remains usable. |
| Owned-upload schedules and alerts | [Schedule guide](hosting-storage-backup-schedules.md): replay/edit/disable, restart, archive/upload execution and SMTP/HTTPS failure alerts. Delivered links open without mutation; recovery requires an explicit action. |
| Owned-upload retention | [Retention guide](hosting-storage-backup-retention.md): reviewed policy/preview/execute, exact retries, independent local/remote removal and retained-copy recovery. No broader mount or cross-host recovery claim. |
| Selected backup-copy deletion | [Deletion guide](hosting-storage-backup-deletion.md): selected local/recorded-remote copies, explicit final-copy confirmation and preserved history.; this does not delete resource volumes or all backups. |
| Catalog applications | [Linkding 1.47.0](hosting-catalog.md) install/retry, credentials and separate-target recovery; [Kuma 2.5.8 SQLite setup/persistence](hosting-catalog.md#uptime-kuma-qualification); [Actual Budget 26.10.0 server-data persistence](hosting-catalog.md#actual-budget-qualification). [Vikunja/PostgreSQL catalog install/deploy](hosting-catalog.md#catalog-compose-qualification) and [local category/search filters](hosting-catalog.md#catalog-filter-qualification) are accepted. Each retains its architecture, recovery and fixture-HTTPS limits; general templates and upgrades remain unqualified. |

<a id="r42-remains-an-unfinished-hosting-workflow"></a>
### Application database connections

R42 remains incomplete beyond the bounded image-application profile. Broader
application inputs, credential rotation and explicit unreachable cross-server
binding presentation need separate tests. PostgreSQL backup qualification does
not establish Redis recovery or complete A17/A26/A37.

<a id="retained-physical-and-ui-evidence"></a>
<a id="retained-earlier-foundations"></a>
### Evidence boundaries

Physical runtime, real API/browser workflows, intercepted UI tests, source tests
and compile checks prove different things. A component test does not qualify a
connected workflow. Preserve the original revision when reusing evidence and
rerun checks affected by a shared engine change. Earlier standalone database passes predate later shared-engine changes and
do not qualify the current combined tree. Earlier failed runs
remain failed at their original revisions.

## Product areas

The C references resolve in the specification's [source list](product-spec.md#13-sources). Source labels identify the reference capability, not proof of NeroCD support.

| Product area | Upstream reference | Current support and limits | Required fixtures | Package and deliberate difference |
| --- | --- | --- | --- | --- |
| Server management | C4, C11 | partial: manual enrollment, destination grants, readiness diagnostics and prepared-host SSH onboarding; broader hosts and lifecycle remain planned | A01, A13, A14, A27; server-reenroll, host-key-mismatch, old-agent-capability | P02; persistent Go agent instead of upstream deployment over SSH |
| Resource organization | C1, C10 | partial: project environments, typed resource configuration and per-binding mutation admission; team ownership, legacy mappings and broader concurrency remain planned | A08, A34, A35; legacy-membership-preservation | P01; preserve legacy IDs and explicit environment grouping |
| Git sources | C2 | partial: public/private HTTPS builds and saved-branch GitHub push events; broader providers and source choices remain planned | A03, A09; provider matrix below | P04, P18; no-Git inputs need no artificial repository |
| Builds | C2, C12, C13 | partial: Dockerfile, Railpack, prepared static and one compiled SPA fixture, typed output and exact same-host deployment; language coverage, remote publication and shared cache remain planned | A02–A07; builder and architecture matrices below | P05; bounded public source-to-local-artifact/deployment evidence does not establish the complete hosting or workspace journey |
| Inputs without Git | C2, C3, C8 | partial: public and [credential-selected private image pulls](hosting-image-deployments.md#private-registry-qualification), plus [four pinned catalog entries](hosting-catalog.md#current-catalog-criteria) and [saved managed Compose](hosting-compose.md#private-registry-qualification); stored Dockerfile and raw/Git/build Compose remain planned | A05, A15, A21; no-git-source | P04, P08, P11 |
| Build placement | C4 | partial: exact configured runner and daemon; dedicated builders and registry delivery remain planned | A07, A13; registry-push-pull, architecture-mismatch | P02, P05; tags never relocate stateful destinations |
| Domains and HTTPS | C5 | partial: managed Traefik, hostname grants and route UI with private-CA fixtures; public DNS/ACME and other modes remain unqualified | A20, A27; proxy matrix below | P07; proxy reconciliation and route ownership have separate generations |
| Deployment automation | C2, C14, C15 | partial: manual deployment, exact retries, retained cleanup, verified route switching, eligible metadata-conflict recovery and optional automatic_unprotected deployment; protected plans remain planned | A06, A09–A14; approval-invalidation, retained-release | P04, P06; optional approval must bind prepared release and destination |
| Previews | C6 | planned: derived resources, isolated configuration, routes and storage | A23, A24; late-close-event, quota-exhaustion | P12; hostile forks require isolated pools or rejection |
| Persistent storage | C17 | partial: owned named-volume metadata, deployment admission, single-writer replacement, uploaded-file recovery, schedules, retention and selected backup-copy deletion; broader mount types and unknown-writer recovery remain open | A19, A26, A37; external-volume-retention, ownership-transfer | P08; resource deletion retains data by default |
| Databases | C7 | partial: PostgreSQL 17.11 and Redis 8.2.10 private hosting, capture/replay, empty-network alias maintenance and bounded same-destination image-application connections; PostgreSQL logical backup, new-resource restore, schedules, remote copies, retention and failure notifications; broader profiles, rotation and engine-version upgrades remain open | A17, A18, A26; engine matrix below | P09; replication and an availability SLA are not implied |
| Service catalog | C8 | partial: [local category/search filters](hosting-catalog.md#catalog-filter-qualification), Linkding install/recovery, [Kuma persistence](hosting-catalog.md#uptime-kuma-qualification) and [Actual Budget server-data persistence](hosting-catalog.md#actual-budget-qualification) and [Vikunja/PostgreSQL catalog deployment](hosting-catalog.md#catalog-compose-qualification); [selected-template destination eligibility and requirements](hosting-catalog.md#destination-requirements-qualification) are accepted; hardware/capacity verification, general templates and upgrades remain open | A21, A22; catalog-version, component-upgrade | P11, P18; initial tested catalog expands through explicit version fixtures |
| Variables and secrets | C16 | partial: runner-file bindings, encrypted managed versions, audited reveal/revoke, private Git transport and exact-version runtime delivery; shared scopes, providers and broader recovery remain planned | A03, A18, A35; scope-denial, redaction, key-recovery, revoked-version | P03; separate build/runtime/preview grants; external-provider versions explicit |
| Ongoing operations | C9 | partial: build output, runtime status, bounded runtime logs and viewer; broader actions and terminals remain planned | A28, A29, A32; stale-observation, log-reconnect | P13, P15; desired state, operation result and observation remain separate |
| Recovery | C18–C20, C15 | partial: platform backups and restore holds, bounded image/proxy recovery, PostgreSQL new-resource recovery and owned-upload new-volume recovery; broader restore/resume and disk-loss contracts remain open | A25–A27, A37; resume-failure, new-resource-restore | P10; platform, database, storage and release rollback are distinct |
| Automation | C21, C22 | partial: PostgreSQL and owned-upload backup schedules, retention, and backup/upload failure alerts over SMTP or HTTPS webhook; general resource commands, full cron grammar and broader channels remain planned | A25, A30, A31; notification matrix below | P14; occurrence identity, DST, overlap and missed-run policies required |
| Programmatic access | C1 | partial: native REST, selected CLI commands and generated frontend client; complete hosting CLI/MCP and broader interface parity remain planned | A35, A36; interface-parity, keyboard-mobile | P16; every interface uses the same authorized application operations |
| Migration | C23 | planned: NeroCD upgrade mappings and Coolify compatibility report/cutover | A33, A34; controller-handoff, post-cutover-writes | P17; existing-container adoption requires explicit ownership transfer |
| Broader infrastructure | C1, C4, C5 | planned: multi-server deployment, Swarm, provisioning, Actions runners and log integrations | infra-multi-server, infra-swarm, infra-provisioning, infra-actions-runner, log-drain | P18; explicit extended parity, with no Kubernetes dependency |

## Database engines

The reference backup/restore distinctions below are retained from R44. PostgreSQL and Redis have bounded accepted hosting and direct origin/alias-upgrade evidence above; the other engines remain planned. Common container lifecycle code does not prove engine-specific backup, restore, credential rotation or major-version upgrade support.

| Engine | Status | Reference scheduled backup | Reference dashboard restore | Supported limits and required outcome | Planned fixtures |
| --- | --- | --- | --- | --- | --- |
| PostgreSQL | partial, bounded private hosting and direct alias upgrade accepted | Yes | Yes | PostgreSQL 17.11 private hosting, same-destination image-application connection and alias maintenance; logical backup/new-private-resource restore, bounded schedules, remote copies, independent retention and failure alerts. Qualification uses trusted loopback Silo and private-CA TLS. Production S3 HTTPS, broader R42, rotation and engine upgrades remain open | db-postgresql-lifecycle, db-postgresql-restore, A17, A18, A26 |
| Redis | partial, bounded private hosting and direct alias upgrade accepted | No | No | Redis 8.2.10 private hosting, retained volume/network/credential identity, capture/replay, empty-network alias maintenance and same-destination image-application connection. Engine backup/restore, rotation and broader R42 remain open | db-redis-lifecycle, db-redis-persistence |
| MySQL | planned, M3 | Yes | Yes | Engine-specific backup/import and application verification | db-mysql-backup-restore, db-mysql-upgrade |
| MariaDB | planned, M3 | Yes | Yes | Separate version/configuration tests from MySQL | db-mariadb-backup-restore, db-mariadb-upgrade |
| MongoDB | planned, M3 | Yes | Yes | Supported authentication and dump/import formats | db-mongodb-auth, db-mongodb-backup-restore |
| ClickHouse | planned, M3 | Yes | No | Backup parity; explicit restore procedure or labeled improvement | db-clickhouse-backup, db-clickhouse-restore-procedure |
| Dragonfly | planned, M3 | No | No | Hosting and persistence with explicit recovery limits | db-dragonfly-lifecycle, db-dragonfly-persistence |
| KeyDB | planned, M3 | No | No | Hosting and persistence with explicit recovery limits | db-keydb-lifecycle, db-keydb-persistence |
| SQLite | planned, experimental | Yes, experimental | Yes, experimental | Retain experimental label until versioned backup/restore fixtures pass | db-sqlite-experimental-backup-restore |

Every supported engine/version pair also needs readiness, credential rotation, network reachability, storage retention, incompatible-version rejection and application read/write evidence. No engine inherits these results from another row.

## Sources and providers

| Provider or input | Reference | Status and delivery | Limits and planned fixtures |
| --- | --- | --- | --- |
| GitHub | C2, C6 | partial: signed fixed-branch GitHub.com build-on-push accepted with manual deployment; broader integration planned | Public/private Git, installation credentials, branch/commit selection, signed webhook receipts, duplicate/delayed events, PR status and cleanup; git-github, A03, A09, A23 |
| GitLab | C2, C6 | planned, M3/P18 | Provider-specific credentials, signatures, MR events and status; git-gitlab |
| Gitea | C2 | planned, M3/P18 | Provider-specific authentication, webhook and preview events; git-gitea |
| Bitbucket | C2 | planned, M3/P18 | Provider-specific authentication, webhook and preview events; git-bitbucket |
| Generic Git and deploy keys | C2 | partial component, P04 | Public and granted private HTTPS are connected product paths. The standalone checkout component also supports callback Basic credentials and pinned-host SSH keys; these do not establish a connected deploy-key workflow. Fixtures: git-generic, git-deploy-key |
| Signed manual deploy webhook | C2 | planned, P04 | Authentication, durable delivery identity, retries and supersession; deploy-webhook, A09 |
| Registry image tag/digest | C2 | partial: public images and [private ordinary-image pulls](hosting-image-deployments.md#private-registry-qualification) plus [one selected registry in saved Compose](hosting-compose.md#private-registry-qualification) and [Git Compose](hosting-compose.md#git-compose-private-registry-qualification), P04/P05/P06 | Explicit secret/version selection supports stateless, owned-storage and matching Compose service images, with saved runtime configuration and routed replacement. Denied pulls preserve the previous workload and uploads. Server registry inventory, login/logout, push, multiple selected private registries, build-base authentication and standalone database/single-artifact local-build selection remain outside qualification; image-tag-digest, A05 |
| Stored Dockerfile | C2 | planned, P04/P05 | Versioned input and context without fake repository; dockerfile-stored |
| Git Compose and pasted Compose | C3 | partial: [saved managed Compose](hosting-compose.md) and [private HTTPS Git mixed build/image Compose](hosting-compose.md#git-compose-qualification), P08 | Original-source CAS, immutable Git receipts, shared-image aliases, persistent volumes, routed redeployment and Stop/Start are qualified. [One selected service-image registry](hosting-compose.md#git-compose-private-registry-qualification) is also qualified for Git replacement and uncached Start, separately from source grants. [Explicit recovery of unresolved stack proxy queuing](hosting-compose.md#stack-route-continuity-recovery) preserves completed workload evidence. [Public build arguments](hosting-compose.md#compose-build-argument-qualification) qualify configured literals and Compose map/list resolution with retained data. Raw grammar, binds and cross-daemon transfer remain open; broader A15, A16 incomplete |
| Catalog installation | C8 | partial, four pinned entries with [local search/category filters](hosting-catalog.md#catalog-filter-qualification) | Linkding install/recovery, [Kuma setup/persistence](hosting-catalog.md#uptime-kuma-qualification) and [Actual Budget setup/server-data persistence](hosting-catalog.md#actual-budget-qualification), plus [Vikunja/PostgreSQL catalog deployment](hosting-catalog.md#catalog-compose-qualification); [selected-template destination requirements](hosting-catalog.md#destination-requirements-qualification) are accepted; app-specific recovery, broader definitions, hardware/capacity verification and upgrades remain open; A21, A22 |

R33 requires Doppler, Infisical and Vault adapters as planned parity work in P18.
Each provider's contract and supported versions remain unselected. No provider
integration is implemented or validated by this ledger.

| External secret provider | Reference | Status | Contract decisions and planned fixtures |
| --- | --- | --- | --- |
| Doppler | R33, C16 | planned, P18 | Define supported versions, authentication, scope, immutable-version or mutable-reference semantics, absent/revoked-value behavior and recovery; secret-doppler-auth, secret-doppler-version, secret-doppler-revocation, secret-doppler-recovery |
| Infisical | R33, C16 | planned, P18 | Define supported versions, authentication, scope, immutable-version or mutable-reference semantics, absent/revoked-value behavior and recovery; secret-infisical-auth, secret-infisical-version, secret-infisical-revocation, secret-infisical-recovery |
| Vault | R33, C16 | planned, P18 | Define supported versions, authentication, scope, immutable-version or mutable-reference semantics, absent/revoked-value behavior and recovery; secret-vault-auth, secret-vault-version, secret-vault-revocation, secret-vault-recovery |

## Builders and execution platforms

| Strategy | Reference | Status and planned slice | Limits and planned fixtures |
| --- | --- | --- | --- |
| Dockerfile | C2 | partial, public/private HTTPS path | Production controller/CLI and bounded UI acquire public or granted private HTTPS Git and run the confined Dockerfile/context/target path into an immutable artifact. Same-host deployment, typed output and [public build variables with optional multistage ARG injection](hosting-git-resources.md#dockerfile-argument-qualification) are qualified. Build secrets, private build-base authentication and remote publication remain open; build-dockerfile, A02, A06 |
| Railpack | C12 | partial, public/private CLI and static-provider UI path | Pinned 0.40.1 planner/frontend executes through public or granted private HTTPS Git/controller/CLI with bounded helper and immutable artifact output; upstream Beta label retained. Same-host deployment and typed output are accepted; private CLI credential use is accepted; private static-provider UI qualification is accepted above; full language matrix and remote publication remain planned; build-railpack, A03 |
| Nixpacks | C2 | planned, separate M3 adapter | Never infer support from Railpack; version/language fixtures; build-nixpacks |
| Prebuilt static files | C13 | partial, public build path | Public Git/controller/CLI packages selected prepared assets and opt-in SPA fallback with pinned nginx and retained receipts; same-host artifact deployment is accepted; this row covers prepared assets only; build-static, A04 |
| Compiled static/SPA | C13 | partial, pinned Vite fixture accepted | [compiled fixture contract](../acceptance/runtime-hosting-local-build/README.md). Explicit build command/output directory with pinned Node/Bun/Vite/Caddy; parser failure preserves the prior app. Earlier unexplained failures remain a reliability limit; broader frontend coverage, complete A04 and production ACME remain pending; baked configuration requires rebuild; build-spa, A04 |
| Compose build sections | C3 | partial: [Git mixed build/image qualification](hosting-compose.md#git-compose-qualification), P05/P08 | Confined Dockerfile contexts/targets, exact local artifacts, shared-image aliases and registry preparation before mutation are qualified with initialization and retained data across redeploy/Stop/Start. [Selected registry-only service pulls](hosting-compose.md#git-compose-private-registry-qualification) are qualified without changing local build/alias identity. [Public build arguments](hosting-compose.md#compose-build-argument-qualification), including multiline configured values and mapping/list interpolation, are qualified without implicit runtime import. Build secrets, build-base authentication, multiple selected registries, remote contexts and cross-daemon transfer remain open; broader build-compose, A15, A16 incomplete |
| Dedicated builder | C4 | planned, M3 | Authorized registry delivery, cache identity, architecture/pull validation; build-remote, A07 |

The [build-engine toolchain table](hosting-build-engine.md#host-and-toolchain-contract)
pins the accepted component and executable identities. The engine accepts one
Linux platform on its fixed native daemon; package receipts and the real fixture
cover that bounded local output. This does not claim a complete linux/amd64 and
linux/arm64 build-to-destination matrix or cross-host transfer. Each product
strategy still needs explicit architecture/version fixtures, credentials,
shared cache/retention controls and cross-host build-to-deploy evidence. Disk mode is
monitored with no finite overshoot guarantee, not a quota. Cancellation,
CPU/memory enforcement and expanded-output limits have bounded engine evidence
in the linked engine fixtures.

Compose compatibility includes managed and raw modes, service fields, interpolation required/default behavior, profiles, networks, volumes, operator labels, advanced host capabilities and versioned Docker Compose differential tests. Raw mode remains planned in P18; preflight rejects a protected policy if its release guarantees cannot be satisfied.

## Proxy adapters

| Adapter | Reference | Status and planned slice | Limits and planned fixtures |
| --- | --- | --- | --- |
| Traefik | C5 | partial engine and backend, P07 | Standalone real TLS/renewal/state recovery; control-plane grants, settled binding targets, generation holds and River dispatch accepted. Explicitly capable agent runtime and managed routing UI have real HTTP/trusted-test-CA fixtures; production DNS/ACME and extra certificate/proxy modes remain unvalidated; proxy-traefik, A20, A27 |
| Caddy | C5 | planned, P18 | Explicit configuration and migration adapter; proxy-caddy |
| Custom proxy | C5 | planned, P18 | Declare operator/platform ownership and unavailable managed features; proxy-custom |
| Proxy migration | C5 | planned, P18 | Validate ownership transfer, traffic continuity, certificate retention and recovery; proxy-migration |

Standalone Traefik evidence covers real HTTP-01, trusted TLS renewal and
coordinated state recovery in the linked engine fixtures.
DNS-01/wildcards, custom certificate rotation and DNS credential protection
remain planned. Route authorization and generation/hold ownership have backend
coverage using backend fixtures and the accepted combined Linux runtime and
browser gates. The managed image-to-routing path has isolated HTTP/trusted-HTTPS
coverage; this does not validate public DNS or production ACME. Same-host public source-build handoff and explicit managed route selection are accepted.
Continuous monitoring and additional certificate/proxy modes remain required.
A server's existing external proxy network does not prove managed routing
support, and the image-deployment UI's loopback HTTP result is not an HTTPS URL.

## Notification channels

Pinned source evidence is [Team notification configuration](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Models/Team.php), C22.

| Channel | Status and slice | Supported limits | Planned fixtures |
| --- | --- | --- | --- |
| Email | validated bounded database/upload backup failure slice | Encrypted write-only SMTP configuration, current project recipients, explicit backup/upload-failure event selectors, durable attempts and explicit recovery; private-CA TLS qualification | [Accepted notification qualification](hosting-backup-notifications.md#accepted-qualification-and-durable-evidence); [Upload schedule alerts](hosting-storage-backup-schedules.md) have separate qualification; full channel/event matrix and A25 remain open |
| Generic webhook | validated bounded database/upload backup failure slice | Encrypted write-only HTTPS endpoint, explicit backup/upload-failure event selectors, durable identities, bounded retry and explicit recovery; private-CA TLS qualification | [Accepted notification qualification](hosting-backup-notifications.md#accepted-qualification-and-durable-evidence); [Upload schedule alerts](hosting-storage-backup-schedules.md) have separate qualification; full channel/event matrix and A25 remain open |
| Discord | planned, M3 | Separate channel adapter and delivery evidence | notify-discord |
| Slack | planned, M3 | Separate channel adapter and delivery evidence | notify-slack |
| Telegram | planned, M3 | Separate channel adapter and delivery evidence | notify-telegram |
| Pushover | planned, M3 | Separate channel adapter and delivery evidence | notify-pushover |

Each channel must redact credentials, preserve resource/project authorization, retain execution history and report partial outcomes. A successful database dump followed by failed upload must generate a useful notification while preserving local recovery data.

## Migration and client compatibility claims

Feature equivalence does not imply drop-in Coolify database or API compatibility. Native API/CLI/MCP support and Coolify client adapters have separate fixtures. Requested adapters require explicit endpoints, request/response/error contracts and version limits before a support claim.

NeroCD upgrades begin in M0 and preserve old IDs, approvals, effective permissions and in-flight execution semantics. Equal environment names are not an automatic grouping rule. Coolify import starts with read-only discovery and a versioned intermediate manifest. Missing secrets remain missing. A33 requires source automation suspension, final data synchronization, explicit traffic handoff, destination automation activation and a recovery policy for writes after cutover.

No full NeroCD domain migration, Coolify migration or unchanged-client
compatibility is validated at this assessment. Additive hosting and River
migration checks cover only their stated schema boundaries. Existing-container
adoption is a separate capability; discovered labels do not prove surrender of
the previous controller's authority.


## Current connected stateful qualification

The current bounded workflow is summarized in the
[qualification table](#accepted-foundations-and-evidence). The
[connection guide](hosting-database-connections.md) describes setup and recovery.
Owned-volume replacement stops the exact predecessor before admitting the new
writer. Ready yield, proxy application and finalization have separate receipts.
Recovery preserves candidate-written records/uploads and does not undo writes.

## Connected application route continuation qualification

The earlier storage-less workflow uses migration 0073 and journal format 16.
The candidate passes readiness while the old application remains healthy. Strict
HTTPS switching precedes a fresh finalize lease that retires the predecessor.
Lost yield, proxy completion and final completion acknowledgments replay retained
receipts without repeating effects or credential delivery.

This profile does not include application storage, source builds, cross-server
binding presentation, credential rotation, database-native restore, disk loss,
public ACME, in-flight request draining or control-plane restart proof. The later
owned-volume profile has its own interruption and single-writer contract.
