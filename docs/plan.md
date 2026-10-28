# Hosting implementation plan

Complete the self-hosted hosting workflows in the pinned Coolify reference
before the broader workspace and coding-agent product. The
[specification](product-spec.md) retains R01–R102 and A01–A66. Current work
prioritizes hosting R01–R68, A01–A37 and their capability matrices.

## Primary hosting journey

> Connect a server → select a repository or image → configure the application → deploy → open its HTTPS URL → operate and recover it.

Direct hosting is the primary product navigation and onboarding journey until
core hosting replacement is complete. A Task, coding agent, workspace or
operational template is never required to deploy or operate an ordinary application.
R65's richer Task-centered main collaboration view remains a future phase.

Judge progress by complete UI/API workflows, including failure and recovery.
Module count, lines changed and unit-test totals do not establish parity.
Feature parity, migration compatibility and unchanged-client compatibility
remain distinct acceptance claims.

## Accepted starting point

The platform already has the Go application, PostgreSQL/River, enrolled runners,
authorization, audit, managed secrets and embedded React UI. Extend those
components for new hosting behavior.

Current bounded workflows include image and HTTPS Git builds, same-host artifact
deployment, runtime configuration, status and logs, Traefik routing, GitHub push
builds and optional unprotected automatic deployment. PostgreSQL/Redis connections
and owned upload volumes have physical qualification on one trusted Linux arm64
profile. PostgreSQL and upload recovery now include selected schedules, remote
copies, retention and failure notifications. Pinned Linkding includes
separate-target application recovery. Pinned Uptime Kuma 2.5.8 now has
[accepted SQLite setup, routing and monitor/settings persistence across replacement](hosting-catalog.md#uptime-kuma-qualification). Actual Budget 26.10.0 has
[accepted server-password setup and fresh-context server-budget persistence](hosting-catalog.md#actual-budget-qualification). Saved managed Compose now
includes [catalog installation of Vikunja/PostgreSQL](hosting-catalog.md#catalog-compose-qualification)
and the qualified save, deploy, HTTPS, redeploy, component
logs, Stop/Start, database-only component backup/import, and local PostgreSQL 16
component schedules at `09cd2670154443f788963d9812aa6b77a2c919c8`. Immediate local
named-volume backup is accepted with product evidence at `02a5639` and reviewed
preservation replay at `37ddc31`; both original aggregate gate commands remain
failed. Local named-volume schedules passed a complete aggregate gate at
`1df3c90a1aef7b3f4235b4ba43eb239415b29e51`. See
[Compose qualification](hosting-compose.md#qualification).

Use [compatibility](compatibility.md#accepted-foundations-and-evidence) for tested profiles and limits. These results do not complete the hosting
requirements. Public production ACME, broader hosts, protected production plans,
remote builders and the remaining capability matrices still need qualification.

## Active delivery sequence

| Order | Complete hosting outcome | Requirements and acceptance |
| --- | --- | --- |
| H1: Practical hosting | Onboarding, repository/image input, private Git, required variables and secret delivery, trusted deploy-on-push, deployment policy, production TLS qualification, runtime logs and continuous status | Applicable R01–R33 and R57–R68; A01–A14, A20, A27–A29, A35 plus their failure/recovery and provider/proxy fixtures |
| H2: Useful stateful hosting | Compose, persistent storage, PostgreSQL/Redis, application bindings, initial tested catalog, backups and application-verified restores | R34–R53 and related secret/operation rules; A15–A22, A25–A27, A37 |
| H3: Remaining core parity | PR/MR previews, remote builders/image delivery, provider/build/database coverage, terminals, schedules, notifications and supported UI/API/CLI/MCP operations | Remaining core R01–R68/A01–A32/A35–A37 and declared matrices together on the candidate |
| H4: Migration and extended compatibility | NeroCD/Coolify migration, unchanged-client adapters where declared and extended infrastructure | R67, specification section 7, A33–A34 and dedicated compatibility matrices |

Use actual dependencies and verified gaps to select the next hosting workflow.
Legacy-ID/permission preservation and data-ownership protection ship with the
hosting behavior that needs them; H4 does not authorize delaying those safeguards.
A practical deployment demo alone does not complete Coolify parity.

## Next hosting slice

Continue H2 from the remaining stateful and catalog gaps. Select a complete user
workflow with a recoverable failure path before adding another internal helper.
Local PostgreSQL component schedules and immediate local named-volume backup are
accepted for the bounded managed Compose profile. Local schedules for one managed
Compose named volume are also accepted at `1df3c90`, including stable selection
across redeployment, a real due archive, disable, and failure compensation.
Immediate Compose volume backup with an optional validated S3 target is
[accepted at `f927e59`](hosting-storage-remote-backups.md#accepted-compose-remote-copy).
It preserves local success through upload failure and retries the same blob
without another archive or interruption. Scheduled remote copies
[passed qualification at `26998a435`](hosting-storage-remote-backups.md#compose-remote-schedule-qualification),
including local success through denied upload and same-archive retry.
Local archive download is [accepted at `ff6b200c`](hosting-storage-archive.md#local-download-qualification)
for the bounded single-controller profile, with native Compose browser proof. The pinned PHP reference has no
archive restore action: `VolumeBackupRecoveryJob` resumes stopped containers and
cleans partial remote uploads. Compose archive restore and coordinated database
and upload recovery are separate NeroCD expansions, not missing upstream restore
parity.

| Area | Existing bounded workflow | Remaining work |
| --- | --- | --- |
| Application and database | [One image application with PostgreSQL or Redis](hosting-database-connections.md), then one owned upload volume | Broader application inputs, credential rotation, cross-server binding presentation and full R42/A17 |
| PostgreSQL recovery | [Immediate backup/restore](hosting-database-backups.md), [schedules](hosting-database-backup-schedules.md), [remote copies](hosting-database-remote-backups.md), [retention](hosting-database-backup-retention.md), and [failure alerts](hosting-backup-notifications.md) | Production S3 HTTPS, additional engines, cross-destination and in-place recovery, broader cron grammar and notification events/channels |
| Upload recovery | [Local archives](hosting-storage-archive.md), [remote copies](hosting-storage-remote-backups.md), [schedules/alerts](hosting-storage-backup-schedules.md), [retention](hosting-storage-backup-retention.md), and [selected-copy deletion](hosting-storage-backup-deletion.md) | Broader mount types, unknown-writer recovery, cross-host/in-place recovery and full A25–A27/A37 |
| Catalog | [Local category/search filters and upstream declarations accepted at `6aeac387`](hosting-catalog.md#catalog-filter-qualification), [pinned Linkding install](hosting-catalog.md) and [separate-target recovery](hosting-catalog-recovery.md), plus [Uptime Kuma persistence at `397a8d826`](hosting-catalog.md#uptime-kuma-qualification) and [Actual Budget server-data persistence at `ca3b8dcd`](hosting-catalog.md#actual-budget-qualification), plus [Vikunja/PostgreSQL catalog install/deploy at `eeee82b6`](hosting-catalog.md#catalog-compose-qualification) and [selected-template destination eligibility/requirements at `985652b9`](hosting-catalog.md#destination-requirements-qualification) | Physical architecture/capacity checks, more tested definitions, explicit upgrades and broader application-specific recovery |
| Managed Compose | [Saved managed Compose with Vikunja/PostgreSQL](hosting-compose.md), including component logs, Stop/Start, database-only backup/import, local PostgreSQL component schedules, immediate and scheduled local named-volume backup, and immediate or scheduled remote copy with same-archive retry, and [local archive download](hosting-storage-archive.md#local-download-qualification), plus [private HTTPS Git mixed build/image stacks](hosting-compose.md#git-compose-qualification) with shared-image aliases, routed redeployment, Stop/Start persistence and [selected private service-image pulls](hosting-compose.md#git-compose-private-registry-qualification), plus [explicit stack proxy-queue recovery](hosting-compose.md#stack-route-continuity-recovery) without new writers or changed receipts, and [public configured/Compose build arguments](hosting-compose.md#compose-build-argument-qualification) with actual multiline image effects and retained data | Archive restore, bind mounts, raw Compose, build secrets, build-base authentication, multiple selected registries, cross-daemon artifacts remain open; broader A15/A16 incomplete |
| Private image pulls | [Ordinary images at `a1ce5a1b`](hosting-image-deployments.md#private-registry-qualification), [saved managed Compose at `c4b9ef45`](hosting-compose.md#private-registry-qualification), and [Git Compose at `db58c6ab`](hosting-compose.md#git-compose-private-registry-qualification): explicit credential versions, matching-service pulls before mutation, retained records/uploads/routes after denied pulls, and later valid redeployment/Start | Server registry inventory, login/logout, push, multiple selected private registries, build-base authentication, and selected credentials for standalone database/single-artifact local-build deployments remain outside this profile |
| Practical hosting | [SSH onboarding](hosting-ssh-onboarding.md), [runtime logs](hosting-runtime-logs.md), [automatic deployment](hosting-auto-deploy.md) | Broader host/capability qualification, public ACME, protected plans under R17/R19, live provider setup and complete A01/A09/A29 |

Keep the existing default-off flags and explicit capabilities. A successful
metadata save does not mount storage, configure a host or prove recovery.
Qualify application reads and new writes from restored data. Preserve uncertain
writer state and mutation holds until supported recovery reconciles them.

<a id="earlier-hosting-checkpoints"></a>
## Hosting implementation dependencies

Source configuration, credential grants, build execution and artifact deployment
have separate authority. A build must retain immutable input/output identities;
deployment must select the exact available artifact. Remote publication cannot
be replaced by an assumption that another host has the image.

Runtime configuration pins secret versions and generations. Delivery audits
metadata before decryption, rechecks the original credential before returning
bytes and preserves its original bounded retry window. Revocation cannot recall
bytes already sent. Docker administrators can inspect environment values, and
controlled log scans do not prove arbitrary output is secret-free.

Keep build, deployment and runtime logs distinct. Runtime status needs bounded
observations, freshness and stale/unknown states. Test response loss, canceled
authority, failed readiness and preservation of the serving release. Do not
interpret a metadata resolution as proof that a proxy converged.

## Current connected stateful workflow

The [connected workflow](hosting-database-connections.md) attaches one PostgreSQL
or Redis database on the application's destination, then one owned named volume.
Storage replacement stops the exact predecessor before admitting the replacement
as sole writer. Recovery preserves candidate-written records and uploads.

### Historical storage-less route qualification

The [earlier storage-less profile](compatibility.md#connected-application-route-continuation-qualification)
starts the candidate before retiring the old application. It has a different
replacement contract from an exclusive writable volume. Neither profile promises
in-flight request draining or universal zero downtime.

## Hosting-dependency test

Before starting any workspace-track change, the Leader's brief must state:

1. **Named hosting requirement.** Identify the specific R01–R68 requirement and user workflow it serves.
2. **Need now.** Show why the current hosting implementation needs it now or how it demonstrably simplifies that implementation now. General future usefulness is insufficient.
3. **Minimum scope.** Define the smallest required contract and owned change, excluding speculative workspace infrastructure.
4. **Hosting acceptance.** Name the applicable A01–A37 scenario and concrete test that will prove its value, including failure and recovery where relevant.

Do not start or integrate workspace work without this recorded test. Sunk effort
does not satisfy it. Remote builders, registry delivery, build caching, immutable
releases, deployment approvals, permissions and runtime observations can proceed
when justified by their hosting requirements. They use hosting contracts through
the existing Go application, PostgreSQL/River and runner authority, without
requiring a general Task, Workspace, model endpoint or reservation product.

Trusted-host deployment remains supported within its declared limits. Keep
untrusted execution disabled until its isolation requirements are actually
satisfied. A qualified VM backend may be implemented for a named untrusted
hosting/preview workflow, but is not a prerequisite for trusted-host deployment.
Never weaken isolation, credential or data ownership rules to accelerate parity.

## Preserved deferred checkpoints

| Work | Recoverable checkpoint | Status |
| --- | --- | --- |
| Durable Task metadata/activity backend | `6a3d04bc28e950d569cbc7705a5d1dbf8df8c5f5` | Unaccepted and deferred; not merged into the accepted baseline; partial R69/R74 only |
| Task UI | `560ef12499cd361c7c2eae5a8a7d310c33ce34a7` | Unaccepted and deferred; temporary DTO/backend wiring, Go/browser checks not run; no main-navigation integration |

No Task schema or UI is accepted product support. Check the then-accepted
migration inventory before any future allocation; a number reserved on an
unaccepted branch is not an applied migration.

## Deferred workspace gates and full future scope

### D0–D3: deferred workspace demonstration

|Gate                                |Deliverable                                                                                                                                                                   |Exit conditions                                                                                                                               |
|------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------|
|D0: Machine and authority foundation|Shared environment/resource model, Task/run distinction, stable machines, one qualified Linux VM backend, capability checks, reservations, managed secrets and task grants    |A bounded remote task survives client disconnect; unauthorized/stale work is rejected; legacy data and destination ownership remain valid     |
|D1: Retained work and shared builds |One coding-agent adapter, isolated checkout, source checkpoints, BuildKit on remote workers, registry cache, authorized artifact publication and explicit build identity      |Task produces a retained candidate; a second compatible builder imports cache; interruption and budget behavior are demonstrated              |
|D2: Working environment and handoff |App plus seeded isolated PostgreSQL preview, private ingress/TLS, runtime observations/logs, trusted checks against the final artifact, teammate review and supported takeover|Another user opens the task and working application after the agent run ends; evidence and permissions agree; cleanup preserves data ownership|
|D3: Reviewed deployment             |Target-specific plan, human production approval, same-artifact promotion, mutation fencing and supported failure recovery                                                     |The full journey passes together, including changed-source/plan, lost-worker, revoked-grant, missing-artifact and expiry cases                |

These are deferred workspace gates. D0–D3 do not define the first public demonstration, authorize ongoing workspace work or block supported trusted-host deployment. Retain every gate and acceptance target for the later workspace track.

Implement conservative complete-artifact reuse and verification reuse after input/evidence recording is dependable. The deferred workspace demonstration may rebuild through shared layer cache and rerun checks. Its schema and UI must already distinguish these outcomes so later reuse does not require misleading history.

### Retained H, P, M, R, O and V scope

H is active. P/M/R/O/V remain future tracks unless a bounded part passes the hosting-dependency test. Existing hosting needs in areas such as cache, observations or restores use their hosting contract, without making the broader workspace product a dependency.

|Track                            |Deliverable                                                                                                                                                               |Completion evidence                                                                                                 |
|---------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------|
|H: Core hosting and compatibility|Direct source/image/Compose hosting, automatic builders, broader databases/catalog, storage, operations, schedules, notifications and Coolify migration                   |Original hosting cases A01–A37 and declared parity matrices pass for the release candidate                          |
|P: Broader owned-machine pool    |Native Mac adapter, qualified Windows VM and trusted WSL paths, qualified NAS roles, additional architectures, power/availability policies and explicit cloud provisioning|Backend-specific isolation, placement, recovery, networking and capacity fixtures pass                              |
|M: Owned model services          |Endpoint integration, supported model lifecycle, native/GPU execution profiles, model storage and quotas                                                                  |Model/driver/device matrix, authorization and interruption/upgrade behavior pass; measured resource use is available|
|R: Reuse and larger workloads    |Conservative full-artifact/check reuse, additional cache transports where justified, retention/garbage collection and measured concurrency                                |Reuse invalidation, trust, source/fixture identity, artifact availability and resource accounting cases pass        |
|O: Application care              |Version-specific upgrades, guided data migration, tested restores, database-aware plans, eligible progressive delivery and drift reconciliation                           |Application/version-path fixtures and failure/recovery cases pass                                                   |
|V: Vendor packages               |Verified package identity, customer-controlled installation/update policy, compatibility and recovery metadata                                                            |Publisher/customer authority, revocation, version pinning and compromised-update scenarios are defined and tested   |

Smaller endpoint/cloud steps remain possible future boundaries, but neither general usefulness nor compatibility with a later design justifies starting them now. A current named hosting dependency and its acceptance test are required.

Additional proxy modes, raw Compose, provider/template breadth, multi-server application deployment, Swarm, GitHub Actions runner hosting, external secret/log integrations and requested Coolify API adapters remain visible compatibility entries. A generic shell job does not establish support for these workflows.

Self-hosted object storage, Wasm execution and a geographically distributed hosted edge remain compatible directions from the earlier hosting discussion. They are not prerequisites of the first task demonstration. Each needs explicit storage/runtime/region qualification and economics before being included in a supported offering. An ordinary VM image or uploaded Wasm module does not by itself establish a global edge product.

No delivery dates, staffing estimates or performance improvements are asserted. Order work by dependencies and demonstrated acceptance, then update the ledger with implementation evidence.


## Hosting scope remains complete

Earlier hosting packages remain a useful allocation map within H:

| Packages | Retained scope |
| --- | --- |
| P00–P03 | Product contract, project/resource migration, destinations and secrets |
| P04–P05 | Public/private sources, provider events, Dockerfile, automatic builders, static/compiled output, remote builders and cache |
| P06–P07 | Releases, deployment strategy, readiness, rollback, routing and certificate lifecycle |
| P08–P10 | Compose/storage ownership, all declared database engines, backup/restore and platform recovery |
| P11–P12 | Versioned catalog, upgrades and PR/MR previews |
| P13–P15 | Continuous operations, runtime logs/actions, schedules, notification channels and terminals |
| P16–P18 | Full authorized UI/API/CLI/MCP, migration and remaining provider/proxy/infrastructure adapters |

Retain PostgreSQL, Redis, MySQL, MariaDB, MongoDB, ClickHouse, Dragonfly, KeyDB
and experimental SQLite with their distinct recovery limits. Nixpacks and
Railpack remain separate adapters. Additional Git providers, raw Compose,
Caddy/custom proxy, external secret sources/log drains, multi-server deployment,
Swarm, provisioning and GitHub Actions runners remain visible backlog entries.
The [hosting matrices](compatibility.md) carry their individual fixtures.
A partial D gate cannot remove these obligations.

## Execution and validation

Use PostgreSQL transactions, River lifecycle, runner fences and durable journals.
Queue dispatch completion is distinct from workload completion. A database fence
cannot prove that a host process stopped, and agent narrative is not trusted check
evidence. Follow [AGENTS.md](../AGENTS.md) for repository work and independent
review requirements.

| Boundary | Required evidence |
| --- | --- |
| Task/schema/links | Actual PostgreSQL migrations, authorized cross-project reads/writes, retries, audit and immutable activity, old-ID compatibility |
| Host and VM | Real qualified backend/architecture, denied host/LAN access, local limits, disconnect, process fencing and uncertain-reservation recovery |
| Agents/checkpoints | Adapter start/observe/interrupt, excluded credentials, lost work, compatible restore and human takeover |
| Shared builds | Cache import on a second compatible builder, trust scopes, secret-generation invalidation, publication failure and platform identities |
| Checks/approval | Trusted check definitions, exact subjects, native/emulated labels, changed-policy invalidation and human production-plan authorization |
| Preview and data | Independent lifetime, private access/revocation, seeded data, all required components, repeated expiry cleanup and protected persistent data |
| Direct hosting | Existing runtime/browser gates plus engine-specific data, backup/restore, provider and proxy matrices as those features are added |
| Release | Current generated client and frontend artifacts, Go embedding and required combined/runtime/release checks on one candidate |

Retained evidence is labeled with its original revision and scope. New Task,
VM, remote cache, agent, preview and promotion targets need new real execution
evidence; old hosting CI does not prove them. No D0–D3, full hosting parity or
multi-OS/GPU qualification is declared complete at this baseline.
