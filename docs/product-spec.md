# NeroCD product specification: hosting replacement and future shared workspaces

Revision: 3, with hosting-first delivery amendment
Status: Adopted target specification; implementation status is recorded separately
Date: 22 October 2026
Supplied revision 3 baseline: edgefloor/NeroCD at `09d65d4ddcbfdf27e97158a9c08beb02a721818c`, a historical ancestor.

Hosting-first documentation baseline: `65311f30470f34d0d26165d1cd13325fbd05fca0`. Accepted application/source assessment: `f4a54506f388339ffe8acdd6b8ec4133690188fa`, reviewed on 2026-10-24. Native migrations extend through 0066. Unaccepted Task checkpoints are not current product support.
Product reference: coollabsio/coolify at b12d1386f3d458618b4c579babcd1e339c6c40b3, with the hosting baseline reviewed for revision 2 and workspace implementation references reviewed for revision 3
Repository path: `docs/product-spec.md`

The [workspace capability ledger](workspace-capabilities.md), [hosting compatibility ledger](compatibility.md), [implementation plan](plan.md), and [domain glossary](../CONTEXT.md) separate the complete target from accepted support.

## 1. Product objective

The active objective is a functional replacement for the self-hosted hosting
workflows in the pinned Coolify reference. Prioritize R01–R68, A01–A37 and the
declared hosting matrices. Direct hosting is the primary navigation and
onboarding journey until core hosting replacement is complete:

> Connect a server → select a repository or image → configure the application → deploy → open its HTTPS URL → operate and recover it.

Users must not need a Task, coding agent, workspace or operational template to
deploy or operate an application. Deliver practical hosting first, then useful
stateful hosting, remaining core parity, and migration/extended compatibility.
Feature parity, migration compatibility and unchanged-client compatibility
remain separate claims.

Revision 3's broader direction is retained in full as a subsequent track:
a shared workspace where remote teams and AI agents build, test, review and
deploy software using machines the team controls, also supporting a personal
cloud operated by one person.

> Give your team and AI agents a place to work that stays running when you go offline, powered by machines you control.

In that future collaboration experience, a Task carries its goal, source
revision and changes, human and agent activity, checkpoints, preview,
verification evidence and deployment history. A teammate can inspect the
result and continue supported work without reconstructing another person's laptop.

The deferred workspace demonstration remains assigning a repository task to an
agent on a team-owned machine, retaining progress while the submitting laptop
is offline, building with shared cache, publishing an isolated preview, and
letting a teammate review and approve the same artifact for deployment. It no
longer defines the first public demonstration or a prerequisite for hosting.

Remote execution still requires an available execution machine. A durable task
record can survive worker failure, but another host can continue only from a
supported checkpoint with compatible capacity. A gateway cannot keep an offline
application host running.

### 1.1 Meaning of "rewrite in Go"

Go implements the platform API, orchestration, controllers, workers, host agents, lifecycle rules and CLI. Retain the React/TypeScript application. Platform-specific helpers, including a potential native Mac virtualization helper, can use the host's supported language and API while keeping the product control plane in Go.

Use established runtimes, BuildKit, Docker/Compose, registries, object storage and reverse proxies through versioned adapters. A Go rewrite does not require replacing these systems. Keep the initial implementation modular inside the existing application and PostgreSQL authority.

Track three hosting compatibility dimensions separately:

1. Feature compatibility: users can complete the declared hosting and operations workflows.
2. Migration compatibility: supported existing resources and data can move to the new platform.
3. Client compatibility: existing Coolify API clients, CLI scripts and integrations work unchanged where an explicit adapter is supplied.

Task collaboration, machine pooling and agent execution are additional product capabilities. Neither their existence nor a repository-to-HTTPS demo establishes complete Coolify compatibility.

### 1.2 Intended users and success

The active audience is developers, self-hosters and small platform teams operating their own application hosts. The later workspace direction also serves remote teams seeking asynchronous work and reproducible review on owned hardware. These are product hypotheses, not customer-research conclusions.

A successful product lets users:

- assign builds, tests and agent work to an authorized compatible machine with an explicit budget;
- leave accepted work running after the submitting browser or laptop disconnects;
- review a retained change, required checks and a working application in one task;
- continue work through a supported handoff and promote the reviewed artifact without rebuilding it;
- run ordinary applications, databases and Compose stacks with clear data ownership;
- recover supported applications through tested backup, restore, upgrade and migration procedures;
- understand unavailable capacity, stale observations, partial results and recovery limits.

Direct application hosting remains a complete journey. Users need not create a coding task or run an AI agent to install an application. Coolify's hosting categories and source/build paths remain the reference for that part of the product. C1, C2

### 1.3 Scope and delivery precedence

The later workspace direction retains task-centered collaboration, distinct Task/Run/Workspace/Environment lifecycles, capability-aware placement, qualified isolation for untrusted work, and separate cache/artifact/verification reuse. D0–D3 and Mac, Windows, NAS, GPU and additional runtimes remain visible deferred gates/tracks.

This amendment changes delivery order, not scope. R01–R102 and A01–A66 remain intact. Direct hosting stays primary now; R65's Task-centered main collaboration view applies to the later workspace phase. References from hosting requirements to future workspace detail require only the smallest justified hosting contract, not the whole workspace product.

Before any workspace-track change starts, identify the named hosting requirement, why current hosting needs it now, the minimum scope, and the hosting acceptance test. General future usefulness, sunk effort and a possible later integration are insufficient. See the [hosting-dependency test](plan.md#hosting-dependency-test).

Supported trusted-host deployment does not require a new VM backend. Keep untrusted execution disabled until a declared hosting/preview workflow has satisfied its qualified isolation requirements. Do not weaken trust or data boundaries to accelerate parity.

Requirements are targets. The pinned source inspection establishes reusable foundations and current limitations. The implementation sequence is a recommendation without asserted delivery dates, effort estimates or measured performance claims.

## 2. Scope and parity ledger

Maintain two linked ledgers: the proposed workspace product and the Coolify hosting baseline. Every reference capability must have a ledger entry recording the upstream reference, NeroCD implementation status, supported limits, acceptance fixture and any deliberate difference. Suggested states are planned, implemented, validated, partial and intentionally different.

A delivery phase is an implementation sequence. It does not remove later capabilities from the target product.

|Product area          |Required target                                                                                                     |Initial slice and completion path                                                                                               |
|----------------------|--------------------------------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------|
|Server management     |Add local and remote servers, validate capabilities, manage destinations, build roles and proxies                   |Local enrollment and SSH-assisted remote onboarding first                                                                       |
|Resource organization |Teams, projects, shared environments, applications, databases and Compose stacks                                    |Introduce the correct domain model before exposing hosting workflows                                                            |
|Git sources           |Public and private repositories, branch/commit selection, deploy keys and provider integrations                     |GitHub first, then GitLab, Gitea, Bitbucket and generic Git/webhooks                                                            |
|Build strategies      |Dockerfile, automatic builders, static files, compiled static sites and Compose builds                              |Complete hosting Dockerfile/automatic/static paths; retain Nixpacks and Railpack as separate tested adapters|
|Inputs without Git    |Registry images, stored Dockerfiles, pasted Compose and catalog templates                                           |Do not require artificial repository records                                                                                    |
|Build placement       |Builds on application hosts or dedicated build servers, with image delivery and cache controls                      |Hosting remote-builder/image-delivery work under R14–R15/A07; no broader Task or Workspace prerequisite                                       |
|Domains and HTTPS     |Custom and generated domains, target ports, paths, redirects, certificate issuance and renewal                      |Managed Traefik first; Caddy and custom proxy modes tracked explicitly                                                          |
|Deployment automation |Manual deploy, deploy on push, deploy webhooks, redeploy and retained releases                                      |Automatic trusted-source deployment is a normal workflow                                                                        |
|Preview deployments   |PR/MR and task previews, isolated configuration and storage, domains, status reporting and cleanup                  |PR/MR previews remain core hosting parity; task-linked private previews belong to deferred workspace gates                                                        |
|Persistent storage    |Named volumes, directory mounts, managed files and existing host-file mounts                                        |Ownership and retention must exist before general stateful hosting                                                              |
|Databases             |Engine-specific creation, credentials, networking, storage, health, configuration and supported recovery            |PostgreSQL and Redis first; complete the engine matrix below                                                                    |
|Service catalog       |One-click installation, generated values, component controls and versioned upgrades                                 |Start with a useful tested catalog, then expand compatibility and breadth                                                       |
|Variables and secrets |Managed encrypted credentials, build/runtime scopes, shared values and external secret sources                      |Managed secrets are a foundation dependency                                                                                     |
|Ongoing operations    |Runtime status, logs, health, limits, start/stop/restart, terminals and diagnostics                                 |Basic operations ship with each resource type                                                                                   |
|Recovery              |Platform backup, database backup, storage backup, restore and image/configuration rollback                          |Implement separate contracts and interfaces                                                                                     |
|Automation            |Resource-bound schedules, notification channels, retries and execution history                                      |Required for core replacement completion                                                                                        |
|Programmatic access   |Versioned REST API, CLI and MCP access to supported operations                                                      |One authorized application layer behind every interface                                                                         |
|Migration             |NeroCD upgrade path and supported Coolify resource/data migration                                                   |Compatibility report and staged cutover before automatic adoption                                                               |
|Broader infrastructure|Additional proxy modes, multi-server application deployment, Swarm, provider provisioning and GitHub Actions runners|Explicit extended-parity backlog with separate capability tests                                                                 |

References for the baseline categories: build paths C2, Compose C3, server/build roles C4, routing C5, previews C6, databases C7, templates C8, operations C9, teams C10, and the reference repository C1.

### 2.1 Workspace capability ledger

|Product area            |Proposed target                                                                                        |Deferred workspace demonstration and follow-through                                                                                    |
|------------------------|-------------------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------|
|Machine pool            |Stable machine identities, roles, capabilities, architecture, availability, trust and bounded resources|Qualified Linux hosts first, with explicit host adapter qualification for other systems                                   |
|Durable tasks           |Goal, repository lineage, runs, activity, workspace, evidence and review history                       |One task survives client disconnect and records an agent attempt                                                          |
|Agent workspaces        |Isolated checkout and tool environment, scoped grants, checkpoints and human takeover                  |One agent adapter and a supported portable source checkpoint                                                              |
|Shared builds           |Remote BuildKit, authorized cache sharing, immutable artifacts and reuse decisions                     |Demonstrate imported cache on a second compatible builder and image delivery to another host                              |
|Review and promotion    |Private preview, trusted checks, immutable candidate and target-specific approval                      |Another teammate reviews and approves the same artifact for production                                                    |
|Model endpoints         |Team-owned inference services with endpoint grants, quotas and availability                            |An existing authenticated endpoint may be connected early; model lifecycle and GPU placement need their own support matrix|
|Owned and cloud capacity|Placement across authorized machines, with explicit cloud fallback policy                              |Manual addition of compatible cloud capacity first; provider provisioning follows                                         |
|Personal cloud          |Compute, storage and public-ingress roles across supported machines                                    |One always-on application host and a VPS gateway, with clear availability and data-location rules                         |
|Application care        |Supported updates, recovery, guided migration and restore tests                                        |Reuse the hosting lifecycle, then add application/version-specific support contracts                                      |
|Later operations        |Database-aware planning, progressive delivery, drift review and vendor packages                        |Each requires explicit eligibility, authority and failure/recovery behavior                                               |

### 2.2 Boundaries that remain valid

The self-hosted platform does not inherently provide a managed-cloud availability SLA, cross-host database replication, universal zero-downtime updates or isolation between hostile workloads sharing one Docker host.

Kubernetes, a replacement container runtime and a separate hosted billing business are not dependencies of the first workspace demonstration. Docker Swarm and other documented Coolify infrastructure features remain visible parity items rather than being silently conflated with Kubernetes.

A machine pool places separate jobs. It does not combine arbitrary machines into one shared-memory computer or imply distributed GPU inference. Cross-host data movement, live migration and host failure recovery need distinct support contracts.

Functional parity is measured against the pinned reference and its recorded capability ledger. Do not call the first repository-to-HTTPS implementation complete Coolify parity.

## 3. Starting codebase: reuse, extend and replace

The current baseline is accepted NeroCD `4ba9fcac1b484244cb2c662e53f588c2dce6b71e`. Static inspection and retained revision-specific runtime reports support the bounded status below. They do not validate the proposed workspace product. The supplied `09d65d4ddcbfdf27e97158a9c08beb02a721818c` remains historical context.

|Existing area                                    |Decision              |Work required                                                                                                                 |
|-------------------------------------------------|----------------------|------------------------------------------------------------------------------------------------------------------------------|
|Go API and application services                  |Reuse                 |Add workspace and hosting operations through existing authorization and transaction boundaries                                |
|PostgreSQL, migrations and durable queue         |Reuse and extend      |Add durable task relationships, bounded reservations and typed workspace, build and hosting work                              |
|Runner enrollment, leases, fences and replay     |Reuse and extend      |Evolve into host agents and build workers with explicit server/destination identity                                           |
|Local/OIDC login and project authorization       |Reuse                 |Add team ownership and resource/server permission rules                                                                       |
|Managed service accounts                         |Reuse and extend      |Preserve principal lifecycle and add short-lived task grants with enforceable operation and resource scopes                   |
|React/Vite/TanStack frontend and generated client|Reuse infrastructure  |Keep direct hosting primary now; retain the richer Task collaboration view for the deferred workspace phase                    |
|Service/environment/revision model               |Extend accepted hosting|Shared project environments and typed resources already exist; add remaining kinds and legacy migration mappings              |
|Compose execution primitives                     |Reuse selectively     |Keep legacy restrictions explicit; extend managed Compose/storage without regressing ordinary hosting HTTP readiness           |
|Provenance and linked rollback                   |Extend                |Separate build outputs, releases, target-specific application and data recovery                                               |
|File-backed secrets and access audit             |Preserve and extend   |Reuse accepted encrypted managed-secret storage and protected reveal; complete scoped execution delivery and recovery            |
|Artifact and encryption helpers                  |Reuse selectively     |Complete the specific consumers, access rules and recovery procedures                                                         |
|Operational templates and runs                   |Preserve and extend   |Keep execution instances distinct from a durable user task, mutable workspace and preview environment                         |
|Production, backup and release tooling           |Reuse                 |Extend coverage to agents, proxy configuration, secrets and hosted workloads                                                  |

Sources: architecture N1, domain model N2, deployment service N3, runtime N4, service accounts N5, secrets N6, and storage/encryption foundations N7.

### 3.1 Historical assumptions and accepted changes

The legacy service/Compose contract remains supported. Its restrictions do not describe every accepted hosting path.

- **Environment cardinality.** Legacy `Environment` belongs to a service. Accepted `ProjectEnvironment` and typed `Resource` records provide shared project grouping. Remaining work includes the broader resource kinds, legacy mappings and complete concurrency acceptance. N2, N14
- **Source requirements.** Legacy services retain repository/Compose requirements. Accepted hosting supports registry-image input without a fake repository and versioned public/private HTTPS Git configuration. Private source grants, protected credential transport, compiled CLI Dockerfile/Railpack checkout and the Dockerfile WebUI journey are accepted within their stated limits. Private Railpack static-provider UI qualification is accepted within its fixture limits; arbitrary language/provider coverage remains incomplete. Stored Dockerfiles, pasted Compose and catalog inputs remain planned. N3, N14
- **Build and deployment input.** The legacy provenance resolver still rejects unsupported Compose builds and mounts. Separately enabled public Git builds execute Dockerfile, pinned Railpack and prepared-static paths. Same-host local artifact deployment is accepted and applies the exact recorded ImageID without rebuilding or pulling. Remote registry publication and shared-cache/reuse guarantees remain planned. N4, N15
- **Health.** The legacy Compose check uses `X-NeroCD-Revision`; accepted image/local-artifact hosting uses ordinary HTTP readiness. The opt-in independent collector now reads narrow Docker status and loopback readiness. Broader component/TCP/database health and runtime metrics remain targets. N8, N15
- **Mutation ownership.** Accepted hosting uses resource-binding holds, exact destinations, leases and fenced receipts. Unresolved effects and restored ownership holds remain blocked. A shared environment does not authorize relocation or clearing a hold. N9, N14
- **Secrets and logs.** Project-managed encrypted secret versions and authorized reveal are accepted. Typed build output has durable receipt/cursor storage, runner delivery, redaction and an opt-in bounded WebUI. Private Git credential delivery and runtime consumption are accepted with live authority, expiry checks and attempt-local redaction; the UI selects grants by metadata without reveal. Desired runtime-configuration API/storage is accepted through migration 0052 with explicit public nonsecret literals and exact managed-secret references, current owner writes/viewer reads, generation compare-and-swap and idempotency. Migration 0053 admission and protected runner delivery are accepted, with metadata audit before decryption and journal format 8 containing only nonsecret references and delivery identity. Opt-in CLI/API execution now applies literals and exact managed-secret versions through protected Docker stdin. Bounded challenge/report and viewer freshness controls are accepted through migration 0054. The configuration UI and opt-in independent collector are accepted within the limits below. The bounded runtime-status dashboard is accepted. Runtime-log control through migration 0055 and bounded live collector/viewer are accepted. Broader managed-secret execution delivery, terminals and broader runtime metrics remain pending. Build logs are not runtime logs. N6, N16

These accepted features preserve the direct hosting journey. They do not supply the Task, Workspace, reservation, task-grant, checkpoint or VM contracts introduced below.

### 3.2 Workspace gaps in the accepted implementation

At the accepted baseline, `TaskRun` describes an execution instance and there is no durable user Task or Workspace entity. Add a separate long-lived Task for the user goal, collaborators, several attempts, review and environment links. Existing enrollment, leases and fences are useful execution foundations, but tags and primitive capabilities do not establish resource reservations, isolation suitability or data-location authorization. N12, N13

Existing managed service accounts have project roles and lifecycle controls. They are not already narrowly scoped task credentials. The local content-addressed artifact helper explicitly leaves authorization to its consumers. Complete access control, transport, retention and remote publication before calling it a shared artifact service. N5, N7

A database lease alone cannot stop an old process from writing a workspace or making an external request. Carry authority generations into the host/guest helper and credential broker, and reconcile uncertain effects before handing mutable work to another writer. This is an implementation requirement derived from the proposed handoff and recovery behavior.

## 4. Target architecture

Keep a modular Go application and the existing PostgreSQL authority. Separate responsibilities in code and job types before considering independently deployed services. Humans, agents, CLI and integrations use one authorized application layer.

Hosting contracts lead current implementation. The Task/Workspace, reservation,
agent and model architecture below remains future scope; any bounded part must
pass the hosting-dependency test before work starts. It is not a prerequisite
for supported trusted-host hosting.

```mermaid
flowchart TB
    Clients["Humans and agent integrations"] --> Control["Go control plane and PostgreSQL"]
    Control --> Work["Task and workspace execution"]
    Control --> Env["Environment lifecycle"]
    Work --> Machines["Authorized machines and VM adapters"]
    Work --> Evidence["Checkpoints, checks and artifacts"]
    Evidence --> Env
    Env --> Guests["Applications, workers and databases"]
    Gateway["Public or private ingress"] --> Guests
    Guests --- Volumes["Explicit persistent data"]
```

The control plane records intent, policy and observations. Machines execute bounded work through local supervisors. OCI registries hold deployable images and supported BuildKit cache exports. Artifact storage holds checkpoints and evidence through a separate authorization and retention contract. A supported S3-compatible backend can be self-hosted, including a qualified Garage deployment. Backend choice does not establish backup, availability or registry compatibility by itself.

Application traffic flows through an ingress/proxy path independent of control-plane request handling. For a host behind NAT, an outbound authenticated tunnel can connect explicitly published services to a gateway. Do not grant the gateway or a preview blanket access to the owner's LAN. An ingress endpoint is not automatically a CDN or an edge execution location.

### 4.1 Execution mechanisms

Extend the existing runner protocol into a managed Go host agent. Support manual enrollment and SSH-assisted installation on supported Linux hosts. Keep stable machine identity separate from the identity of one agent process.

Coolify currently describes SSH-based management without requiring a persistent deployment agent. A Go agent is an intentional implementation difference suited to the existing runner model and proposed long-lived work. C11

Use a small privileged backend helper where virtualization or host networking requires it, and a guest-side supervisor for work inside an isolated Linux environment. Version and authenticate the interfaces. Keep host administration, guest execution and user task credentials separate.

For a future qualified untrusted-work backend, the proposed first adapter is Linux with working KVM and Firecracker, with Docker/Compose or BuildKit inside supported guests. This is deferred unless the hosting-dependency test establishes a bounded need for a declared untrusted hosting/preview workflow; trusted-host deployment does not wait for it. Firecracker requires a Linux host and runs guests matching the host architecture. Native Mac execution needs a different adapter, for example Apple Virtualization.framework. These are backend-specific implementations of one lifecycle contract. W1, W2

A task workspace, a preview environment and a production environment have separate lifecycle and authority boundaries. A supported preview can contain an application, worker and isolated database within one environment boundary. Persist its data only through declared volume bindings. Shared tenant Docker daemons are an explicitly trusted mode, not the default boundary for untrusted agent tasks.

Builds, agent sessions and application mutations use the same durable execution foundations but distinct credentials, scheduling rules and records. A database cannot move because another machine has matching tags. A build retry does not automatically resume an interrupted agent session.

Use versioned adapters for agents, models, VM backends, build tools and Compose. The Go Compose model remains a candidate dependency. Test actual runtime behavior rather than inferring compatibility from parsing alone. T1

### 4.2 Domain model

|Entity                                  |Responsibility and lifecycle                                                                                                                   |
|----------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------|
|Team and project                        |Ownership, membership, policy, source connections and machine access                                                                           |
|Task                                    |Durable goal, owner, collaborators, source/issue/PR lineage, activity, review state and links to workspaces and environments                   |
|Run and attempt                         |Bounded execution with actor, worker, reservation, lease, fence, deadline and result; extend existing `TaskRun` internally                     |
|Workspace                               |Mutable checkout and tool state, current writer generation, storage location and checkpoint policy                                             |
|Checkpoint                              |Immutable source snapshot or patch, environment recipe and adapter-supported resume state, with a declared recovery limit                      |
|Project environment                     |Related running resources such as preview, staging or production, with an independent lifetime                                                 |
|Resource and deployment binding         |Application, database or stack, plus its destination, configuration, policy and mutation ownership                                             |
|Machine                                 |Stable physical/virtual host identity, architecture, roles, capabilities, trust, availability and observations; preserve legacy Server mappings|
|Destination                             |A specific runtime/guest boundary, networks and persistent-data placement on a machine                                                         |
|Reservation                             |Accounted CPU, memory, disk, GPU and concurrency allocation, including expiry and reconciliation state                                         |
|Task grant                              |Short-lived, enforceable authorization for a run or attachment, independent of the agent's stable identity                                     |
|Source connection                       |Git or registry access and credential references                                                                                               |
|Build input manifest and attempt        |Frozen effective inputs, eligibility for reuse, builder identity, logs and outcome                                                             |
|Artifact                                |Immutable content identity, platform, storage locations, original production evidence and retention references                                 |
|Verification run                        |Required-check definition and version, artifact/source subject, execution context, freshness and measured result                               |
|Release                                 |Immutable deployable artifact set and source/build evidence; a candidate is a release under review, not a mutable branch                       |
|Deployment plan                         |Release plus named target, effective runtime configuration, secret versions, verification evidence and data/migration preconditions            |
|Approval and deployment                 |Authorized decision over a specific plan, then a separately recorded attempt to apply it                                                       |
|Route and ingress binding               |Hostname/path/port ownership, certificate status, preview access policy and any authorized tunnel endpoint                                     |
|Storage binding and secret version      |Explicit data ownership/location and protected values or external references with lifecycle                                                    |
|Preview                                 |Task/PR-derived resource set, current release, visibility, expiry and independent runtime/data policy                                          |
|Backup execution and runtime observation|Recovery artifact/consistency/verification, and timestamped evidence about machine or resource state                                           |
|Model endpoint                          |Authenticated service, model/version identity where available, data policy, quota and availability, separate from an arbitrary host shell      |

Not every entity needs a separate page. The task view should show the current candidate, work and preview status, last checkpoint, required checks, budget and next available action. Keep application operations accessible without a task.

Staging and production may use different configuration and data. Promotion preserves the artifact identity while producing a target-specific plan. Further workspace changes create a new candidate and never silently modify the one a teammate reviewed.

### 4.3 Machine roles and qualification

The following is a proposed qualification matrix, not an implemented support claim.

|Machine                          |Useful roles                                                                                                   |Conditions and limits                                                                                                                                                                            |
|---------------------------------|---------------------------------------------------------------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
|Linux gaming PC or workstation   |Builds, tests, agent work, optionally a trusted model endpoint                                                 |Qualified KVM backend for VM-isolated work, bounded allocations, local availability policy and native target architecture                                                                        |
|Linux mini PC or dedicated server|Persistent applications, previews, builds and control services                                                 |Reserve capacity for persistent workloads and define disk ownership, backups and maintenance behavior                                                                                            |
|Apple Silicon Mac                |Native ARM64 build/workspace guests, supported persistent Linux guests, native model serving                   |Separate Virtualization.framework adapter and qualification; native Metal model service exposed through a constrained endpoint; no assumption of Firecracker support                             |
|Windows gaming PC                |Work inside a separately managed Linux VM; trusted work in WSL2; optionally a separately managed model endpoint|Use a qualified restricted VM boundary for untrusted work. A WSL distribution and its settings do not satisfy that boundary by themselves. Any nested VM backend requires separate qualification.|
|NAS                              |Backup/object storage and, where qualified, limited compute                                                    |Qualify OS, architecture, virtualization, memory and disk behavior; storage eligibility does not imply compute eligibility                                                                       |
|VPS                              |Public ingress, control plane, registry or eligible applications                                               |VM-isolated execution requires actual KVM access or an explicitly qualified alternative; inexpensive VPS capacity must not be assumed to expose it                                               |
|Additional cloud machine         |Authorized overflow for compatible jobs or environments                                                        |Explicit provider, location, trust, credentials, spend limit and data-transfer policy before fallback                                                                                            |

A machine's capability report describes eligibility. Authorization decides whether a project may use it. Placement first filters by authorization, trust, allowed location, architecture, isolation backend and hard capacity requirements, then ranks eligible hosts by queue, cache/data locality, availability and declared preferences.

Reserve resources on claim, enforce local limits, and reconcile reservations after uncertain failures. Do not infer GPU availability from a machine label. Record device identity, supported access mode and exclusive or shared ownership. GPU passthrough, native model serving and API access to a model are different capabilities.

Mac and Windows integration must be qualified against real host exposure. Apple's documented container machine mode, for example, shares the user's home directory and SSH agent by default, while WSL has host-drive and interoperability settings. NeroCD must configure an explicit isolation profile rather than inherit convenience defaults. Disabling WSL drive mounts and interoperability reduces exposure but does not turn WSL into an untrusted-code sandbox. W3, W4, W13

### 4.4 Durability and handoff contract

|Event                               |Required behavior                                                                                                                |
|------------------------------------|---------------------------------------------------------------------------------------------------------------------------------|
|Submitting client disconnects       |Accepted remote work continues under a supervisor independent of the browser, laptop connection or interactive BuildKit session  |
|Control plane restarts              |Persisted tasks remain, agents follow the documented lease/reconnect policy, and existing application traffic continues          |
|Worker becomes unreachable          |Mark observations stale and work uncertain/interrupted; retain known evidence and reconcile before granting conflicting authority|
|Worker returns                      |Reconnect to the recorded attempt where valid, or reject stale authority and reconcile its effects                               |
|Another machine takes over          |Restore the latest supported checkpoint to a compatible environment; describe lost uncheckpointed work and any restarted steps   |
|Human takes over a workspace        |Pause/revoke the previous writer, confirm quiescence or clone a checkpoint into a new workspace, then issue a new grant          |
|Agent run completes                 |Retain the candidate and evidence; keep the preview according to its independent policy                                          |
|Machine with persistent data is lost|Use the supported data recovery/migration path; scheduling another process alone cannot recover that data                        |

A checkpoint may contain source, a patch, uncommitted files, tool metadata and an agent adapter's documented continuation state. Exclude credentials and unrelated home-directory content. Process-memory snapshots are optional backend-specific optimizations, not the portable handoff format. Report whether recovery resumes a supported session, restarts from a checkpoint, or retains evidence only.

### 4.5 Build identity, cache and promotion

Expose three different decisions: reuse cached build steps, reuse a complete artifact, and reuse verification evidence. Each has a different correctness and trust contract.

A frozen build input manifest must cover the effective build context, Dockerfile and named contexts, target, platform, resolved base/frontend identities, builder/toolchain versions, generated configuration, relevant non-secret arguments, dependency inputs and output-affecting secret version identifiers. A Git commit alone cannot identify all these inputs. Snapshot a mutable agent checkout before building.

Whole-artifact reuse is eligible only when the recorded inputs and declared refresh policy support it. Unresolved mutable network inputs or untracked dependencies must disable that guarantee or use an explicit bounded refresh policy. Reuse under a freshness window satisfies that policy but does not prove unresolved external inputs remained unchanged. Reusing the original artifact preserves its original provenance and records the new task's reuse decision. It does not fabricate a new build.

Use registry-backed BuildKit exports for the initial shared layer-cache path, with isolated trust scopes and controlled publication of shared references. BuildKit's registry cache can be separate from the final image. Its RUN --mount=type=cache directories remain builder-local and are not exported by --cache-to; portable compiler/package caches require an additional supported mechanism. W5, W6

Build-secret contents do not participate automatically in BuildKit's cache checksum. Track an opaque version or generation for output-affecting secret changes and apply the supported invalidation mechanism without storing secret values or guessable hashes in build identity. W7

A self-hosted registry may use a qualified object-store backend. Do not make direct BuildKit-to-Garage S3 cache export the first interoperability guarantee: Docker's backend overview labels S3 unreleased, while its S3 page labels it experimental. Pin and test the actual combination if adopting it. W8, W9

Required runtime checks must identify the exact artifact they executed. Source-level tests, tests of a separate build stage and runtime verification have distinct subjects. Keep required check definitions under trusted policy so agent changes cannot replace mandatory verification. Reuse checks only when their recorded source or artifact subjects, check-definition and policy versions, relevant execution context, fixtures and freshness policy still match.

For multi-platform artifacts, retain the OCI index and selected platform-manifest digests and their verification coverage. Success on ARM64 does not establish native AMD64 runtime behavior. Emulated results must be labeled. Freeze any attestations included in the index before sealing that index digest, or attach later evidence through immutable external references. W10, W11

An approval binds the immutable release, required evidence and the destination-specific plan. It does not authorize future branch contents. Runtime configuration and data can differ between preview and production while the promoted application artifact remains identical.

## 5. Required user journeys

### 5.1 Repository to HTTPS application

A user installs the platform, adds or selects a server, connects a public or private repository, chooses a build strategy, supplies variables, configures a domain and selects Deploy. The platform resolves the source, builds the image, starts the application, checks readiness and configures HTTPS.

The user sees build progress, deployment progress, the live URL, runtime logs and current health. A subsequent push deploys automatically when enabled. Failed builds leave the running version available.

### 5.2 Application with a database and uploaded files

The user creates PostgreSQL, receives a private connection binding, attaches it to an application, and adds persistent storage for uploads. Generated credentials are stored by the platform. Redeploying either resource preserves its data.

The user configures database and storage backups, restores copies into new resources, verifies data, and sees the separate restore status of each dependency.

### 5.3 Compose or one-click service

The user selects a catalog entry, pastes Compose, or connects a repository containing Compose. The platform identifies components, required variables, persistent mounts and public endpoints. It generates credentials once, builds or pulls the images, and starts the stack.

Component pages expose logs, health, terminals, schedules and applicable database backup controls. A catalog refresh does not silently change an installed service.

### 5.4 Pull-request preview

A trusted PR/MR event creates or updates an isolated preview with its own routes, configuration and storage policy. The provider shows its status and URL. Closing the request removes the preview according to retention policy without touching production data.

Production secrets are not copied into previews by default. Public-fork execution requires an explicit trust policy and suitable build/runtime isolation.

### 5.5 Operate and recover

The operator inspects resource health after deployments finish, restarts a container, opens an authorized terminal, runs a scheduled command, receives a failed-backup notification and diagnoses an unreachable server.

A control-plane restart leaves hosted resources running. A restored control plane reconciles ownership before resuming mutation.

### 5.6 Task to reviewed deployment: deferred workspace demonstration

Use one supported Linux architecture and two physical machines with three logical roles: an always-on control/gateway role, a team-owned agent/build worker, and an always-on preview/deployment target. The control/gateway and target may share a machine with bounded allocations. Use a real registry transfer between build and deployment hosts.

1. A human creates a repository task against a concrete starting revision, a trusted check policy and a preview recipe containing an application and isolated seeded PostgreSQL. Add a worker component if the application needs one.
2. Placement selects an authorized compatible machine, records a reservation and creates an isolated workspace. One supported agent receives only the repository, model, build and preview grants required for the task.
3. The agent works in its checkout. NeroCD retains activity, source checkpoints and failures. The initiating human disconnects and work continues remotely.
4. An immutable source checkpoint and build input manifest freeze the inputs. BuildKit builds with authorized shared cache and publishes the artifacts. Successful publication creates the immutable candidate release. Required source checks identify their source subjects. Required runtime checks execute the final published candidate artifacts and record their digests.
5. The preview target pulls the artifact by digest and exposes a private URL with its own application data. It remains available after the agent run ends and the build machine is drained.
6. A teammate opens the task in another session, reviews changes, evidence and the live application, and can take over through a supported workspace handoff.
7. An authorized human approves the production plan. Deployment applies the reviewed artifact without rebuilding and records the destination, configuration and data preconditions. The observed platform-manifest digest must match the approved artifact for that platform.
8. A later source or relevant plan change requires a new decision. Preview expiry removes only owned disposable resources and retains task evidence and pinned releases.

The demonstration must also run a compatible build on a second builder and show imported cache use, elapsed time and transferred bytes. A warm cache on the original builder alone does not prove shared cache. Keep this small fixture isolated from persistent workloads.

This demonstrates one supported path. It does not assert universal agent resume, cross-architecture execution, application migration or full Coolify parity.

### 5.7 Personal cloud and owned model endpoint

A single owner enrolls a build-capable gaming PC, an always-on application host and a public VPS gateway. The platform shows which host executes work, which stores persistent data, and which publishes each endpoint. A NAS may be a configured backup target without joining the compute pool. A sleeping build host delays new jobs or triggers an explicitly configured fallback, while the independent application host continues serving.

The owner or team can register a model service running on suitable hardware. Tasks receive scoped endpoint access and usage limits, not arbitrary access to the model host. A failed or unavailable model endpoint appears as a dependency failure with a declared retry policy. First-class model installation, GPU allocation and model upgrades require separately qualified lifecycle support.

### 5.8 Reproduce, upgrade and recover

A failed deployment can open a reproduction task containing its exact release, available configuration references, selected diagnostics and an isolated environment recipe. Show missing data and unavailable external dependencies. Production data copies require authorization, an appropriate sanitization policy and blocked external side effects by default.

A supported application update declares its version path, preflight, backup, migration, verification and recovery procedure. Guided moves between machines transfer data explicitly, coordinate writers and switch traffic only after verification. Scheduled restore tests create disposable environments and verify application behavior, rather than merely confirming that an archive can be unpacked.

## 6. Functional requirements

All requirements below describe the complete target. The hosting-first amendment in sections 1.3 and 10 governs delivery order: R01–R68/A01–A37 and declared hosting matrices first; R69–R102/A38–A66 remain visible future scope. R65's Task-first collaboration view is deferred; direct hosting is primary now. No requirement definition is removed or narrowed by this ordering.

### 6.1 Organization and server management

R01. Shared environments. A project environment MUST contain independently operated applications, databases and stacks. Two unrelated resources in one environment MUST be able to deploy concurrently.

R02. Team and project authorization. Reuse existing identity and service-account behavior. Add explicit team ownership and server-access rules. Preserve current project permissions during migration. A viewer MUST not gain deploy, terminal, secret-reveal or data-deletion rights through a new UI or API.

R03. Machine onboarding. Support local installation, manual agent enrollment and SSH-assisted remote enrollment where the host adapter supports them. Probe operating system, architecture, virtualization/runtime availability, disk capacity, connectivity and relevant ingress readiness. Display qualified roles and unmet prerequisites. Validate SSH host identity and include installations or service restarts in the reviewed onboarding operation. Do not advertise VM isolation based only on a CPU flag or machine label.

R04. Credential separation. Bootstrap SSH credentials, enrolled-agent credentials, Git credentials and registry credentials MUST have separate scopes and lifecycles. State whether bootstrap credentials are retained for recovery. The browser MUST not receive permanent agent or host credentials.

R05. Stable destinations. Machine and destination identity MUST survive agent process replacement. Every deploy, rollback, volume operation, backup, route change and scheduled command MUST identify its intended runtime boundary and data location. Matching tags do not authorize relocation. Preserve legacy Server identifiers through migration mappings.

R06. Machine roles and lifecycle. Support declared build, agent, test, application, storage, ingress and model roles where qualified, including combined roles with bounded allocations. Provide drain, reconnect, credential rotation, maintenance and removal. Removal MUST not implicitly destroy workloads or data. Old agents MUST not claim unsupported operations. Show the impact of a role or availability change on persistent workloads and queued tasks.

R07. Execution trust. Untrusted agent tasks and untrusted build inputs MUST use a qualified isolation profile under R81 and R82. A shared host Docker daemon remains an explicitly trusted deployment mode. Database-level team separation does not sandbox hostile workloads. A machine administrator remains trusted with guest data and secrets placed on that machine. Capability matching never grants access to a machine, dataset or private network.

### 6.2 Sources and builds

R08. Source choices. Support public/private Git, selected branches and commits, registry image tags or digests, stored Dockerfiles, Git Compose, pasted Compose and catalog templates. Preserve the source of truth for each. A no-Git input MUST not require a repository placeholder.

R09. Git integrations. Provide repository and branch selection, scoped credentials, connection tests and webhook setup. Implement GitHub first, then the provider matrix in section 2. Generic Git and signed manual deploy webhooks remain supported paths.

R10. Trigger integrity. Authenticate provider events, record delivery identity, deduplicate retries and bind a deployment to the intended source revision. Define how superseded queued changes are handled. A delayed older event MUST not silently replace a newer active release. Replaying a webhook MUST not create unbounded duplicate builds.

R11. Build strategies. Support Dockerfile contexts and targets, automatic builders, static file packaging, compiled static output and Compose build sections. Record the exact builder/version and effective settings. Validate monorepo base paths, output directories and command overrides. Nixpacks and Railpack support must be tracked separately. Coolify currently marks Railpack as Beta. C12

R12. Static sites. Distinguish serving already-built files from compiling frontend source. Support publish directory, SPA fallback and configurable static-server behavior. A configuration baked into an image requires a rebuild rather than a misleading restart action. C13

R13. Build records and outputs. Record resolved source or workspace checkpoint, frozen build input manifest, platform, builder identity, logs, cache outcome and immutable output identities. Accept image tags as convenient inputs and record selected digests. Keep original build provenance when an artifact is reused. A deployment retry MUST not silently rebuild different contents. R87–R92 define input, trust and verification rules.

R14. Placement and image delivery. Support documented local builds and dedicated build workers. Remote builds MUST publish to an authorized registry or another qualified transfer mechanism. Verify target platform, artifact availability and pull access before mutation. A local image on one daemon is not available elsewhere by implication. The supervising build session MUST be owned by a remote service or worker so a submitting laptop disconnect does not cancel accepted work. Buildx provides a remote BuildKit driver, but product supervision and authorization remain NeroCD responsibilities. W12

R15. Cache and retention. Expose layer-cache reuse, complete-artifact reuse and verification reuse as separate outcomes. Scope cache access by project/team and trust policy. Track output-affecting secret versions and dependency refresh rules. Provide independent controls for disabling cache, rebuilding an artifact, refreshing mutable inputs and rerunning checks. Protect artifacts referenced by active deployments, retained releases and evidence roots. R87–R92 define the detailed contract.

R16. Build credentials and limits. Separate build-time credentials from runtime secrets. Scope source and registry access to the operation. Apply time, CPU, memory, disk and concurrency limits. A failed or canceled build MUST not modify the active resource or route. Untrusted preview code MUST not receive production credentials.

Build and deployment are separate product records even when they execute on one host. Coolify likewise supports distinct local and remote build paths. C2, C4

### 6.3 Releases and deployment behavior

R17. Automatic and protected deployments. Trusted push-triggered deployment remains a normal hosting workflow under an explicit resource/environment policy. Every deployment resolves a concrete release and target plan. Task work produced through an agent MUST pass independent human production authorization under R85. Agent access to a repository, branch or workflow MUST NOT become an indirect bypass of this rule through automatic deployment. New agent-workspace projects default production targets to protected.

R18. Frozen release and plan identity. A release MUST identify immutable artifacts, source/build evidence and the deployable recipe version. A deployment plan freezes the selected release, destination, effective workload configuration, secret references/versions, relevant variable generations, required verification and data preconditions. Durable records MUST retain secret references and version identifiers instead of expanded values. Resolve secrets only into protected execution material. Secret-expanded Compose output MUST NOT enter ordinary release exports, audit payloads or artifacts. Hash normalized configuration with secret identities rather than publishing hashes derived from low-entropy values. Distinguish immutable secret-provider versions from mutable references.

R19. Plan approval. Where approval is required, review the concrete plan after image preparation and required verification, before workload mutation. Bind the decision to release, target, effective configuration/secret generations and evidence. New selected contents, material evidence-policy changes, expiry or relevant plan changes require a new decision. Current authorization and revocation still apply at execution. State human self-approval policy separately from the prohibition on an agent approving its own production work.

R20. Admission and concurrency. Preserve durable idempotency and linked attempts. Scope active deployment exclusion to the resource binding. Extend current revision-level intent matching to release and plan identity. Coordinate shared proxy, network and storage mutations with their own ownership constraints.

R21. Deployment strategies. Provide separate strategies for a single-image application, a Compose stack and a stateful database. Eligible stateless applications should start a replacement, verify readiness, switch traffic and drain the old container. Fixed host ports, exclusive mounts or application constraints may require replacement with interruption. Show the selected strategy before deployment.

R22. Generic health. Support container health, HTTP, TCP and engine-specific readiness. Treat platform revision headers as optional extra evidence. Track expected components, images and container state through platform-owned metadata. Successful one-shot initialization jobs and optional components require different status rules from long-running services.

R23. Cancellation and uncertainty. Cancel queued/build/pre-mutation work where safe. Once mutation may have occurred, record cancellation intent and reconcile effects before retry, rollback or termination. Preserve attempt fencing, durable replay and target ownership. A terminal intervention or failed rollback MUST not unlock unresolved runtime effects for a competing deployment.

R24. Rollback and application data. Offer retained release selection, show the configuration and data-compatibility implications, and restore the selected supported release on the same destination. State whether the action uses the retained configuration or explicitly selected current overrides. Current authorization and secret revocation still apply. Never describe image rollback as reversal of database migrations or uploaded-file changes.

Coolify's own replacement strategies differ between single-image applications and Compose, and its documented image rollback uses current runtime configuration. This rewrite may provide a stronger retained-configuration option, but must label it as a deliberate improvement. C14, C15

### 6.4 Routing and certificates

R25. Managed routing. Provide an integrated Traefik path first. Store route ownership, hostname, path, target component and internal port. Support multiple domains and public components, redirects, WebSockets and appropriate forwarding headers. Do not confuse an internal target port with a public host-port mapping.

R26. Domain setup. Support custom domains and generated subdomains under a configured wildcard domain. Report DNS, ownership, reachability and route conflicts with actionable errors. Testing domains must be identified as such. Do not silently assume ownership of a domain or a working DNS record.

R27. Certificate lifecycle. Manage ACME issuance, renewal, status and failure reporting. Support ordinary HTTP validation and a separately configured DNS-validation path for wildcard certificates. Protect DNS-provider credentials. Use bounded retries and retain a valid certificate when a renewal attempt fails. Custom certificates need an explicit storage and rotation path.

R28. Proxy changes and availability. Validate new generated configuration and preserve the last working configuration on failure. Serialize shared proxy changes without blocking unrelated builds. Existing routes and workloads MUST continue serving during a control-plane restart. Track Caddy, custom-proxy operation and proxy migration in the parity ledger.

Current Coolify provides managed Traefik, Caddy and custom proxy choices. The rewrite can sequence those adapters while keeping routing a core requirement. C5

### 6.5 Variables, secrets and credentials

R29. Managed secret storage. Integrate encrypted secrets in the first application-hosting path. Cover Git, registry, SSH bootstrap, application, database, S3, webhook, DNS and notification credentials. Reuse existing cryptographic and lease-bound access foundations where suitable. Complete persistence, authorization, versioning, key provisioning, rotation and recovery before claiming support.

R30. Value scopes. Separate build-time, runtime and preview variables. Support shared references at team, project, environment and resource scopes with a documented override rule. Show the origin of each effective value and whether a change requires a rebuild, redeploy or restart. Shared secrets MUST not be injected into every resource merely because they exist at a parent scope.

R31. Disclosure and delivery. Mask secrets in ordinary views. Allow reveal or connection-string copy only through an explicit authorized operation with appropriate audit. Preserve one-time disclosure for credentials whose contract requires it. Deliver runtime/build secrets only to authorized execution. Redact supported output representations and keep plaintext out of stored job metadata and notification payloads.

R32. Rotation and provider behavior. Define rotation, revocation and unavailable-version behavior. A runner_file label does not by itself freeze file bytes. For database accounts, rotate the engine credential and dependent bindings through a recoverable operation. For external secret sources, state resolution time, cache policy, missing-key behavior and build-cache consequences.

R33. Key recovery and external sources. Preserve required encryption-key material through an explicit recovery process separate from ordinary database backups. Wrong or missing keys MUST fail closed. Retain runner_file as an operator option. Track Doppler, Infisical and Vault integration as parity work with provider-specific contracts. C16

### 6.6 Compose and persistent storage

R34. Compose compatibility. Support service images and builds, variables, commands, profiles, networks, health checks, dependencies, resource settings, named volumes and supported bind/file mounts. Publish a tested compatibility matrix covering managed and raw deployment modes. Replace the current blanket prohibitions with capability and ownership rules. Unsupported fields must produce a precise preflight error.

R35. Compose ownership. Git-owned Compose remains repository-owned. Pasted or template-derived Compose remains stored resource configuration. Generated routing, credentials and overrides must be reviewable and must not create a second conflicting source of truth. Preserve required/optional variable semantics and existing configured values when reloading definitions. Track raw Compose as an explicit parity mode that preserves operator-owned labels and networking and identifies which managed features are disabled. Raw mode MUST preserve authorization, destination ownership and data-retention rules. State its resolution and reproducibility limits for external inputs. Reject a protected-deployment policy during preflight if the raw definition cannot satisfy its required release guarantees without alteration.

R36. Multi-component execution. Resolve or build each required image and record the selected set before application. Interpret long-running services, initialization jobs and optional components explicitly. Show component-level status, endpoints, logs and controls. A healthy web endpoint alone is insufficient when a required database or worker failed.

R37. Advanced host access. Support administrator-authorized advanced Compose needs through explicit capabilities where qualified. Distinguish paths/devices inside a guest from physical-host access. Do not silently discard unsupported fields or grant physical-host access through a convenience setting. Host mounts, privileged execution or devices that weaken an isolation profile require a different explicit trust profile and may make a destination ineligible for untrusted work.

R38. Storage identity. Record each binding's destination, Docker volume identity or canonical host path, mount destination, mode, ownership and resource references. Keep identity stable across rename, redeploy and agent replacement. Distinguish managed files, managed directories and pre-existing host files. Classify each storage object as platform-managed or externally owned. Importing or mounting an external object does not transfer ownership; any supported ownership transfer MUST be separate, authorized and auditable.

R39. Data retention and deletion. Stop, redeploy, remove resource and delete data MUST be separate operations. Retain persistent data by default when removing a resource. Resource deletion and automated cleanup MUST preserve externally owned volumes, files and directories, including when deletion of managed data was explicitly selected. A destructive action must name the exact data objects and affected resources. Cleanup must protect retained data, active work, shared references and required recovery points.

R40. Cross-host state. A local volume remains bound to its destination. Moving a resource with data requires explicit transfer, restore or a supported replication/storage model. Matching server tags or a new container name cannot create continuity of data.

Current Coolify supports both Git and pasted Compose, as well as several storage mount workflows. Those are the compatibility baseline. C3, C17

### 6.7 Database resources

R41. Database creation. Provide engine/version selection, target/network, stable persistent storage, generated credentials, resource limits, readiness and connection details through the UI and API. Default to private networking. Public exposure requires an explicit operation with endpoint and access implications shown.

R42. Application bindings. Let users bind a database to an application through credential references and valid network information. Show unreachable cross-server bindings rather than inventing a shared Docker network. Redeployment must preserve credentials and data unless an explicit change is requested.

R43. Engine-specific lifecycle. Implement configuration, readiness, start/stop/restart, credential rotation and supported backup/restore per engine. Major-version upgrades must validate data layout and compatibility and show a recovery procedure. Changing an image tag must not be advertised as a universal database upgrade.

R44. Engine coverage. Maintain the matrix below in implementation and documentation. PostgreSQL and Redis may ship first, but the remaining documented engine coverage stays in the product plan. "Managed" describes supported lifecycle operations, not automatic replication or a database availability SLA.

|Engine    |Reference scheduled backup|Reference dashboard restore|Rewrite requirement                                             |
|----------|--------------------------|---------------------------|----------------------------------------------------------------|
|PostgreSQL|Yes                       |Yes                        |First stateful slice, with a tested application restore         |
|Redis     |No                        |No                         |Early hosting support with explicit persistence/recovery limits |
|MySQL     |Yes                       |Yes                        |Engine-specific backup and import                               |
|MariaDB   |Yes                       |Yes                        |Separate version/configuration compatibility tests              |
|MongoDB   |Yes                       |Yes                        |Supported authentication and dump/import formats                |
|ClickHouse|Yes                       |No                         |Backup parity, explicit restore procedure or labeled improvement|
|Dragonfly |No                        |No                         |Hosting and persistence support with honest recovery limits     |
|KeyDB     |No                        |No                         |Hosting and persistence support with honest recovery limits     |
|SQLite    |Yes (experimental)        |Yes (experimental)         |Retain an explicit experimental status until validated          |

The reference matrix is documented by Coolify. Similar-looking database controls must not imply identical support across engines. C7

### 6.8 Backups and restores

R45. Separate recovery types. Expose platform backups, database backups and storage backups as distinct workflows. A platform database backup does not contain hosted application data. A storage archive does not prove transactional consistency for a running database. Application rollback remains a fourth, separate operation.

R46. Backup execution. Support immediate and scheduled backups with target, format, consistency method, timeout, retention, concurrency policy and execution history. Record resource identity, engine/version, size, digest and available locations. Use local staging and S3-compatible remote storage where configured. A storage backup MUST state whether writers remain active or are quiesced. For a stop-during-backup policy, record which containers were running, stop the required writers, create the archive, and attempt to resume those containers after success, failure or timeout. Do not start containers that were already stopped. Reconcile incomplete resume attempts and preserve their failure evidence. A live file archive MUST NOT be labeled an application-consistent database backup.

R47. Partial outcomes and retention. Distinguish dump/archive success from remote-upload success. Preserve the local recovery artifact after failed upload. Keep local and remote retention independent. Replaying a retention request after response loss must return its receipt without deleting another batch. Preserve audit and receipt atomicity.

R48. Restore workflow. Prefer restore into a new database or volume for validation. In-place restore must identify its target, replacement behavior, compatibility, writer coordination and recovery point. Restore ownership and permissions where relevant. Verify an application can read and write restored data before marking the recovery test passed.

R49. Platform recovery. Preserve the existing compatible-image/schema restore discipline and invalidation of restored sessions and active leases. Recover required secret keys and configuration. Enter a recovery mode that observes existing hosts and confirms ownership before resuming mutation. Do not recreate or delete healthy workloads merely because the control-plane database was restored.

Coolify documents separate instance, database and storage backup paths with different limits. Its current storage archive workflow does not establish a complete dashboard restore path. A tested restore UI in the rewrite is an improvement that should be identified as such. C18, C19, C20, N10

### 6.9 Service catalog and upgrades

R50. Versioned templates. Store template identifier, source version, original definition, user changes, generated values, component identities and resolved images. Generate passwords and stable identifiers once. Redeployment and catalog refresh MUST not regenerate credentials.

R51. Installation experience. Search and filter catalog entries, show required variables, domains, storage, architecture, resource needs and component roles, then install through the same resource engine as pasted Compose. Templates must not bypass authorization or storage rules.

R52. Update semantics. Distinguish pulling newer images under saved tags, changing application configuration and upgrading the catalog definition. Show a change preview for images, variables, mounts, networks, health and migrations. Catalog updates MUST not silently rewrite installed resources. Block or explain rollback when data compatibility prevents it.

R53. Catalog compatibility. Support a useful initial tested catalog and expand through a published fixture matrix. Import compatible reference templates with recorded origin/version and required attribution. An imported YAML file is not validated support until installation, persistence and update behavior pass.

Coolify installs a copied resource definition from a template, and later catalog changes do not overwrite the deployed copy. Preserve that distinction. C8

### 6.10 Previews and delivery automation

R54. Preview identity. Represent a preview as a task-, PR/MR- or manually derived resource set with separate routes, configuration, networks and data. Support single-image and declared Compose paths, including application, workers and isolated databases. Its lifecycle MUST be independent of the agent run and mutable workspace. Record the current immutable release and its verification coverage.

R55. Preview trust and credentials. Default automatic previews to trusted repository actors and authenticated viewers. Public-fork execution requires an explicit policy and qualified isolation. Production secrets, database bindings and writable volumes MUST NOT be copied by default. Use isolated seed data unless a separate data-copy operation is authorized. Preview access, source editing and deployment permissions are distinct. Limits account for build and runtime capacity.

R56. Preview lifecycle. Update a preview from its intended candidate, report provider/task status and URL, and expire it under a recorded policy. Define failed-update, retry, data retention and extension behavior. Ending an agent run MUST NOT remove a retained preview. A delayed close event or repeated cleanup MUST NOT delete production, external data or a newer unrelated preview. Closing the creator's client does not alter the preview lifetime.

R57. Delivery policy. Expose auto-deploy branch selection, path filters where supported, manual deploy, deploy webhooks, optional approval and rollback policy. Keep ordinary push deployment simple. Advanced operational templates must not become mandatory configuration for a normal application.

Reference preview behavior includes provider events, separate variables/domains and cleanup. C6

### 6.11 Ongoing operations, schedules and notifications

R58. Continuous observation. Collect server reachability, container state, readiness, disk usage and bounded CPU/memory samples independently of deployment jobs. Combine event-driven updates with periodic reconciliation. Document collection and stale-state thresholds. An unreachable agent means current state is unknown, not that every workload stopped.

R59. Distinct logs. Provide build, deployment and runtime log views with clear source, time, component, ordering and retention. Runtime logs continue after a deployment ends. Live follow must reconnect and indicate gaps or truncation. Keep audit records separate. External log drains and metrics export are part of the operations plan.

R60. Resource actions. Support start, stop, restart, redeploy, inspect, force rebuild and supported rollback with resource-specific behavior. Display desired state, last operation result and latest observed state separately. Expose diagnostics and resource limits without requiring generic run IDs as the primary navigation.

R61. Terminals. Provide container and server terminals through short-lived grants bound to actor, destination and resource. Container access and host access require different permissions. Support server-level disablement, revocation and session termination. Audit session metadata without placing permanent credentials in the browser.

R62. Scheduled commands. Bind a schedule to a resource and component, then resolve its current container on the correct target at execution. Record occurrence identity, schedule version and actual target. Define timezone, DST, timeout, overlap and missed-run policy. The default should skip missed runs and forbid overlap rather than create an uncontrolled catch-up burst.

R63. Durable notifications. Start with email and generic webhooks, then complete the declared channel matrix. Users select events per channel. Include deployments, backup/upload failures, schedules, resource state, server reachability and disk pressure. Persist delivery attempts, bounded retries and deduplication keys. Test delivery must be distinct from exercising an actual event.

R64. Maintenance. Provide owned-image/cache cleanup, disk-pressure reporting, agent/proxy version visibility and supported upgrade procedures. Never prune all host resources indiscriminately. Platform and agent updates must preserve running applications, retain a recoverable control-plane state and explain compatibility limits.

Reference operations include continuous status collection, runtime metrics, terminals, container schedules and channel-specific notifications. C9, C21, C22

### 6.12 UI, API and compatibility

R65. Task-centered collaboration and direct operations. The main collaboration view is a durable Task showing its goal, source changes, agent/human activity, workspace status, checkpoint, required checks, candidate, preview, budget and deployment history. Provide direct navigation to projects, environments, applications, databases/stacks, machines, sources and settings. Users can host or operate an application without an agent task. Existing generic runs remain execution details and advanced automation, not a substitute for the new Task.

R66. Complete UI workflows. After documented platform installation, every supported resource can be created and operated without seeded fixtures, SQL or ad hoc API calls. Preserve non-secret form input after validation failure. Show missing prerequisites and direct actions. Verify keyboard access, responsive views and authorization failures.

R67. One application contract. UI, API, CLI, webhooks and MCP MUST call the same authorized application operations. Regenerate the TypeScript client from the versioned contract. Preserve legacy NeroCD routes during the supported migration window. Do not expose a shortcut that bypasses target, secret, approval or data-ownership rules.

R68. Evidence and releases. A capability is complete when schema, API, permissions, audit, UI, operational behavior and tests agree. Publish supported OS, architecture, Docker/tool versions and measured limits. Use candidate-specific checks and runtime evidence. Do not claim performance gains from choosing Go without measurement.

### 6.13 Durable tasks and workspaces

R69. Task identity and activity. Create a durable Task with owner, collaborators, goal, project, source lineage and linked environments. Support human, integration and agent actors, several runs, candidates and review decisions. An execution retry MUST NOT create a different user task or erase prior evidence. Activity must distinguish requested action, observed outcome and agent narrative.

R70. Durable admission and attempt authority. Acknowledge accepted work only after its task/source references and queued intent are durable. Atomically record a claimed attempt, its resource reservation and lease authority. Bound retries by the task's original deadline and aggregate budget. Reject stale state changes, credential requests and artifact/candidate publication. Reconcile possible external effects before conflicting mutations or releasing uncertain reservations. Do not claim exactly-once execution.

R71. Workspace lifecycle. Create an isolated checkout and declared tool environment per workspace, with a named base revision, branch or content snapshot and owned disk allocation. Separate writable work from immutable candidates. Use one writer by default. Permit concurrent experiments through separate workspaces or an explicit coordination protocol. Workspace cleanup MUST retain required checkpoints and never delete unrelated repositories or data.

R72. Checkpoints and continuation. Publish immutable checkpoints containing the declared source changes, selected uncommitted files, environment recipe and supported agent continuation metadata. Encrypt and authorize sensitive content, exclude credentials and report excluded files. Identify the latest durable checkpoint and possible lost work after interruption. Publish an adapter-specific distinction between session resume, restart from source/checkpoint and evidence-only recovery. No universal process-memory or cross-architecture migration guarantee is implied.

R73. Human attachment and handoff. A teammate can inspect the diff, logs, checks and preview and attach to supported workspace tools under a short-lived grant. Before writable takeover, revoke or pause the old writer and confirm quiescence. If the old host cannot be fenced, create a separate workspace from a checkpoint rather than granting a second writer to the same data. Preserve ownership generations and the handoff event. The first release can use terminal/editor attachment integrations without implementing a new IDE.

R74. Evidence persistence and disclosure. Retain task activity, checkpoints, diffs, command/check results and artifact references independently of temporary worker disks. Enforce project/task permissions on every download and stream. Indicate missing, truncated, stale and partially uploaded evidence. Apply separate retention to task history, logs, checkpoints, previews, caches and releases. Record deletion and preserve required references without exposing removed secrets.

R75. Source handoff. Allow a supported agent to submit a patch, commit or review branch tied to the task and candidate. Preserve the distinction between a source checkpoint, provider commit, pull request and deployed artifact. Repository write operations use the same explicit task authority. Branch restriction claims MUST be enforced by the provider or by a broker that holds broader credentials outside the guest. A repository-wide token handed to an agent cannot be described as branch-scoped merely because its task metadata names one branch.

### 6.14 Placement, availability and budgets

R76. Typed capabilities. Record machine OS/architecture, qualified execution backends, resource capacity, device capabilities, tool versions and observation freshness. Separate native target support from emulation and from cross-compilation. A successful capability probe is evidence of a tested prerequisite, not proof that every workload is supported. Revalidate material host changes and reject unsupported operation/backend combinations before execution.

R77. Placement and reservations. Filter by authorization, trust, data location, required isolation, architecture and hard resource constraints before applying scheduling preferences. Account for CPU, memory, disk, GPU/VRAM where supported and concurrency, with local enforcement and bounded overcommit policy. Reserve persistent application capacity separately from opportunistic jobs. Prefer cache locality only among eligible destinations. A tag match MUST NOT override stable destination or volume ownership.

R78. Budgets and expiry. Apply task/project limits for elapsed time, aggregate resource consumption, concurrency, workspace and preview storage, network usage where metered, and configured model/cloud spending. Show measurement gaps instead of invented precision. Budget is shared across retries and subtasks. Enforce stop/reject behavior at a documented boundary, with safe cleanup and retained evidence. Preview expiry must include a reviewable extension policy.

R79. Availability policy. Let owners declare always-on or opportunistic capacity, schedules and drain behavior. Show queued work blocked by unavailable eligible capacity. A submitting-client disconnect must not end accepted remote work. Sleep or loss of an executing host causes explicit interruption/uncertainty and supported recovery. Do not automatically move stateful applications or claim continuity of uncheckpointed work. Rejoining hosts must reconcile old authority before accepting conflicting work.

R80. Cloud fallback. Support adding compatible cloud machines through the same enrollment and policy model. Automated provisioning or fallback requires configured provider credentials, locations, allowed data, spending limits and cleanup ownership. Never silently upload private source, checkpoints, models or datasets to a different trust/location domain. When fallback is disallowed or exhausted, preserve the task and explain the blocked condition. Provider provisioning is separate from the first manual-capacity workflow.

### 6.15 Agents, isolation and models

R81. Qualified isolation profiles. Run untrusted code in a supported VM boundary, with an explicit host/kernel/backend/guest-image support matrix, patch/update policy and resource controls. Distinguish task, preview and production boundaries. Use disposable guest disks for ephemeral work and explicit bindings for retained data. Never claim that VM isolation makes arbitrary malicious code harmless. Native Mac, Windows and Linux implementations require their own tests. A WSL distribution alone is not a qualifying VM isolation boundary. GPU/host-device access needs a separate qualified profile. W13

R82. Filesystem and network grants. Deny access to personal home directories, host management interfaces, host Docker sockets, personal SSH agents and private networks unless explicitly granted for the task. Enforce network policy outside the untrusted guest, including IPv4/IPv6, host/private/metadata ranges, proxy paths and address revalidation after DNS resolution. Grant specific destinations, ports and data mounts with modes and lifetimes. An ingress tunnel only exposes authorized services and must not become a general LAN route. Revocation behavior must be exercised.

R83. Task-scoped authority. Associate a stable agent/service principal with short-lived run grants for task/project, repository operations, environment/destination, artifact/cache namespace, secrets, model endpoints and expiry. Enforce restrictions in the actual provider, broker or runtime, not just in a signed claim that a downstream service ignores. Prefer brokered access where narrow downstream credentials do not exist. Do not place production credentials in ordinary agent workspaces. Apply revocation to active sessions and outstanding operations as supported and report residual uncertainty. Grants do not promise to erase data already legitimately read.

R84. Agent adapter contract. Version an adapter for starting, observing, interrupting and collecting one supported coding agent. Declare its authentication, model requirements, tool access, checkpoint/resume semantics, costs where available and cancellation behavior. Preserve prompts, tool events and results according to project retention/disclosure policy. Agents use ordinary authorized build, preview and task APIs. Adapter availability must not depend on the initiating laptop staying online.

R85. Human production authorization. Agent-produced changes require an authorized human decision over the concrete production plan after required checks. Enforce this at the application boundary for UI, CLI, MCP, repository automation and webhooks. Agents cannot change protected deployment policy or replace trusted checks to authorize themselves. A reviewed merge may satisfy the human decision only under an explicit policy that binds the accepted source/artifact, required evidence and final target plan; an unrelated historical PR approval is insufficient. Content or target changes require a new decision.

R86. Model services. Register an authenticated model endpoint with allowed projects/tasks, model identity where available, context/request limits, concurrency, usage budget and availability. Record the selected model/service in task evidence and expose unavailable or unsupported behavior clearly. Access to the endpoint does not grant a shell, GPU device or model-host files. Separately qualify native Mac/GPU serving, model installation, storage, licensing constraints, upgrades and device ownership before claiming managed model-hosting support.

### 6.16 Shared results and verification

R87. Effective build inputs. Freeze a canonical build input manifest including effective context bytes after ignore rules, relevant named contexts, Dockerfile/frontend, target, platform, resolved base identities, builder/toolchain version, effective build settings, dependency material and output-affecting secret generations. Include submodule/LFS material where used. Retain source lineage separately from content equivalence. A live mutable checkout MUST NOT change while its candidate input is being captured without detection and retry.

R88. Conservative artifact reuse. Reuse an existing authorized artifact only when the manifest and refresh policy establish eligibility, the artifact is available with its expected identity, and current policy permits it. Unknown mutable external inputs require a declared freshness bound or disable complete-artifact reuse. Freshness-based reuse does not prove those external inputs remained unchanged. A matching Git SHA is insufficient. Record why reuse was accepted or refused and retain original provenance. Force rebuild, mutable-input refresh and check rerun are separate operations.

R89. Cache trust and transport. Qualify registry-backed build-result caching first. Scope read and write permissions separately by team/project/trust class. Untrusted contributions MUST NOT overwrite a protected cache reference or supply authoritative check results. Prefer immutable per-run exports and controlled shared-reference updates. Warn that caches can contain private build material and apply the same disclosure boundary as source. Distinguish shared layer results from builder-local compiler/package cache mounts. Cache eviction MUST NOT delete pinned releases or backups. W5, W6

R90. Required checks and reuse. Every check result records its exact source/artifact subject, definition version, execution environment/platform, time, outcome and retained evidence. Trusted policy chooses mandatory checks. Agent-edited tests can be useful additional evidence but cannot silently redefine the deployment gate. Runtime checks must execute the release artifact. Reuse of successful checks requires matching source/artifact subjects, check-definition and trusted-policy versions, fixtures, relevant environment and freshness criteria. Security or policy changes may require re-verification without rebuilding.

R91. Platform and attestation identity. Record target OS/architecture/variant, native or emulated execution and image index/platform-manifest identities. Display verification coverage per supported platform. Do not infer native correctness on one platform from a result on another. Seal index-embedded attestations before approval of that index, or retain later evidence by immutable references without mutating the approved identity. Deployment selects the approved eligible platform digest and records it. W10, W11

R92. Artifact availability and retention. Publish through authorized, bounded transfers with digest verification and explicit completion state. Candidate publication and deployment MUST NOT claim an artifact exists when required uploads failed. Protect artifacts referenced by retained releases, active deployments and required evidence, with separate cache and checkpoint policies. Reconcile registry/object-store retention and garbage collection. Report unavailable/corrupt artifacts and require a new build/approval where recovery changes their identity. Qualify any self-hosted storage/registry combination before advertising its guarantees.

### 6.17 Application care and deeper capabilities

R93. Reviewable environments. A task preview must show the candidate it serves, health of required components, access policy, data origin, expiry and source of verification. Default access is authenticated and project-scoped. Optional share links need explicit audience, expiry and revocation. A reviewer can inspect without receiving source-edit, terminal or production rights. A retained preview remains available only while its declared runtime dependencies remain available.

R94. Failure reproduction. Create a reproduction environment from an exact retained release, supported environment recipe and authorized diagnostic/configuration data. Show missing inputs and external dependencies. Production data copying is a distinct authorized operation with sanitization and retention rules. Block outbound email, payments, webhooks and other external side effects unless granted. Reproduction means a documented reconstruction attempt, not a claim of exact historical system state.

R95. Supported application updates. Maintain an application/version-path contract describing preflight, compatibility, backup/consistency method, migration, verification, expected interruption and tested recovery. Verify recovery prerequisites before mutation. Preserve reports for partial failure and unresolved writer state. An available newer image or VM snapshot does not establish a safe upgrade or database rollback. Unsupported update paths must be labeled manual/unsupported.

R96. Guided migration between machines. Plan destination capability/resource checks, image compatibility, secrets, data transfer, writer coordination, verification and route cutover. Name each data object and recovery point. Prevent both installations from concurrently performing exclusive schedules or consuming external events during cutover. Account for post-cutover writes in recovery. Cross-architecture migrations require supported images and application/data-format compatibility, not just successful file copy.

R97. Restore testing. Restore a selected backup into an owned disposable environment, verify application startup and meaningful data reads/writes, retain the result and then clean up by ownership. Record engine/application versions, recovery artifact identity, elapsed time and missing dependencies. Keep restored credentials, production data and outbound integrations under explicit policy. A successful archive extraction alone MUST NOT mark the application recovery test passed.

R98. Database-aware deployment planning. Describe migration direction, supported version pairs, backward/forward compatibility, writer coordination and the effect on image rollback. Use declared application support and executed checks. Do not infer semantic safety from SQL syntax alone. A plan must explain when rollback needs data restoration, a forward fix or interruption, and revalidate preconditions before applying it.

R99. Progressive delivery. Offer gradual exposure only for eligible workloads with a supported routing strategy, defined health metrics, sufficient observation criteria and abort/recovery behavior. Persist the rollout state and decisions. Do not automatically roll back an incompatible schema change merely because a metric crossed a threshold. Show unsupported stateful/shared-session cases explicitly.

R100. Drift detection and reconciliation. Compare observed platform-owned configuration with intended state, attribute known manual changes and show a reviewable reconciliation plan. Mark unknown/stale observations. Observe external resources without claiming ownership or deleting them. Apply plans through normal authorization, concurrency and data-retention rules.

R101. Vendor-managed application packages. Define versioned packages with verifiable publisher identity, artifact identities, supported platforms, configuration/data requirements, update checks and recovery contract. Customers approve installation/update policy and retain local control. Publisher authority to release a package does not grant remote administration of customer machines. Revocation, compromised updates and pinned versions need explicit behavior before this capability is declared supported.

R102. Evidence-based AI assistance. AI investigation and optimization proposals must link the failing observation or measured baseline, proposed change, actual checks/benchmarks and resulting measurements. Label untested hypotheses and measurement uncertainty. An agent's narrative cannot substitute for executed verification, approve production or author authoritative evidence without the corresponding trusted execution record.

## 7. Migration design

### 7.1 Migrate NeroCD's existing domain

Do not discard the existing code or history. Introduce the new domain alongside the old contract, then move workflows in bounded steps.

1. Add project-level environments, typed resources and server/destination identities.
2. Map each existing service-specific environment to a resource instance or deployment binding.
3. Keep legacy service/environment/deployment IDs available through mappings and compatibility reads.
4. Require an explicit grouping decision before combining old environments with the same name. Equal names do not prove shared ownership or variable scope.
5. Give image/database/template resources a source type that does not require Git.
6. Preserve active-deployment exclusion at the mapped resource binding.
7. Convert source evidence into build/release records without inventing missing provenance.
8. Preserve historical approvals as approvals under their original contract. Do not relabel them as approvals of newly introduced immutable plans.
9. Drain or explicitly reconcile in-flight work during the upgrade. Old agents cannot execute new operation types without the required capabilities.
10. Keep applied migration checksums unchanged and rehearse upgrade/restore with representative prior data.

Add Tasks and Workspaces without relabeling every historical TaskRun as an existing collaborative workspace. Historical runs can be linked to a new task through an explicit mapping. Preserve unknown checkpoint, artifact, platform and verification fields as unknown rather than inventing evidence.

Map existing enrolled runners to stable machine identities only after destination ownership is established. Re-probe capabilities and require an explicit trust/isolation profile before assigning untrusted agent work. Preserve legacy trusted Docker operation where declared. Existing service-account roles remain valid but do not automatically become narrow task grants or authorize cloud data movement.

The current service-specific environment can remain an internal binding during the transition. The UI can create the necessary rows together so users do not need to understand legacy storage structure.

### 7.2 Migrate workloads from Coolify

Provide a read-only discovery and compatibility report first. Prefer supported API/export inputs and explicit manifests over depending on undocumented database tables.

The report must cover source/build settings, resource types, domains, variables, inaccessible secrets, networks, storage, database versions, schedules and unsupported extensions.

The supported first migration path is:

1. Import configuration into a versioned intermediate manifest.
2. Map teams, projects, environments and destinations.
3. Request missing credentials rather than treating absent secret values as empty strings.
4. Create destination resources under temporary domains and isolated dependencies. Keep destination deployment triggers, schedules and external-event consumers disabled during validation.
5. Make an initial data copy using engine-compatible database backup/restore and a consistency-aware file transfer.
6. Disable source deployment triggers, schedules and other mutating automation for the moved resource.
7. Coordinate remaining application writers and perform final synchronization.
8. Verify readiness, application reads/writes, file permissions and external integration settings.
9. Transfer route or DNS ownership and record the cutover.
10. Enable the intended destination triggers, schedules and external-event consumers only after ownership and cutover are explicit.
11. Retain a recovery window with an explicit statement about writes made after cutover.

Current Coolify's own instance-migration documentation describes reconstruction and state transfer rather than a universal application import/export operation. The rewrite should not assume a complete export format exists. C23

Existing-container adoption is a separate mode. It requires explicit transfer of mutation ownership, preservation of real volume/network identities, and protection against both controllers managing the same resource. Reading Docker labels is insufficient evidence of a completed handoff.

### 7.3 API compatibility

Maintain native NeroCD API stability through versioned additions. Record Coolify endpoints required by target users and implement adapters where justified.

A compatibility test must cover behavior, authorization, error responses and lifecycle outcomes. Matching endpoint names is insufficient. Feature parity and API compatibility must have separate release statements.

## 8. Acceptance scenarios

These scenarios define the corrected product outcome. They are proposed tests, not claims that the current code passes them.

|ID |Scenario                                        |Required result                                                                                                                                                                                                                                                                      |
|---|------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
|A01|New installation and remote server              |UI-assisted enrollment produces a ready server/destination without hand-editing database rows                                                                                                                                                                                        |
|A02|Public repository with Dockerfile               |Build and deploy through the UI, then open a valid HTTPS endpoint                                                                                                                                                                                                                    |
|A03|Private repository with automatic builder       |Scoped credentials work, source resolves, image builds and secrets do not leak into normal logs                                                                                                                                                                                      |
|A04|Static files and compiled SPA                   |Both paths serve assets and deep links using their declared build/output settings                                                                                                                                                                                                    |
|A05|Registry image supplied by tag                  |Platform records the selected digest and runs an ordinary third-party image without a revision header                                                                                                                                                                                |
|A06|Failed source build                             |Existing containers and routes continue serving the previous release                                                                                                                                                                                                                 |
|A07|Remote build server                             |Image reaches the deployment host through the declared registry path with architecture and pull checks                                                                                                                                                                               |
|A08|Two apps in one production environment          |They deploy independently while each resource prevents conflicting active mutation                                                                                                                                                                                                   |
|A09|Duplicate or delayed Git webhook                |Retries deduplicate and an older event does not silently replace newer desired state                                                                                                                                                                                                 |
|A10|Protected deployment                            |Approval identifies a resolved release and target; branch movement cannot substitute content                                                                                                                                                                                         |
|A11|Eligible stateless replacement                  |New version passes readiness before traffic moves; failed readiness retains the old working version                                                                                                                                                                                  |
|A12|Fixed host port or exclusive state              |Platform selects and explains an appropriate replacement strategy without promising universal zero downtime                                                                                                                                                                          |
|A13|Two hosts with identical tags                   |Deployment, rollback, backup and volume operations remain on their authorized destination                                                                                                                                                                                            |
|A14|Lost lease after possible Docker mutation       |Recovery observes actual effects and blocks conflicting work until ownership is resolved                                                                                                                                                                                             |
|A15|Git and pasted Compose                          |Both support mixed builds/images, variables, profiles, networks, volumes and component-level status; secret interpolation reaches the authorized workload while stored release evidence and ordinary exports retain references                                                       |
|A16|Stack with one-shot init, web and database      |Successful initialization is not reported as failed solely because its container exited                                                                                                                                                                                              |
|A17|PostgreSQL plus application                     |Generated credentials and binding work; data remains after application and database redeploy                                                                                                                                                                                         |
|A18|Credential rotation                             |Engine credentials and dependent bindings change through a recoverable operation                                                                                                                                                                                                     |
|A19|Rename and remove a stateful resource           |Rename preserves volume identity; removal retains managed data unless explicitly deleted and always preserves externally owned volumes and host paths                                                                                                                                |
|A20|Multiple routes and certificate renewal         |Correct components receive traffic; route conflicts and failed renewal retain useful status and working configuration                                                                                                                                                                |
|A21|Template install and redeploy                   |Generated passwords stay stable; component data survives                                                                                                                                                                                                                             |
|A22|Catalog update                                  |Installed definitions remain unchanged until an explicit upgrade, whose changes are reviewable                                                                                                                                                                                       |
|A23|PR preview and close                            |Preview has isolated route/configuration/storage; cleanup does not affect production                                                                                                                                                                                                 |
|A24|Untrusted fork preview                          |Default policy blocks execution or uses explicitly authorized isolation without production credentials                                                                                                                                                                               |
|A25|Backup dump succeeds, remote upload fails       |Local recovery data remains and the partial outcome generates an actionable notification                                                                                                                                                                                             |
|A26|Database and volume restore                     |Restore into new resources and verify application data, writes and file permissions                                                                                                                                                                                                  |
|A27|Platform restart and restore                    |Existing workloads/routes continue; restored control plane invalidates stale authority and reconciles before mutation                                                                                                                                                                |
|A28|Agent unreachable                               |Dashboard marks observations stale/unknown and reports reachability without inventing healthy or stopped state                                                                                                                                                                       |
|A29|Runtime logs after deployment completion        |Live container logs remain accessible with reconnect behavior and clear retention                                                                                                                                                                                                    |
|A30|Container replacement before scheduled execution|Task runs in the current intended component on the correct target with recorded occurrence identity                                                                                                                                                                                  |
|A31|Scheduler outage, overlap and DST               |Documented missed-run and overlap behavior holds without duplicate unintended executions                                                                                                                                                                                             |
|A32|Terminal permission revoked                     |New grants are denied and existing sessions terminate according to policy without leaking permanent credentials                                                                                                                                                                      |
|A33|Coolify migration with DB and uploads           |Configuration and both data types verify before cutover; source mutating automation is disabled before final synchronization, destination automation stays disabled until handoff, no scheduled side effect runs from both controllers, and recovery accounts for post-cutover writes|
|A34|Existing NeroCD upgrade                         |IDs/history remain accessible, environment grouping is explicit and old runners cannot execute incompatible work                                                                                                                                                                     |
|A35|Unauthorized direct API requests                |Cross-project reads and unauthorized mutations, terminal sessions, secret reveals and destructive data actions are denied                                                                                                                                                            |
|A36|Release claims core replacement                 |All declared core workflows pass for the same candidate; partial and extended-parity items remain visible                                                                                                                                                                            |
|A37|Storage backup fails after quiescing writers    |Previously running containers are resumed or an unresolved recovery failure is reported; previously stopped containers remain stopped and backup failure evidence is retained                                                                                                        |

### 8.1 Workspace and owned-machine acceptance

The deferred full task demonstration must pass the applicable workspace cases together with its hosting dependencies. Later capability rows are acceptance targets for those tracks, not claims of current hosting support or the active first demonstration.

|ID |Scenario                                                            |Required result                                                                                                                                                                       |
|---|--------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
|A38|Task accepted, submitting laptop disconnects                        |Remote work continues under its supervisor; reconnect shows the same task, attempts, checkpoint and evidence                                                                          |
|A39|Worker disappears and later returns                                 |State becomes stale/uncertain; retry follows the supported checkpoint contract; the old fence cannot publish or mutate the new candidate and possible effects are reconciled          |
|A40|Human takes over while an agent is writing                          |Old write authority is quiesced or a separate checkpoint-derived workspace is created; no two uncoordinated writers own the same workspace                                            |
|A41|Checkpoint restore on a compatible host                             |Declared source changes and supported continuation state restore; excluded secrets and lost uncheckpointed work are explicit                                                          |
|A42|Concurrent reservations and repeated retries                        |Capacity and aggregate budget are enforced; retries do not reset limits and uncertain allocations are reconciled before reuse                                                         |
|A43|Wrong architecture, absent KVM or incompatible backend              |Placement explains ineligibility before execution and does not silently weaken isolation or treat emulation as native validation                                                      |
|A44|Task attempts host/LAN access                                       |Personal files, Docker socket, SSH agent, management and metadata endpoints and private networks remain denied under the declared profile, including IPv6 and DNS/address-change cases|
|A45|Restricted repository write and revoked grant                       |The actual provider/broker blocks unauthorized refs and actions; active sessions follow revocation policy and no broad credential is mislabeled as narrowly scoped                    |
|A46|Agent changes tests or pushes to auto-deploy branch                 |Trusted mandatory checks and human production authorization cannot be bypassed; source tests and final-artifact runtime tests retain distinct subjects                                |
|A47|Code, target or relevant configuration changes after approval       |The previous decision cannot authorize the changed plan; unchanged approved artifact identity is observed at deployment                                                               |
|A48|Compatible build on a second worker                                 |Authorized registry cache is imported with step-level evidence and measured time/bytes; builder-local cache mounts are reported separately                                            |
|A49|Same Git SHA with changed build inputs                              |Base/config/toolchain/context/dependency changes affect eligibility; complete-artifact reuse follows the manifest and refresh rule and retains original provenance                    |
|A50|Output-affecting build secret rotates                               |The required cache/reuse invalidation occurs through opaque version/generation without exposing the value or a guessable public hash                                                  |
|A51|Untrusted task exports cache or check metadata                      |It cannot overwrite trusted shared cache references or publish a passing mandatory-check record; private cache reads remain authorized                                                |
|A52|Reuse checks after fixture/policy/freshness change                  |Invalid evidence is rerun or rejected while unchanged artifact reuse remains a separate decision                                                                                      |
|A53|Multi-platform candidate and later attestation                      |Coverage and native/emulated status are platform-specific; selected child digest matches the approved index/plan and late evidence does not mutate approved identity                  |
|A54|Agent completes and builder drains                                  |Independent preview stays healthy on its declared available target; review does not depend on the build worker or creator session                                                     |
|A55|Viewer opens a private preview or share link expires                |Authorized preview access works without source-edit/deploy rights; revoked/expired sharing stops according to documented policy                                                       |
|A56|Preview expiry, repeated cleanup and partial storage failure        |Only owned disposable objects are removed; production, external data, retained checkpoints and pinned releases remain protected; failures are visible                                 |
|A57|Model endpoint unavailable or GPU capacity occupied                 |Explicit dependency/capacity status, scoped access and usage policy apply; the task does not gain host-shell/device access or silently switch data locations                          |
|A58|Reproduce a failed release using copied production data             |Exact available artifact/configuration evidence is recorded; missing historical inputs are shown, data-copy authority holds and external side effects are blocked                     |
|A59|Move an application with uploads and a database                     |Destination and data verify, writers and automation are coordinated, cutover is recorded and recovery handles post-cutover writes                                                     |
|A60|Supported application upgrade fails after migration                 |The version-path contract governs recovery; incompatible image rollback is not advertised as safe and partial state remains actionable                                                |
|A61|Restore test opens application and exercises data                   |Result identifies backup and versions, verifies meaningful reads/writes, retains evidence and cleans only its owned isolated environment                                              |
|A62|Progressive rollout encounters health failure or incompatible schema|Eligibility, observation and recovery rules are enforced; no unsafe generic rollback is inferred from a failed metric                                                                 |
|A63|Manual configuration drift or vendor package update                 |Reconciliation/update plans show changes and retain local approval, identity checks and external-data ownership                                                                       |
|A64|Owned hosts unavailable and fallback disabled or capped             |Task remains durably blocked with a reason; no unauthorized cloud upload/provisioning or budget reset occurs                                                                          |
|A65|Artifact upload fails, cache is pruned or image disappears          |Partial publication is not a valid release; cache pruning preserves pinned artifacts and unavailable content produces explicit recovery or new approval                               |
|A66|AI proposes a build optimization or failure fix                     |Report links baseline, change and actually executed verification/measurements, and distinguishes untested conclusions                                                                 |

Additional fixture matrices must cover host/VM adapters, agent adapters and resume modes, network policy, native and emulated CPU architectures, cache and verification reuse, model endpoint types, builder strategies, database engines, Compose fields, provider events, proxy adapters and imported template versions.

## 9. Operational and implementation standards

Preserve useful existing guarantees while changing the product model:

- PostgreSQL remains authoritative for accepted work, leases, operation history and audit.
- A side effect is not exactly-once merely because a job has an idempotency key. Record intent and observed results and reconcile uncertainty.
- Background observation is distinct from active deployment ownership.
- OCI images, build cache, task checkpoints, verification evidence, runtime logs and backups have separate storage and retention policies.
- Secret-management key loss and backup restoration must be exercised, not handled only in documentation.
- Generic operational templates remain supported, but a user must not author one to create a normal application or database.
- Start with bounded job concurrency and measured quotas. Publish scale claims only after representative load testing.
- Use existing PostgreSQL, fencing, spool, secrets, enrollment, provenance, Compose, web, production, restore and release gates where applicable. Extend them for hosting workflows instead of replacing them with a parallel acceptance framework. N11

Retain the current API/application/store layers. Suggested additional modules cover tasks, workspaces, machine capabilities and reservations, grants, artifacts/verification, resources, builds, releases/plans, destinations, routes, storage, databases, previews and backups. These are code boundaries, not a requirement for separate services. Prefer the smallest extension of the existing durable queue and reconciliation mechanisms that satisfies the next complete journey.

### 9.1 Product and resource measurements

Measure time from task submission to eligible placement, retained candidate, healthy preview and human review. Show queue reasons, interruption recovery outcomes, checkpoint age and preview availability. For builds, distinguish cache-hit steps, downloaded/uploaded bytes, wall time, complete-artifact reuse and reused versus rerun checks. A cache percentage alone does not prove saved time.

Track resource allocation and observed consumption for persistent applications, previews, builds, agent sessions and model services separately. Record the capacity reserved for always-on work and how much opportunistic capacity is actually available. Do not claim a fixed number of customers from host RAM alone.

Owned hardware has power, maintenance, connectivity, storage and replacement costs. Hosted-service planning must additionally account for control-plane operations, ingress/relay traffic, retained artifacts/checkpoints, backup capacity, cloud overflow and any model-provider usage. The earlier hosted application cost model does not price this expanded agent/GPU workload without new workload assumptions. This revision sets measurement requirements and makes no new pricing claim.

## 10. Delivery plan

Hosting parity is the active priority. Complete the repository/image-to-HTTPS
hosting journey and its operation/recovery paths before the broader workspace
product. The [implementation plan](plan.md) identifies accepted foundations,
remaining gaps and candidate-specific acceptance.

| Order | Active hosting outcome | Required evidence |
| --- | --- | --- |
| H1 | Practical repository/image-to-HTTPS hosting: onboarding, private Git, required variables and secret delivery, trusted deploy-on-push, deployment policy, production TLS qualification, runtime logs and continuous status | Applicable R01–R33 and R57–R68; A01–A14, A20, A27–A29, A35 and relevant matrix/failure/recovery cases |
| H2 | Useful stateful hosting: Compose, persistent storage, PostgreSQL/Redis, application bindings, initial tested catalog, backups and application-verified restores | R34–R53 and related secrets/operations; A15–A22, A25–A27, A37 |
| H3 | Remaining core parity: PR/MR previews, remote builders/image delivery, provider/build/database coverage, terminals, schedules, notifications, authorized API/CLI/MCP | Remaining R01–R68 and core hosting matrices; applicable A01–A32, A35–A37 together on the release candidate |
| H4 | Declared migration and extended compatibility | R67 and section 7; A33–A34 plus explicit migration, unchanged-client and extended-infrastructure matrices |

Order individual slices by real dependencies and verified gaps. Any necessary
legacy-ID/permission mapping or data ownership protection ships with the hosting
behavior it protects; this sequence does not permit unsafe migration shortcuts.
A narrow deployment demo is not complete Coolify parity.

Before starting a workspace-track change, record the exact hosting R requirement,
evidence of need now, minimum scope and A acceptance test. Remote builders,
registry delivery, cache, immutable releases, approvals, permissions and runtime
observations can proceed through their hosting contracts without a durable
collaboration Task, coding agent or general machine reservation system.

Unaccepted workspace work remains a coherent recoverable checkpoint. Preserve
it and unrelated changes without deletion, reversion or history rewriting.
Do not integrate it because work has already been spent on it.

### 10.1 Deferred workspace demonstration

|Gate                                |Deliverable                                                                                                                                                                   |Exit conditions                                                                                                                               |
|------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------|
|D0: Machine and authority foundation|Shared environment/resource model, Task/run distinction, stable machines, one qualified Linux VM backend, capability checks, reservations, managed secrets and task grants    |A bounded remote task survives client disconnect; unauthorized/stale work is rejected; legacy data and destination ownership remain valid     |
|D1: Retained work and shared builds |One coding-agent adapter, isolated checkout, source checkpoints, BuildKit on remote workers, registry cache, authorized artifact publication and explicit build identity      |Task produces a retained candidate; a second compatible builder imports cache; interruption and budget behavior are demonstrated              |
|D2: Working environment and handoff |App plus seeded isolated PostgreSQL preview, private ingress/TLS, runtime observations/logs, trusted checks against the final artifact, teammate review and supported takeover|Another user opens the task and working application after the agent run ends; evidence and permissions agree; cleanup preserves data ownership|
|D3: Reviewed deployment             |Target-specific plan, human production approval, same-artifact promotion, mutation fencing and supported failure recovery                                                     |The full journey passes together, including changed-source/plan, lost-worker, revoked-grant, missing-artifact and expiry cases                |

D0–D3 remain the later workspace demonstration gates. They do not lead the active delivery plan or block trusted-host hosting. Their Task, agent, checkpoint, general reservation, model/GPU and vendor-package work stays deferred unless a bounded part passes the hosting-dependency test. One architecture/backend may qualify a later declared milestone only with explicit limits.

Implement conservative complete-artifact reuse and verification reuse after input/evidence recording is dependable. The deferred workspace demonstration may rebuild through shared layer cache and rerun checks. Its schema and UI must already distinguish these outcomes so later reuse does not require misleading history.

### 10.2 Subsequent capability tracks

H is the active hosting track. P/M/R/O/V retain their future scope; a bounded part may advance only when the hosting-dependency test establishes a current hosting need. The broader workspace tracks must not displace or delay hosting replacement.

|Track                            |Deliverable                                                                                                                                                               |Completion evidence                                                                                                 |
|---------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------|
|H: Core hosting and compatibility|Direct source/image/Compose hosting, automatic builders, broader databases/catalog, storage, operations, schedules, notifications and Coolify migration                   |Original hosting cases A01–A37 and declared parity matrices pass for the release candidate                          |
|P: Broader owned-machine pool    |Native Mac adapter, qualified Windows VM and trusted WSL paths, qualified NAS roles, additional architectures, power/availability policies and explicit cloud provisioning|Backend-specific isolation, placement, recovery, networking and capacity fixtures pass                              |
|M: Owned model services          |Endpoint integration, supported model lifecycle, native/GPU execution profiles, model storage and quotas                                                                  |Model/driver/device matrix, authorization and interruption/upgrade behavior pass; measured resource use is available|
|R: Reuse and larger workloads    |Conservative full-artifact/check reuse, additional cache transports where justified, retention/garbage collection and measured concurrency                                |Reuse invalidation, trust, source/fixture identity, artifact availability and resource accounting cases pass        |
|O: Application care              |Version-specific upgrades, guided data migration, tested restores, database-aware plans, eligible progressive delivery and drift reconciliation                           |Application/version-path fixtures and failure/recovery cases pass                                                   |
|V: Vendor packages               |Verified package identity, customer-controlled installation/update policy, compatibility and recovery metadata                                                            |Publisher/customer authority, revocation, version pinning and compromised-update scenarios are defined and tested   |

Future model endpoints and manually enrolled cloud capacity remain valid smaller steps within their tracks. They do not receive active priority without a named current hosting dependency, and neither establishes managed GPU/model lifecycle or provider provisioning.

Additional proxy modes, raw Compose, provider/template breadth, multi-server application deployment, Swarm, GitHub Actions runner hosting, external secret/log integrations and requested Coolify API adapters remain visible compatibility entries. A generic shell job does not establish support for these workflows.

Self-hosted object storage, Wasm execution and a geographically distributed hosted edge remain compatible directions from the earlier hosting discussion. They are not prerequisites of the first task demonstration. Each needs explicit storage/runtime/region qualification and economics before being included in a supported offering. An ordinary VM image or uploaded Wasm module does not by itself establish a global edge product.

No delivery dates, staffing estimates or performance improvements are asserted. Order work by dependencies and demonstrated acceptance, then update the ledger with implementation evidence.

## 11. Documentation changes

Adopt this as the proposed revision of the product specification. Repository documentation should distinguish implemented behavior, planned workspace capabilities and the hosting compatibility target.

- README.md: lead with direct hosting and the repository/image-to-HTTPS-to-operation/recovery journey, while retaining the future workspace vision and explicit current support.
- docs/plan.md: order hosting H1–H4 first, preserve deferred D0–D3 and broader tracks, and require the hosting-dependency test before workspace work.
- docs/architecture.md: document task/run/workspace/environment lifecycles, stable machine identity, reservations, grants, build/artifact/verification/release/plan separation and independent workload traffic.
- Task and agent guides: describe submission, checkpoints, cancellation, budgets, human takeover, review, adapter resume limits and production authority.
- Machine guides: publish backend/OS/architecture qualification, trust profiles, model roles, availability policy, private ingress and cloud data-placement rules.
- Build and source guides: document all source/build paths, shared layer cache versus local cache mounts, whole-artifact reuse, verification reuse and explicit refresh controls.
- Hosting and operations guides: retain domains, certificates, variables, databases, storage, templates, logs, terminals, schedules, notifications, backups, restores and migration.
- Application support matrix: describe supported version transitions, recovery, migration and restore-test procedures per application/engine.
- Compatibility and capability ledgers: identify pinned references, tested versions, acceptance fixtures, partial support and deliberate differences.

The earlier revision 3 instruction to make Task collaboration and D0–D3 the first public demonstration is superseded by this delivery amendment. Direct hosting remains primary until core hosting replacement is complete. The richer durable Task stays future scope alongside all R69–R102/A38–A66 requirements. The earlier narrow prebuilt-only scope remains superseded.

## 12. Evidence and review limits

This status update starts from hosting-first documentation `65311f30470f34d0d26165d1cd13325fbd05fca0` and accepted application integration `a92d100c6ba654e6797432e7ef7a6cfd55527272`, including private runtime source `4642dc413cc21be955fd9639d12fdf0606735cfc` and UI source `513fe8d71b0f8297cceb3a26ab4e077c8eb49088`. The supplied historical `09d65d4ddcbfdf27e97158a9c08beb02a721818c` remains an ancestor, not the current assessment. The accepted native migration inventory extends through 0066, including GitHub push admission, server-readiness diagnostics and verified route switching. The current accepted application assessment is `f4a54506f388339ffe8acdd6b8ec4133690188fa`. Protected delivery, journal format 8 and opt-in runtime application are accepted within the limits below. Earlier planning inspection at `a763a90b3896257b784c9498d01650ccdad2427a` and revision 2 hosting evidence remain historical, revision-specific records.

The implementation campaign changed application code and exercised real infrastructure before this documentation adoption. Retained accepted evidence includes:

| Accepted work | Revision and evidence | What it establishes |
| --- | --- | --- |
| Same-host build artifact runtime | `61b77e75f2d95f3f015b0275a15ce2e376d82330`; [runtime guide](hosting-local-build-runtime.md), [real controller/local-deploy fixture](../internal/runnerruntime/local_build_deployment_integration_test.go) | Explicit local deployment capability, exact artifact/daemon/platform checks, ordinary HTTP and separately selected managed routing; no cross-host transfer |
| Artifact deployment WebUI | `08b9ff21f8def396d13f9464a197469e4d59adba`; [workflow and real browser gate](hosting-build-deployments.md#deploy-through-the-resource-ui) | Real API/PostgreSQL/River/compiled CLI build and deployment with authenticated UI, retries, viewer denial and responsive layouts |
| Typed output persistence and API | `28fc384bdb8fb641558cd6d20224d207c7095f77`; earlier migration replay check `1938a2fb5b41223d5b01303b88807d09b8933e4e`; [log contract](hosting-build-logs.md) | Full-authority receipts, canonical hashes, ordered allocation, retention watermarks and explicit old-history gaps |
| Log pagination boundary | `3ea2f815f844ff60008ecf22891bec5b370fb07f`; [API boundary tests](../internal/api/hosting_build_log_pagination_test.go) | Typed build log limit 200 reaches the endpoint while legacy pagination limits remain intact |
| Runner output delivery | Candidate `65dc15c1462485d84c9d6d524577f26a20048677`, accepted integration `5c0a1329c89f4a784a254aac6679591db3d6b0b0`; [delivery contract](hosting-build-log-delivery.md), [real controller gate](../acceptance/runtime-hosting-build-controller/gate.sh) | Actual Dockerfile/Railpack/static output, response-loss recovery, log delivery before completion, lease renewal, retention cursor, SIGTERM/cancellation and owned cleanup; journal format 7 |
| Build output WebUI | Candidate `07ce38d8230a84d5d1ef469bc8f604c5230afde4`, accepted `852f8e6b8d5624a95865c276dcea4a239d1f8fea`; [bounded viewer contract](hosting-build-logs.md#resource-build-output-viewer), [browser scenario](../web/app/tests/browser/local-build-deployments.spec.ts) | Opt-in typed build output reads with sequence/gap/retention handling, bounded local plaintext, access revalidation and responsive behavior |
| Private HTTPS Git control plane | Source `f89e019da9a2641da9dc36592fd54908e896b003`, accepted `edf8f14c8262c896e4c886088f8058878d77e965`; [grant/API integration tests](../internal/store/postgres_hosting_source_grant_test.go), [capability tests](../internal/store/postgres_hosting_private_capability_test.go), [credential client tests](../internal/runnerclient/hosting_source_credential_test.go) | Private HTTPS configuration, owner grant lifecycle, gated admission/dispatch and strict credential endpoint/client; at that checkpoint runtime/UI were absent; the accepted additions below supersede that limit; complete A03 remains partial |
| Private HTTPS Git runtime | Source `4642dc413cc21be955fd9639d12fdf0606735cfc`, accepted with UI at `4ba9fcac1b484244cb2c662e53f588c2dce6b71e`; [private runtime fixture](../internal/runnerruntime/private_git_runtime_integration_test.go), [operator setup](hosting-build-runtime.md#enable-private-https-git) | Explicit private capability/configuration; real CLI Dockerfile and Railpack, exact credential-response retry, redaction/cleanup, same-host artifact deployment and test-CA HTTPS; public regression 51.44s, private cases 76.59s |
| Private HTTPS Git WebUI | Source `513fe8d71b0f8297cceb3a26ab4e077c8eb49088`, accepted `4ba9fcac1b484244cb2c662e53f588c2dce6b71e`; [private browser test](../web/app/tests/browser/private-git-build-deployments.spec.ts), [workflow](hosting-git-resources.md#request-a-private-https-git-build) | Owner creates a new reference grant, recovers an identical retry, selects its ID, builds Dockerfile, reads typed output, deploys exact artifact and verifies test-CA HTTPS; keyboard, 320/390/430/1440px, viewer 403 and no reveal; browser 25.0s/Go 34.03s. Public browser regression 22.2s/Go 30.63s; automatic-builder private UI was absent at that checkpoint and bounded Railpack qualification is accepted below; complete A03 remains partial |
| Private Railpack static-provider UI qualification | Accepted `a92d100c6ba654e6797432e7ef7a6cfd55527272`; [runtime limits](hosting-build-runtime.md#enable-private-https-git), [fixture contract](../acceptance/runtime-hosting-local-build/README.md) | Existing UI creation/editing, exact-version grant and lost-response retry, compiled CLI build, exact artifact deployment, test-CA HTTPS, genuine failed rebuild preserving old HTTPS bytes, viewer denial and 320/390/430/1440px layouts; qualification changes tests/docs only; no arbitrary language matrix, Nixpacks, compiled frontend, public production ACME, complete A03 or parity |
| Desired runtime configuration | Source `5b435d4e8e35562ab6c9a7116a6b0b5f16e85751`, accepted `bdd5e0168655edd31df48a7818a560a49876147e`; [service](../internal/app/hosting_runtime_config_service.go), [API tests](../internal/api/hosting_runtime_config_handlers_test.go) | Migration 0052; `GET`/`PUT /api/v1/resources/{id}/runtime-configuration` for application image/Git resources; generation compare-and-swap and idempotency; explicit public nonsecret literals and exact managed-secret version references; current owner writes/viewer reads; no decryption, injection or UI at that checkpoint; accepted execution/UI are below |
| Runtime-configuration admission | Source `7c51350083f6fa448e8043e32158ecf9427be6d6`, accepted `1ddfb1f43416ff8109c31a852963942448035583`; [admission persistence](../internal/store/postgres_hosting_runtime_environment.go) | Migration 0053; prepares exact configuration authority for deployment; does not apply values to containers |
| Protected runtime-environment delivery and journal | Delivery source `c7d3a0d5a4fdb00bf9b2ab5e6bd0cdfd05e6b30e`, journal source `804d4f460c6f106993c0a223f36eb42831f6a7f2`, accepted `66b58845dab1fdee6f8b12809f0434a2a94326ac`; [delivery service](../internal/app/hosting_runtime_environment_delivery.go), [endpoint tests](../internal/api/hosting_runtime_environment_handlers_test.go) | `POST /api/v1/runners/hosting/runtime-environment` checks exact prepared authority and current actor/configuration/secret versions, commits metadata audit before decryption and preserves the original bounded window; journal format 8 persists only nonsecret references and delivery ID; older formats remain legacy; runtime consumption was absent at that checkpoint; accepted execution is below |
| Protected runtime execution | Runtime source `420300352ba14f40f082c519f6a75d04f82129c1` and credential repair `b9836b8a626fc12c5a6e4dde44ebc40e80b870a2`, accepted `0a50debdfa5ab14191e52025cd1bb62684e86cd6`; [qualification guide](../acceptance/runtime-hosting-environment/README.md), [gate](../acceptance/runtime-hosting-environment/gate.sh) | Explicit CLI/API opt-in; literals and exact managed-secret versions reach Docker through protected stdin; fixed delivery/replay windows and deadlines; after authorization commits and before returning environment bytes, the endpoint rechecks the original credential and rejects delivery if it detects rotation; it cannot recall bytes already sent or values already delivered to a runner or container; Docker administrators can inspect environment values |
| Bounded runtime status control | Source `56baaa7ec5f155ca490fa7cc989833ed92de9189`, accepted `b55bbcf7b2313e32391a4390dcbb8562de2617b5`; [store](../internal/store/postgres_hosting_runtime_observation.go) | Migration 0054; bounded challenge/report and viewer freshness controls; collector was absent at that checkpoint and is accepted below; status UI was absent at that checkpoint and is accepted below; no disk/CPU metrics |
| Runtime configuration WebUI | Source `558d365571630e2a701aef3f60c2e9710771ba54`, accepted `46ceb43f77085e9d11c5c6375b79d6cbfc8e1485`; [browser scenario](../web/app/tests/browser/runtime-environment-deployments.spec.ts) | Public literals, exact managed-secret references, generation compare-and-swap, idempotent retry and fresh deployment generation; authenticated cookie API plus compiled runner/Docker; failed-readiness preservation, viewer authorization and 320/390/430/1440px layouts; seeded accounts/resource/placement and trusted persisted Git artifact; no fresh Git build, onboarding or public ACME qualification |
| Independent runtime collector | Source `bd9638c227db67e10d1ee9647b40c60c5676f0a5`, accepted `132012237ee484e9cd6fce2fba656966567a1cc8`; [operator contract](hosting-runtime-observation.md), [real gate](../acceptance/runtime-hosting-observation/gate.sh) | Opt-in `--hosting-observation` with matching persisted `hosting_observation_v1` capability; read-only narrow Docker status and loopback readiness; fair selection of four targets per 10-second cycle, not a 10-second per-resource guarantee; no metrics; status UI was absent at that checkpoint and is accepted below |
| Runtime-status dashboard | Source `e5bc0b6aa85bae5ef409ebf3e8e6758fe30f45f6`, accepted `24c363fd22ac8237a09c12040386a442b0a3ff53`; [status UI](../web/app/src/pages/ResourceRuntimeStatus.tsx), [read contract](hosting-runtime-observation.md#resource-status-in-the-webui) | Bounded read-only current/stale/historical target evidence under current viewer authorization; timeout and retry handling; real compiled collector/browser proof uses seeded accounts/resources/deployment; no general metrics, complete onboarding, public ACME or complete A28 |
| Enrollment command capability repair | Accepted `1c4e7076cde2c79d2aa3d46eb4de2133d72e8a3e`; [command helper](../web/app/src/lib/runner-enrollment-command.ts) | Command uses exact persisted capabilities and exposes configuration prerequisites; does not qualify SSH-assisted enrollment or complete host readiness |
| Runtime-log control | Decoder repair `0b450dd55351aa5884b9fe83eb7bb3c0e96084ff`, accepted `aa88f62e76a4ef964900db17bf81515e141da03b`; [API](../internal/api/hosting_runtime_logs_handlers.go), [PostgreSQL tests](../internal/store/postgres_hosting_runtime_logs_test.go) | Migration 0055 and 173-route contract; actor sessions/renewal/stop/bounded retained reads; exact runner claims/events, counters, replay gaps and purge; live collector/viewer were absent at that checkpoint and are accepted below |
| Live runtime logs and embedded viewer | Accepted `3a2b524ca622a7ebfba761c590803d110192cb9f`; [collector contract](hosting-runtime-logs.md), [viewer contract](hosting-runtime-log-viewer.md), [real gate](../acceptance/runtime-hosting-logs/gate.sh) | Bounded R59/A29 evidence: registered settled deployment, fresh Docker stdout/stderr, compiled collector, API and embedded viewer; explicit Follow/30-second demand, retention/source gaps and revocation; `--hosting-runtime-logs` must match persisted `hosting_runtime_logs_v1`; trusted Unix Docker with json-file/local drivers; additional TTY proof has synthetic provenance; raw application output can be sensitive, known bearer redaction is bounded and delivery is not exactly once |
| Exact trigger-revision prerequisite | Source `21349d3e2492dca8b8b3ca3d99132085889360e8`, accepted `0a9422194e8bff7485e7fb20c02f74418ea2ce51`; [contract and qualification](hosting-build-trigger-revision.md) | Trusted internal caller freezes commit before checkout; runner revision capability enforced; revision-bound idempotency; public/private HTTPS checkout preserves selected commit after branch movement and rejects unavailable commits without fallback; provider authentication/controller were absent at this checkpoint; the accepted GitHub integration below supersedes that absence; webhook registration remains manual |
| Server-readiness diagnostic backend | Accepted `3cb62858e3d1e263d89d5e3c6aecd0a30792e938`; [diagnostic contract](server-readiness.md), [PostgreSQL tests](../internal/store/postgres_server_readiness_test.go) | Migration 0057; bounded runner challenge/report and administrator freshness/authority reads for `basic_image_hosting.v1`; diagnostic API only, no deployment admission or complete A01; agent and UI were absent at this backend checkpoint; accepted manual enrollment qualification is recorded below |
| Manual enrollment and readiness | Accepted `0771f1b56befcd30b195c45121dba4d3efeec974`; [qualification contract](server-readiness.md#runtime-verification-fixture), [real gate](../scripts/ci/server-readiness-runtime-gate.sh) | UI enrollment and generated command consumed by compiled native Linux arm64 agent over fixture-CA-verified HTTPS; ready/blocked/stale recovery, viewer denial, administrator role loss and persisted-credential restart; CLI metadata only, Docker API 1.54 and 1 GiB per work/journal root; amd64 compile-only, no runtime qualification; no SSH, production TLS, application deployment or complete A01 |
| Direct hosting navigation | Source `32ab3bcd259781c565e2a052dfbba320acd9206e`, accepted `43e9c305f71a267b1d201f7849c12a31f8c532d6`; [navigation metadata](../web/app/src/router/metadata.ts), [Home guidance](../web/app/src/pages/HomeView.tsx), [browser scenarios](../web/app/tests/browser/hosting-navigation.spec.ts) | Home, Projects and Servers and runners lead the Hosting group; advanced workflows remain available; direct image/Git application guidance requires no Task; saving configuration, heartbeat and a saved route do not establish deployment, readiness or HTTPS |
| Verified existing-route switch engine | Accepted `99250c2634109cdd2008c56dae1b1740d377c0a1`; [engine contract](hosting-route-switch.md), [runtime fixture](../internal/hostingproxy/runtime_test.go) | Opt-in existing-route port switches verify old/new HTTP and trusted TLS, preserve unrelated routes and verify rollback; cancellation or unverified rollback stays `Uncertain`; controller/API admission and agent capability negotiation were absent at this engine checkpoint; accepted controller evidence is below; automatic deployment was absent at this engine checkpoint; accepted bounded automation is recorded below; no production ACME or complete A11/A20 |
| Guarded verified-route controller | Accepted `5687b88465839b8e341181ba8b47f54dc17f006c`; [request, authority and recovery contract](hosting-route-switch.md), [compiled runner fixture](../internal/api/hosting_route_switch_runtime_integration_test.go) | Migration 0058; resource POST selects exact healthy or retained-running releases for existing routes; explicit agent capability/opt-in, verified HTTP/trusted TLS, prior-generation rollback, lost-response idempotency and cancellation/replay holds; current authority, expiry and revocation checks; metadata conflicts remained held without reconciliation at this controller checkpoint; bounded R21/R25/R28 and A11/A20 evidence, not full R21, draining or automatic data rollback |
| Route-switch reads and operator UI | Read API accepted `fa93d16fa09d5a7929581d5acb206029be9752d0`; UI source `1a555c514a23241bca34224380008dc42edcce49`, accepted `1c6ddb414d984372bb1f081fb6ff23db8bd81f88`; [operator contract](hosting-route-switch.md) | Resource-scoped authoritative options, explicit review, frozen retry intent, pending-operation recovery and visible holds; compiled controller 33.98 seconds/browser 19.6 seconds plus 111 normal browser cases; native Linux arm64 and local test CA; unresolved metadata conflicts remain held |
| Completed metadata-conflict recovery API | Source `93305f8c528b8a3990ec79178584f4e170bada02`, accepted `e097ffa706325f55cab7a7b0416a235a0e9ad151`; [administrator recovery contract](hosting-proxy-recovery.md) | Migration 0059; current server administrator/control-project owner reviews eligible completed rolled-back/refused conflicts; 30-second plans and idempotent apply preserve original receipts, disabled/revoked state and release protection, settle only the matching hold and queue authorized convergence; concurrent retries return one resolution; uncertain/unfinished outcomes and generic unlock remain unsupported; administrator UI was absent at this API checkpoint and is accepted below |
| Completed metadata-recovery administrator UI | Source `0e84be388bf0132e58bd9ef67b8e083a354626cb`, accepted `d238d23e63ea2ae7f1bb0f7ed843f7b71863660f`; [UI contract and local qualification](hosting-proxy-recovery.md#local-ui-qualification), [browser fixture](../web/app/scripts/hosting-proxy-recovery-fixture.sh) | Explicit immutable 30-second plan review and confirmation, exact retry, new review after 409, current authorization, scoped operation locator, new current GET and denial masking; recorded settlement and queued convergence remain distinct from convergence completion; no general unlock, grant restoration or data restore |
| Trusted automatic GitHub deployment | Accepted `86825ab17f2e2bb0b2b0a26e6e65e2e2769d8fa3`; [policy and recovery contract](hosting-auto-deploy.md), [real gate](../acceptance/runtime-hosting-auto-deploy/README.md) | Optional `automatic_unprotected` after an initial healthy manual release and existing routes; verified signed fixed-branch push builds the exact source, deploys on a fresh port and switches existing trusted HTTPS routes; duplicate/delayed/superseded work, failed build/readiness preservation, unrelated routes, response loss and restart covered; live GitHub, public ACME, protected approval, remote builders and previews remain unsupported |
| SSH prerequisite installer and durable control | Installer `b6cd8962aba3894d6ee86cf0ec34530d9b319ccd`; control source `342fb8beb4da5a711bd25716888627e4dfb587f2`, integrated `5bf42af0174caf27a6c0d758242a0e3cf48b4851`; [installer contract](hosting-ssh-bootstrap.md), [durable control contract](hosting-ssh-onboarding.md) | Native arm64 Debian 12/systemd, Docker API 1.54 and Compose 5.1.1 fixture; baseline probe/installer exposes only four capabilities, not build/proxy/log capabilities; full SSH API/UI was absent at this installer checkpoint; bounded workflow acceptance is recorded below; no complete A01 |
| SSH onboarding API and UI | Source `57093e14d6b65e12305fd44aa61a3dd7b689545b`, accepted `b4951282aefb3ad63d525c9d10037d7120f1d4ba`; [workflow and authority contract](hosting-ssh-onboarding.md), [prepared-host installer limits](hosting-ssh-bootstrap.md) | Real API/River/SSH and browser workflow on prepared Debian 12 native arm64/systemd, Docker API 1.54, Compose 5.1.1 and 1 GiB minimum; operator artifact metadata and pinned SSH identity; response-loss recovery retains installation identity and uses owned inspection; four-capability image-hosting profile only, including `hosting_image_v1`, with no source-build/proxy/log/storage preset; not public ACME, live-host breadth, full OS qualification or complete A01 |
| Owned-storage foundations | Metadata `13f8e6f952003dc1c36ba467ac4e19817ded6f0c`; private helper `753a027dabc1fb26176bceb9e91579b649ece8a2`; materialization controller `8e00631aac26e0787a43398332e31a35980f067d`; [metadata service](../internal/app/hosting_storage_service.go), [private volume helper](../internal/runnerruntime/hosting_owned_volume.go), [durable admission tests](../internal/store/postgres_hosting_storage_materialization_test.go) | Migrations 0062–0063; accepted desired metadata, private owned-volume helper and durable materialization admission only; storage HTTP, coordinator/capability and mounted-upload workflow were absent at this foundation checkpoint; accepted runtime and recovery evidence is below; storage metadata API/UI is accepted separately below; deployment-review/recovery UI was absent at this foundation checkpoint; bounded UI acceptance is recorded below; PostgreSQL/Redis, complete A19/A26 and parity remain planned |
| Storage metadata API and UI | Accepted `2f8beb9b555e651bcccb47d3c0dbc7ccfdb268db`; [metadata HTTP tests](../internal/api/hosting_storage_handlers_test.go), [metadata UI](../web/app/src/pages/ResourceStorage.tsx), [browser scenarios](../web/app/tests/browser/hosting-storage.spec.ts) | Authorized metadata creation/selection and saved mount intent; saving metadata does not materialize a volume, mount an application or prove persistence; storage deployment-review/recovery UI was absent at this metadata checkpoint and is accepted below |
| Uploaded-file storage runtime | Source `b33466c81899e2a7278b2e551fb2532ae17b46bc`; [physical qualification contract](../acceptance/runtime-hosting-storage/README.md), [API/compiled-runner fixture](../internal/api/hosting_storage_runtime_integration_test.go) | Accepted through current assessment; migration 0064 continuity, public deployment options/status, typed materialization acknowledgment and explicit `hosting_owned_storage_v1` opt-in; uploaded bytes survive A-to-B replacement, lost ACK/restart and failed-readiness restoration with the same volume, single writer and unrelated HTTPS route preserved; this runtime fixture does not qualify deployment-review/recovery UI, backups/restore, unknown-writer recovery, public ACME or complete A19/A26 |
| Bounded storage-continuity recovery API | Accepted `284f45cf1659a559d55d54342a902d0eeb112685`; [public handler](../internal/api/hosting_storage_route_recovery_handlers.go), [PostgreSQL recovery tests](../internal/store/postgres_hosting_storage_route_recovery_test.go) | Recovery only for eligible unqueued continuity; current authorization, immutable identity, drift rejection and duplicate/concurrent replay checked; queues authorized continuity work without claiming proxy convergence or generic unlock; no actual proxy-recovery fixture; later UI evidence does not add physical proxy-recovery proof |
| Connected storage deployment-review/recovery UI | Accepted `77be63a03c9caf3e95239a751045f0fe7b5918cf`; [deployment review](../web/app/src/pages/StorageDeploymentReview.tsx), [deployment page](../web/app/src/pages/ResourceDeployments.tsx), [browser scenarios](../web/app/tests/browser/hosting-storage-deployment.spec.ts) | Authoritative options and connected review/retry/continuity controls accepted; browser uses real authentication/resource setup with intercepted deployment/options/status responses, so it proves UX rather than a new mounted workflow or physical proxy recovery; earlier physical upload proof retains its original revision |
| Private database foundations | Creation/domain `d2dd99af9a88f64b68a9ad25c42a23a7826d20db`; metadata UI `bed700ed0bb8f006c10cd355b98eab1df9c8b5e0`, source `e143e45dde772a881ec75e0602985c84df06a92e`; controller `b93eef4a5b633f7639adc2bee78b7c39bbf8af83`; engine helpers `ca818c6ec94c0bdea661373566e28f46cf5ef229`; protected delivery `33a7506f8b5f92f88f455e3cc4029629dfb6c7a9`; [metadata UI](../web/app/src/pages/ResourceDatabase.tsx), [engine tests](../internal/runnerruntime/hosting_database_engine_test.go), [delivery API tests](../internal/api/hosting_database_delivery_handlers_test.go) | Migrations 0065–0066 and 211-route contract; accepted metadata/creation, private controller/helpers and protected credential API/protocol/client only; engine and delivery were not runner-wired at this checkpoint; later direct-engine qualification and default-off coordinator are recorded below; no database deployment button, application binding, combined API/runner physical pass or complete A17 |
| Direct private database engine qualification | Accepted `b9c4c93ef55d946ca67384599d4c6d6290e36d0e`; [physical fixture contract](../acceptance/runtime-hosting-database/README.md) | Six native Linux arm64 engine cases, PostgreSQL 17.11/Redis 8.2.10, passed in 30.52 seconds; A/B/C data continuity on the same volume/private bridge, changed credentials and revoked exact replay denied, lost retirement ACK recovered, missing/foreign-volume negatives; direct engine only, not API/runner workflow, archive restore, AMD64 or full A17 |
| Default-off database runner coordinator | Source `9040572132e60787186fd59ffddc57d765d6c752`, accepted `f4a54506f388339ffe8acdd6b8ec4133690188fa`; [typed coordinator](../internal/runnerruntime/hosting_database_execution.go), [replay tests](../internal/runnerruntime/hosting_database_replay_test.go), [CLI configuration tests](../cmd/nerocd/runner_database_config_test.go) | Journal v11, protected delivery before ApplyBegin, current-authority callback, native Linux arm64 and verified HTTPS for database runner-to-control-plane requests; capability disabled by default; accepted code and race/build evidence, no accepted current API/runner physical workflow or deployment UI |
| Compiled Vite SPA qualification | Source `9b49eb6dac7e3d22b9db81d984ce2da592d27c66`, accepted `d29edb3bc8cde064108891f70713423bb57776a3`; [fixture contract](../acceptance/runtime-hosting-local-build/README.md) | Exact pinned source compiles with Node 22.22.0, Bun 1.3.6, Vite 8.2.2 and Caddy 2.10.2; runtime 141.87 seconds/default regression 42.72 seconds; genuine parser failure preserves the prior serving application; HTTP browser bridge and independent TLS probe are separate evidence; earlier unexplained failures remain a reliability limit; not complete A04 or production ACME |

Private Railpack qualification is now accepted at
`a92d100c6ba654e6797432e7ef7a6cfd55527272`, tree
`279db979551419a4f77bf1bad4973c76ab20d2bc`. The retained browser pass at
`893d3b92ade54359a397e6c585584afe6f56cbeb` took 82.04 seconds; its inputs are
unchanged. Fresh default combined checks at the accepted revision passed public
44.63 seconds, private Dockerfile 14.51 seconds and private Railpack 56.45 seconds,
plus fixture policy checks. Isolated private checks also passed in 74.87 seconds.
The earlier three-case combined failures at `893d3b92ade54359a397e6c585584afe6f56cbeb`
remain historical failures of unknown cause. Later passes do not waive or relabel
them. This qualifies the existing Railpack static provider on trusted Linux/Docker
with seeded accounts, existing secret metadata and a local test CA. It adds no
production UI and does not establish arbitrary language providers, Nixpacks,
compiled frontend builds, public production ACME, complete A03 or hosting parity.
Direct-engine evidence in `/tmp/nerocd-database-qualified-physical-handoff.json`
belongs to `b9c4c93ef55d946ca67384599d4c6d6290e36d0e`: six cases passed in
30.52 seconds on native Linux arm64. The coordinator handoff
`/tmp/nerocd-database-runner-integration-handoff.json` records full combined
runner/client/runtime/CLI race checks in 81.189 seconds, compile, vet and Linux
arm64 build at `f4a54506f388339ffe8acdd6b8ec4133690188fa`. Eight engine files
remain identical to the qualified engine revision, but the shared volume helper
changed; the earlier physical pass is not qualification of the final combined
tree. The current API workflow fixture remains unaccepted and unrun. No database
deployment UI, application binding, archive backup/restore, major upgrade, public
database, AMD64 qualification, complete A17 or full parity is claimed.

Current storage UI evidence at `77be63a03c9caf3e95239a751045f0fe7b5918cf`
records 179 frontend tests, responsive/keyboard browser checks, compiled Go and
207-route contract/74-served-asset checks in
`/tmp/nerocd-storage-complete-ui-handoff.json` and
`/tmp/nerocd-main-storage-complete-ui-promotion.json`. Intercepted operation
responses qualify review/retry UX only. Continuity recovery at
`284f45cf1659a559d55d54342a902d0eeb112685` retains its bounded metadata/API proof;
there is no physical proxy-recovery case.

Protected database delivery integration
`33a7506f8b5f92f88f455e3cc4029629dfb6c7a9` records actual PostgreSQL delivery/
controller/storage race checks, focused races, vet, SQL placement, Go compile,
211-route contract, generated-client parity and 74 served assets in
`/tmp/nerocd-database-delivery-integration-handoff.json` and
`/tmp/nerocd-main-database-delivery-promotion.json`. Engine helpers and delivery
were unwired at this credential-delivery checkpoint. Later direct-engine and
default-off coordinator evidence is recorded below. Database UI remains metadata
only; no combined API/runner pass, application binding, complete A17, backup/archive
restore or production ACME is claimed.

Physical storage evidence at `b33466c81899e2a7278b2e551fb2532ae17b46bc`
passed in 17.06 seconds. The retained summary
`/tmp/nerocd-storage-runtime-20261024.xH772x/qualification-summary.json` records
65,537 uploaded bytes with SHA256
`7d0f6225a3b0afb197160548a0de3516364c175b3ce66942f0f322e7e3899e49`.
Authenticated API/River and the compiled runner preserve those bytes through
replacement, committed acknowledgment loss/restart and failed-readiness recovery.
It checks exact volume identity, a single running writer and an unrelated trusted
HTTPS route under local Pebble CA trust. This is physical API/runtime proof;
later accepted deployment-review/recovery UI uses intercepted operation responses
and proves UX separately; it adds no new physical runtime proof.

Recovery integration `284f45cf1659a559d55d54342a902d0eeb112685` retains unchanged
production source from `ec9b6e0e584b6c4c7b6348ddc17ded427be2956d` and test-only
repair `2848822ac9c944face898760076b06d028f6f0ac`, with all three reviews passing.
Source PostgreSQL recovery checks passed in 4.377 seconds; fresh combined
domain/application/API checks passed with a maximum package time of 8.014 seconds.
These are contract/database tests, not an actual proxy-recovery runtime fixture.
Storage deployment-review/recovery UI is accepted with UX-only limits below.
Direct database engine qualification and default-off runner coordination are
accepted below; actual API/runner workflow qualification remains unaccepted. Backups,
restore, unknown-writer handling, production ACME, complete A19/A26 and full
hosting parity remain unfinished.

Storage foundation evidence retains its original boundaries. Metadata promotion
`13f8e6f952003dc1c36ba467ac4e19817ded6f0c` is recorded in
`/tmp/nerocd-main-storage-repaired-promotion.json`. Private helper promotion
`753a027dabc1fb26176bceb9e91579b649ece8a2` retains helper race 65.888 seconds and
Docker fixture 0.80 seconds in `/tmp/nerocd-main-volume-helper-promotion.json`;
the helper is not a mounted application workflow. Materialization integration
`8e00631aac26e0787a43398332e31a35980f067d` records actual PostgreSQL race
19.772 seconds, domain race 1.507 seconds, focused runtime race 1.373 seconds,
vet, SQL placement, generated SQLc identity and Go compile in
`/tmp/nerocd-materialization-integration-handoff.json` and
`/tmp/nerocd-main-materialization-promotion.json`. Its runtime race skipped Docker;
helper Docker evidence remains retained at the helper revision. No new mounted
runtime, UI or application data-persistence qualification is inferred.

SSH onboarding source `57093e14d6b65e12305fd44aa61a3dd7b689545b` is accepted
at `b4951282aefb3ad63d525c9d10037d7120f1d4ba`. Actual API/River/SSH evidence at
`10f830f71e88df544864b56ffcd8be9bdb222fa5` passed in 67.94 seconds with browser
5.3 seconds, retained under
`/Users/bubble/Documents/Dev/NeroCD-hosting-ssh-onboarding-runtime-20261023/.cache/nerocd-server-bootstrap.ojx62tze`.
This includes response loss, retained installation identity and inspection recovery.
Fresh accepted integration checks record 1,144 frontend tests in 90 files,
200-route contract and 74 served assets matching the 75-file manifest
`c3e352055c383ab544dd9b15ed2770d42709f3e758580d42ee004e004a9faf69`.
Exact source retention and limits are recorded in
`/tmp/nerocd-ssh-final-integration-handoff.json` and
`/tmp/nerocd-main-ssh-final-promotion.json`. These do not claim a new runtime run
at main. The qualified current artifact was built and served in the integration
checkout; root ignored assets remain historical. Storage metadata, the private helper and durable admission are accepted below;
the bounded API/compiled-runner mounted workflow is accepted below; accepted storage deployment-review/recovery UI is separate UX evidence; database
direct engine qualification and default-off coordination are accepted below;
actual API/runner workflow qualification remains unaccepted.

Automatic deployment evidence is source-bound: runtime
`3510437292cb7195a7cda9760b7eb7bf468d7877` passed in 66.85 seconds at
`/tmp/nerocd-auto-deploy-runtime.4Yekz5`; browser source
`b9e34d7cd0e4aa6a7531b3150ad75eba6caf4282` passed in 22.5 seconds with Go runtime
41.61 seconds at `/tmp/nerocd-auto-deploy-browser.Iwkcfa/gate.log`. The later HTTP
constant and test-only repairs were reviewed with unchanged qualification inputs.
Fresh accepted-main evidence records 1,123 frontend tests in 88 files, supply-chain policy
passed for 29 components, full Go build, 189-route contract and 74 served assets matched byte-for-byte. Exact revision,
tree and retained evidence are in `/tmp/nerocd-auto-ssh-integration-repaired-handoff.json`
and `/tmp/nerocd-main-auto-ssh-promotion.json`. The qualified current binary and
dist remain in the integration checkout; root ignored dist is historical and is
not a newly installed automatic-deployment artifact.

SSH installer runtime evidence is the owned fixture
`.cache/nerocd-server-bootstrap.pbeehlaf` at the installer revision above. The
installer and durable control evidence is distinct from the later accepted SSH
API/UI workflow below. After bounded automatic deployment and SSH onboarding,
H2 stateful hosting is next.
Protected plans, production TLS and remaining hosting/migration compatibility
retain their separate unfinished requirements. Workspace scope stays deferred.

Recovery UI source `0e84be388bf0132e58bd9ef67b8e083a354626cb` passed all three
independent reviews before accepted integration
`d238d23e63ea2ae7f1bb0f7ed843f7b71863660f`. Retained combined evidence in
`/tmp/nerocd-recovery-ui-combined-*` records 1,072 frontend tests in 87 files,
72 focused tests, actual controller 26.25 seconds and nine browser cases with
Go runtime 92.73 seconds. The separate 110-case operator run used `--no-deps`
and omitted OIDC setup; it does not qualify OIDC. Fresh combined and accepted-main
checks record the 189-route Go contract and 74 served assets, including
`/tmp/nerocd-main-recovery-ui-contract.log` and
`/tmp/nerocd-main-recovery-ui-served.log`. Runtime qualification is native Linux
arm64 with local fixture CA trust, not production TLS or convergence completion.

React Doctor 0.9.18 evidence at `baf2c3a3326dea01ce7ad9142c36daaab81102d8`
records 122 diagnostics reduced to 107: 118 warnings and four errors became
103 warnings and the same four retained errors. The remaining findings include
23 selected-rule diagnostics and 84 others. This revision-specific result does
not remove all selected findings; the newer recovery integration tree was not scanned.
The [secret-state change and tests](../web/app/src/pages/ProjectSecrets.test.tsx)
and `/tmp/nerocd-doctor-top-three-evidence/dispositions.json` identify the source
and retained dispositions. These Doctor counts remain historical, not a scan of
current accepted main. Bounded automatic deployment and SSH installer evidence
are recorded below; bounded SSH API/UI acceptance is recorded separately.

Bounded completed metadata-conflict recovery is accepted at
`e097ffa706325f55cab7a7b0416a235a0e9ad151`, including migration 0059. Source
`93305f8c528b8a3990ec79178584f4e170bada02` passed all three independent reviews.
The earlier concurrent-replay specification failure remains historical; the
accepted repair returns the same durable resolution after serialization retries.
Actual compiled-runner recovery passed in 24.89 seconds in
`/tmp/nerocd-metadata-replay-final-runtime.log`; PostgreSQL recovery race checks
passed in 45.390 seconds in `/tmp/nerocd-metadata-replay-final-race.log`.
The historical SQL-policy failure at `fa93d16fa09d5a7929581d5acb206029be9752d0`
was repaired through SQLc query extraction. Combined and accepted-main SQL
architecture checks passed in `/tmp/nerocd-recovery-combined-sql-policy.log` and
`/tmp/nerocd-main-recovery-sql-policy.log`. Main's 189-route contract and 74 served
assets passed in `/tmp/nerocd-main-recovery-contract.log` and
`/tmp/nerocd-main-recovery-served.log`. No fresh whole-suite or production TLS
result is claimed. Bounded R17 automatic deployment is recorded below; protected
plans, broader SSH/host qualification, production TLS, stateful hosting and remaining parity stay open.

Controller qualification at `b1e42f53d06af36c086d5a8160dba4c17bced8c9`
has tree `f0d646b8f85f32e82646189d59dc81fe1ac0b61f`, identical to accepted
`5687b88465839b8e341181ba8b47f54dc17f006c`. Retained route runtime passed in
23.92 seconds in `/tmp/nerocd-route-integration-route-runtime.log`; readiness
native 61.19 seconds and browser 70.66 seconds passed in
`/tmp/nerocd-route-integration-readiness-browser-runtime.log`, with owned cleanup
verified. The first Bash 3.2 empty-array invocation failed; the corrected run
passed. Whole-repository lint retained three unchanged errors; the required lint
delta was zero and passed. These results do not claim a clean whole lint gate.
Main's 184-route contract and 74-served-asset checks passed in
`/tmp/nerocd-main-route-control-contract.log` and
`/tmp/nerocd-main-route-control-served.log`, retaining manifest
`0e81691a8d4fcb7f1b59facf37ce1d9dc84cf601e87b42f24532778e827b6cd6`.
The bounded route-switch read API and operator UI are now accepted below. R17 automatic/protected
plans, SSH, production ACME and stateful hosting remain incomplete.

Navigation evidence at source `32ab3bcd259781c565e2a052dfbba320acd9206e`
records 965 Vitest tests and 120 compiled-Go browser cases in 6.3 minutes in
`/tmp/nerocd-hosting-navigation-browser-final-20261023.log`, with three independent
reviews passing. Accepted main `43e9c305f71a267b1d201f7849c12a31f8c532d6`
passed fresh 183-route contract and 74-served-asset checks, with manifest
`0e81691a8d4fcb7f1b59facf37ce1d9dc84cf601e87b42f24532778e827b6cd6`
and binary `c3c95c79862e0ca396ee0875ca2fb5d5152ad7490b36df5ea74087e2bf4a6e3a`.
Synthetic fault and actor cases prove client behavior only; real OIDC denial
evidence remains separate. These retained results do not qualify complete route-switch recovery, automatic deployment, protected plans, SSH,
production ACME or complete hosting parity.

Manual enrollment qualification at `68c403aa6d319dd9a391d6f6eb577336943e355b`
passed the native agent case in 61.24 seconds and the actual enrollment/browser
case in 69.64 seconds, with exit 0 in
`/tmp/nerocd-readiness-qualification-final.log`. Accepted integration
`0771f1b56befcd30b195c45121dba4d3efeec974` preserves 31 reviewed blobs and records
106 focused frontend tests, the 183-route contract and a 75-file manifest with
74 served assets. These are retained acceptance results, not fresh application
checks for this documentation candidate. The profile checks safe CLI metadata;
it does not execute the CLI or prove deployment. Runtime evidence is native
Linux arm64; amd64 has compile evidence only. SSH, production TLS, application
deployment and complete A01 remain open.

GitHub build-on-push is accepted at
`be85bf4c4d42c0b2ddb9e9935f2fa74332656bde`. The [operator contract](hosting-github-push.md),
[real gate](../acceptance/runtime-hosting-github-push/gate.sh) and
[browser scenario](../web/app/tests/browser/hosting-github-push-builds.spec.ts)
cover signed GitHub.com pushes for one saved branch, independent current-head
verification, durable commit deduplication, ignored delayed older deliveries,
owner policy/recovery and viewer history. Deployment is manual in the default build-only policy. A genuine
failed build preserves the previously serving artifact, checked by HTTP bytes.

The exact-candidate local gate passed with browser 23.7 seconds and compiled
runner/API runtime 25.21 seconds. It uses a simulated GitHub TLS provider with
registered API routes, PostgreSQL/River, real checkout and compiled Go CLI Docker
builds. Evidence is `/tmp/nerocd-push-bootstrap-v3-full-browser.log` and
`/tmp/nerocd-push-bootstrap-v3-cleanup.log`. Earlier failed gate attempts remain
historical failures; the final candidate passed. No fresh full application suite
is claimed on this candidate.

This qualifies bounded R09/R10/R13/R57 and A09 behavior. It does not qualify live
GitHub webhook configuration, optional automatic deployment, PR previews, other providers,
remote image transfer, production ACME, complete A09 or full hosting parity.

The desired-configuration foundation passed actual PostgreSQL and focused race
checks, 718 frontend tests, the 163-route contract, generated-output checks and
build checks at accepted integration `bdd5e0168655edd31df48a7818a560a49876147e`.
These retained results do not establish all-feature test coverage.
The Docker protected-environment helper source
`8f33c86126f3eb52492490b3e7e3a11962a27e65`, integrated at
`8e6c9c28a91f6c76981b683f444886d9ed7a193b`, was unwired at that checkpoint.
The accepted runtime integration now wires it.
Migration 0053 admission and protected delivery are accepted at the revisions above.
The accepted runtime integration applies desired values through explicit CLI/API
opt-in. The combined fixture used a trusted producer's persisted Git artifact and
a registry image with test-CA TLS. It verified readiness-failure preservation of
the original container, pinned version 1 after rotation, denial after revocation
or new configuration, and no apply on startup. It did not qualify a new Git build,
complete A03/A28, production ACME or full hosting parity. Log scans covered only
controlled output; Docker administrators can inspect environment values.
The configuration UI and independent collector are now accepted at the revisions
above. The bounded runtime-status dashboard and enrollment command repair are now
accepted at the revisions above. Runtime-log control and migration 0055 are now
accepted at `aa88f62e76a4ef964900db17bf81515e141da03b`. The live collector,
embedded viewer and manual enrollment log profile are now accepted at
`3a2b524ca622a7ebfba761c590803d110192cb9f`. Independent runtime status remains
accepted. The new path provides bounded R59/A29 coverage, not full Coolify parity.
Exact trigger-revision source `21349d3e2492dca8b8b3ca3d99132085889360e8` is now
accepted at `0a9422194e8bff7485e7fb20c02f74418ea2ce51`. This is a trusted internal
build prerequisite. Provider event authentication and push ordering are supplied
by the accepted GitHub integration above; webhook registration remains manual.

Accepted roll-forward evidence records the real 86.96-second combined log gate
at `3a2b524ca622a7ebfba761c590803d110192cb9f`, including registered settled
deployment, fresh Docker stdout/stderr, compiled collector, API and embedded
viewer. The fixture uses seeded accounts, placement and image setup, verified
test-CA TLS and a trusted Unix Docker daemon with json-file/local drivers.
Additional TTY evidence uses synthetic provenance. Follow admits 30-second demand;
retention, source gaps and revocation remain explicit. Known bearer redaction is
bounded; application stdout may contain sensitive values and delivery is not
exactly once. Manual enrollment emits the exact log flag/capability agreement;
SSH automation and full A01 remain unqualified.

Introduced lint reported zero issues, the contract has 173 routes, and the full
Go build passed. Retained 864 frontend tests and focused race evidence remain
at `ac598e53466fbe6827e0f4d79723f51df5695a85`; production paths were unchanged
by the fixture-close repair. Twenty enrollment tests remain attributed to
`2f15c86f8e6b713eb1a72943fd4646a1b86e2543`.
At accepted roll-forward, 73 primary HTTP-served assets matched embedded files
and the 74-file manifest
`002c9dc9ff0567a822e17c1c4c1cf3f79b0290fd34ffaa29dfdb2134121e3432`.
The main binary hash at that log checkpoint was
`cf022655b79acdd6ce86f3efd2cb3e83add878db73cf6347d4d6bbcad6a503c1`.
That binary and manifest describe the accepted log roll-forward checkpoint.
The later accepted trigger integration `0a9422194e8bff7485e7fb20c02f74418ea2ce51`
passed five PostgreSQL tests and a 12.58-second compiled CLI gate: commit A was
admitted, the branch moved to B, and the runner built/served A. Controlled public
and private TLS checkout verified exact revision handling and no unavailable-commit
fallback. These fixtures use trusted routing, seeded initial setup and a test CA;
they do not qualify provider authentication, production ACME or a complete
acceptance scenario. Trigger-affected PostgreSQL evidence of 108.392/8.019 seconds
remains attributed to source `21349d3e2492dca8b8b3ca3d99132085889360e8`.
Fresh main contract checks passed 173 routes, and 73 HTTP-served assets matched
embedded files and the 74-file manifest
`f95e4d116c52dfeadc3ccf3686cf6484e4e654c13255c34bfa84c2e4d2b623a3`.
Earlier evidence retains its original revision and does not describe the current
static artifact or imply a rerun.

The earlier integration `3af3fbb3153d0450bf600e62df40a14faf583b72` failed combined
client decoding checks. Repair `0b450dd55351aa5884b9fe83eb7bb3c0e96084ff` resolves
that blocker and passed three independent required reviews before acceptance.
Fresh accepted evidence includes the 1.38-second API/PostgreSQL log round trip,
2.756-second API check, 51.261-second relevant PostgreSQL checks and 1.842-second
full runner-client check. Retained full-store 346.320-second and 811-frontend-test
evidence remains attributed to `3af3fbb3153d0450bf600e62df40a14faf583b72`; the
repair did not change the store or frontend paths supporting that evidence.
The main binary hash at that control checkpoint was
`586e4a754378f9882a447d088f986ef31f389bb92cd4ec90563fd760277d71a6`.
The asset manifest at that control checkpoint was
`7e8208110b92416f7ed20984141a0f63690bdd138fab331e028b1c8773ba607a`.
At that runtime-log control checkpoint, the contract had 173 routes and accepted
migrations extended through 0055.
Those earlier results establish control prerequisites; accepted live collection
and viewer evidence is recorded above.

At accepted dashboard integration `24c363fd22ac8237a09c12040386a442b0a3ff53`,
combined evidence records 811 frontend tests in 73 files and a 75.63-second real
compiled-collector/browser fixture. HTTP-served assets matched embedded files and
manifest `7e8208110b92416f7ed20984141a0f63690bdd138fab331e028b1c8773ba607a`.
The qualified main binary hash was
`30a6768c8e2cec2162bbb853b084f448a2654d8ce72b1d2f2dd14bc16820ec85`.
The fixture used seeded accounts, resources and deployment. It does not establish
public ACME, general metrics or complete onboarding. At that dashboard checkpoint, the contract had 167 routes and accepted
migrations extended through 0054. Earlier evidence below retains its
original revision and was not rerun by this documentation change.

Accepted UI evidence records a 19.2-second browser run and 26.20-second Go fixture,
748 frontend tests in 70 files, and a 74-asset web manifest
`6057d72030cc23ca03fd951f340c007f60fc405a0c9500c10e6d3a1192519378`.
The accepted collector's 43.00-second compiled CLI/test-CA/Docker gate verified
healthy status, stopped/exited state, SIGTERM join, stale/unknown after 30.2
seconds, resumed collection and rejection of an old report after replacement.
At that collector checkpoint, the contract had 167 routes and accepted migrations
extended through 0054.
Full-package lint failed on three existing errcheck findings in unchanged tests;
lint of introduced changes against the accepted base passed with zero issues.
These results do not claim whole-package lint is clean.

Retained runtime evidence includes the 11.34-second combined gate and 20.691-second
PostgreSQL race check at runtime integration `0a50debdfa5ab14191e52025cd1bb62684e86cd6`.
Status integration `b55bbcf7b2313e32391a4390dcbb8562de2617b5` retains store/API
race checks of 59.679/2.931 seconds and full PostgreSQL store checks of 287.732
seconds. Its contract has 167 routes; sqlc generation compared 36 generated files. The
web artifact manifest at that checkpoint was
`2031889de69af8ec37d3999505414fa99e375b9a0e6b72a74c7d014e5d50200e`.
Those earlier checks establish their named boundaries. The later UI and collector
qualification is recorded above.

Retained combined checks at `66b58845dab1fdee6f8b12809f0434a2a94326ac` passed
focused race checks, 29 PostgreSQL race tests in 35.812 seconds, vet, the 164-route
contract, lint, Go build, generated API types and route-generation checks. These foundation checks
do not establish end-to-end environment application, production ACME or full parity.

These are retained reports and source fixtures for their exact revisions. This documentation change does not rerun those application/runtime gates or turn their separate results into a complete current release assertion. Test-only isolated DNS and a trusted CA do not establish public DNS or production ACME.

The reviewed control-plane source passed unit/race, PostgreSQL authority/expiry/replay/capability, transport, contract and generated-output checks. Integration separately passed focused race/vet/contract and frontend/embed checks plus a 43.35-second public CLI build-to-HTTPS fixture, including failed-build/missing-image preservation, lost receipts and protected cleanup. That earlier fixture uses a trusted test CA and did not execute private checkout. The later private runtime/UI rows add that bounded coverage, without establishing complete A03.

Revision 3 targets remain unimplemented unless the ledgers identify narrower accepted behavior. In particular, no durable Task/Workspace product, qualified KVM/Firecracker backend, Mac/Windows/NAS/GPU adapter, reservation/budget system, remote shared cache, whole-artifact/check reuse, task preview, agent adapter or handoff is claimed. D0–D3 and A38–A66 require future implementation and combined qualification. Partial hosting evidence does not establish all of A01–A37 or complete Coolify parity.

The pinned Coolify reference stays `b12d1386f3d458618b4c579babcd1e339c6c40b3`; its revision 2 evidence is retained. W1–W13 were reviewed against primary documentation on 2026-10-22. They describe dependency behavior, not NeroCD integration qualification. Docker's overview still labels S3 cache unreleased, while its S3 page labels it experimental. Adoption must pin and test the chosen driver/backend combination.

The unaccepted Task backend checkpoint `6a3d04bc28e950d569cbc7705a5d1dbf8df8c5f5` and Task UI checkpoint `560ef12499cd361c7c2eae5a8a7d310c33ce34a7` are preserved and deferred. Neither is merged into the accepted baseline. The UI uses temporary DTO wiring; its Go/browser validation was not run. These checkpoints are not support claims or justification for integration.

Only documentation changes in this status candidate. No new hosting feature or workspace runtime, schema, API, configuration or migration is implemented here. Earlier accepted application/runtime work remains recorded above.

## 13. Sources

NeroCD baseline

- N1: [Architecture.](architecture.md)
- N2: [Service, environment and deployment domain.](../internal/domain/domain.go)
- N3: [Deployment application service.](../internal/app/deployments_service.go)
- N4: [Provenance resolver and current Compose restrictions.](../internal/runnerruntime/provenance_resolver.go)
- N5: [Service-account lifecycle.](../internal/app/service_accounts_service.go)
- N6: [Secrets guide, runner secret handling, lease-bound access.](secrets.md)
- N7: [Local artifact store](../internal/artifactfs/store.go) and [secret encryption helper](../internal/secretcrypto/secretcrypto.go).
- N8: [Compose health and reconciliation.](../internal/runnerruntime/compose_engine.go)
- N9: [Linked rollback and active-deployment constraints, deployment persistence.](../internal/store/postgres_deployments.go)
- N10: [Production operations, restore authority invalidation.](production.md)
- N11: [Validation and release gates.](../Makefile)

- N12: [TaskRun, template, runner and lease domain](../internal/domain/domain.go). These are execution foundations, not a durable collaboration Task.
- N13: [Runner enrollment, claim and lease persistence](../internal/store/postgres_runners.go).
- N14: [Accepted hosting resource domain and placement](../internal/domain/hosting.go), [hosting services](../internal/app/hosting_service.go).
- N15: [Same-host artifact deployment](hosting-local-build-runtime.md), [resource deployment UI](hosting-build-deployments.md).
- N16: [Typed build output](hosting-build-logs.md), [durable runner delivery](hosting-build-log-delivery.md).

Coolify capability reference

- C1: [Pinned repository README.](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/README.md)
- C2: [Build and deployment model.](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Jobs/ApplicationDeploymentJob.php)
- C3: [Docker Compose.](https://coolify.io/docs/applications/build-packs/docker-compose)
- C4: [Build servers](https://coolify.io/docs/core/infrastructure/servers/build-servers), [server overview](https://coolify.io/docs/core/infrastructure/servers/overview).
- C5: [Proxy overview, domains.](https://coolify.io/docs/core/networking-in-coolify)
- C6: [Preview deployments.](https://coolify.io/docs/applications/ci-cd/github/preview-deploy)
- C7: [Database capability matrix.](https://coolify.io/docs/databases)
- C8: [Services and template behavior.](https://coolify.io/docs/services)
- C9: [Monitoring and operations](https://coolify.io/docs/applications/operations/overview), [web terminal](https://coolify.io/docs/core/infrastructure/servers/web-terminal).
- C10: [Roles and permissions, team ownership.](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Models/Team.php)
- C11: [How Coolify works.](https://coolify.io/docs/core/how-coolify-works)
- C12: [Railpack.](https://coolify.io/docs/applications/build-packs/railpack)
- C13: [Static applications.](https://coolify.io/docs/applications/build-packs/static)
- C14: [Rolling updates.](https://coolify.io/docs/applications/deployments/rolling-updates)
- C15: [Rollbacks.](https://coolify.io/docs/applications/deployments/rollbacks)
- C16: [External secret managers.](https://coolify.io/docs/integrations/security/using-secret-managers)
- C17: [Storage mounts.](https://coolify.io/docs/applications/configuration/persistent-storage)
- C18: [Instance backup](https://coolify.io/docs/core/backup-and-recovery/instance-backup), [instance restore](https://coolify.io/docs/core/backup-and-recovery/instance-restore).
- C19: [Database backups, database restore.](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Jobs/DatabaseBackupJob.php)
- C20: [Storage backups.](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Jobs/VolumeBackupJob.php)
- C21: [Create scheduled tasks.](https://coolify.io/docs/applications/operations/scheduled-tasks)
- C22: [Notification events, notification overview.](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Models/Team.php)
- C23: [Application migration.](https://coolify.io/docs/applications/operations/migrate-between-instances)

Implementation reference

- T1: [Compose Go library](https://github.com/compose-spec/compose-go). This is a candidate dependency, not a claim that NeroCD already uses it.

### Workspace implementation references

Reviewed on 2026-10-22. These primary sources qualify design assumptions only; NeroCD host/backend integration and dependency-version qualification remain planned.

- W1: [Firecracker FAQ](https://github.com/firecracker-microvm/firecracker/blob/main/FAQ.md), Linux/KVM host and matching guest architecture.
- W2: [Apple Virtualization Linux VM guide](https://developer.apple.com/documentation/virtualization/creating-and-running-a-linux-virtual-machine), native Mac VM configuration and lifecycle.
- W3: [Apple container machine guide](https://github.com/apple/container/blob/main/docs/container-machine.md), home-directory and SSH-agent sharing defaults for this specific mode.
- W4: [Microsoft WSL advanced configuration](https://learn.microsoft.com/en-us/windows/wsl/wsl-config), host mounts, interoperability and VM settings.
- W5: [Docker registry cache](https://docs.docker.com/build/cache/backends/registry/), external build-result cache separate from the image.
- W6: [Dockerfile RUN cache mounts](https://docs.docker.com/reference/dockerfile/#run---mounttypecache), builder-local cache mounts are absent from cache-to exports.
- W7: [Docker cache invalidation](https://docs.docker.com/build/cache/invalidation/), build secrets do not automatically invalidate cache.
- W8: [Docker cache storage backends](https://docs.docker.com/build/cache/backends/), scoped exports and S3 still listed as unreleased.
- W9: [Docker Amazon S3 cache](https://docs.docker.com/build/cache/backends/s3/), experimental availability and a non-default driver requirement.
- W10: [OCI image index specification](https://github.com/opencontainers/image-spec/blob/main/image-index.md), platform descriptors and referenced manifests.
- W11: [Docker image attestation storage](https://docs.docker.com/build/metadata/attestations/attestation-storage/), attestation manifests within image indexes.
- W12: [Docker Buildx remote driver](https://docs.docker.com/build/builders/drivers/remote/), connections to externally managed BuildKit.
- W13: [Microsoft WSL security model](https://wsl.dev/technical-documentation/security/), distributions and integration settings do not create an untrusted-code sandbox.
