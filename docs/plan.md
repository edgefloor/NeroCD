# Hosting-first implementation plan

Active delivery priority: complete the functional self-hosted hosting workflows
of Coolify at `b12d1386f3d458618b4c579babcd1e339c6c40b3`.
The [specification](product-spec.md) retains all R01–R102 and A01–A66.
Prioritize R01–R68, A01–A37 and the declared hosting matrices. The broader
revision 3 workspace/agent product is subsequent scope, not a prerequisite.

Hosting-first documentation baseline: `65311f30470f34d0d26165d1cd13325fbd05fca0`.
Accepted application/source assessment:
`b55bbcf7b2313e32391a4390dcbb8562de2617b5`, 2026-10-23, through migration 0054.
The supplied `09d65d4ddcbfdf27e97158a9c08beb02a721818c` is historical.

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

| Foundation | Accepted bounded behavior | Remaining hosting gaps |
| --- | --- | --- |
| Application and persistence | Go API/application/store layers, PostgreSQL through migration 0054, River OSS v0.44.1, authorization, audit and React UI | Extend these contracts instead of creating a parallel queue or control plane |
| Projects and placement | Shared project environments, typed resources, enrolled server/default destination, project grants and binding mutation holds | SSH-assisted onboarding and host readiness, complete team/server access and legacy mappings |
| Secrets | Encrypted project-secret versions, scoped lifecycle and protected reveal; separate runner-file execution | Scoped managed build/runtime delivery, broader variable scopes, provider credentials and key recovery |
| Desired runtime configuration | Accepted migration 0052 and resource GET/PUT API, generation compare-and-swap, idempotent requests, public nonsecret literals and exact managed-secret version references; current owner writes/viewer reads | Configuration UI acceptance with actual browser proof |
| Admission and protected delivery | Migration 0053 admission and protected runner endpoint; metadata authorization/audit before decryption, bounded original window; journal format 8 persists only nonsecret references and delivery ID | Broader runtime qualification and configuration UI browser proof |
| Runtime execution and status controls | Opt-in CLI/API applies literal/exact managed versions via protected Docker stdin; bounded challenge/report and viewer freshness API, migration 0054 | Collector implementation, status UI, runtime logs and disk/CPU metrics |
| Builds | Public builds and private HTTPS Basic/PAT checkout; compiled CLI Dockerfile/Railpack, owner grant selection and Dockerfile UI through exact artifact deployment and test-CA HTTPS | Automatic-builder private UI qualification, provider integrations, compiled static, remote publication and hosting cache |
| Deployment | Exact same-host artifact deployment through CLI/API/UI, ordinary HTTP readiness and selected managed routing | Public production TLS qualification, deployment policy, triggers, remote image delivery and supported hosting previews |
| Build output | Typed receipts and retention cursors, redaction, journal format 7, delivery/replay before completion, bounded opt-in viewer | Runtime application logs, continuous status, resource actions and terminals |
| Routing | Explicit proxy capability, Traefik generation/holds and isolated trusted-test-CA HTTPS fixtures | Public DNS/production ACME qualification, broader certificate modes, continuous observations and hosting preview access |


The [hosting ledger](compatibility.md#accepted-foundations-and-evidence)
contains exact revisions and limits. Public Git builds, same-host artifact
deployment, explicit route selection and typed build output are accepted.
Private provider breadth, broader variable/managed-secret delivery, production TLS
qualification, runtime logs, continuous status collection and the broader hosting
workflows remain incomplete.
Test-only DNS and a trusted CA do not establish public production ACME.

## Active delivery sequence

| Order | Complete hosting outcome | Requirements and acceptance |
| --- | --- | --- |
| H1: Practical hosting | Onboarding, repository/image input, private Git, required variables and secret delivery, trusted deploy-on-push, deployment policy, production TLS qualification, runtime logs and continuous status | Applicable R01–R33 and R57–R68; A01–A14, A20, A27–A29, A35 plus their failure/recovery and provider/proxy fixtures |
| H2: Useful stateful hosting | Compose, persistent storage, PostgreSQL/Redis, application bindings, initial tested catalog, backups and application-verified restores | R34–R53 and related secret/operation rules; A15–A22, A25–A27, A37 |
| H3: Remaining core parity | PR/MR previews, remote builders/image delivery, provider/build/database coverage, terminals, schedules, notifications and supported UI/API/CLI/MCP operations | Remaining core R01–R68/A01–A32/A35–A37 and declared matrices together on the candidate |
| H4: Migration and extended compatibility | NeroCD/Coolify migration, unchanged-client adapters where declared and extended infrastructure | R67, specification section 7, A33–A34 and dedicated compatibility matrices |

Use actual dependencies and verified gaps to select each bounded slice.
Legacy-ID/permission preservation and data-ownership protection ship with the
hosting behavior that needs them; H4 does not authorize delaying those safeguards.
A practical deployment demo alone does not complete Coolify parity.

## Next hosting slice

Accepted private HTTPS work at `4ba9fcac1b484244cb2c662e53f588c2dce6b71e`
includes source grants, protected credential transport, compiled CLI Dockerfile
and Railpack execution, and a Dockerfile browser workflow through build output,
exact local artifact deployment and test-CA HTTPS. See the
[scope and revision-specific evidence](compatibility.md#accepted-foundations-and-evidence).
A03 remains partial. The private browser fixture does not qualify an automatic
builder or provider integrations, and test-CA HTTPS is not production ACME.

The next H1 slice is collector implementation and actual browser proof for runtime
configuration, followed by runtime application logs and continuous status qualification. This serves R29–R32,
R58–R60 and the applicable hosting acceptance scenarios, including A28 and A29.
Use existing resources, Go application authorization, managed-secret versions,
PostgreSQL/River and fenced runner execution. Do not require a collaboration
Task, Workspace, VM or general grant platform.

The accepted desired-configuration foundation fixes explicit version selection,
current owner writes/viewer reads, generation compare-and-swap and idempotent
requests. Admission and protected delivery now enforce their accepted authority
and replay contracts. Runtime execution now has bounded Docker/API proof for
rotation/revocation, controlled log scans, cleanup and preservation of the serving container. Define how configuration changes affect redeploy/restart and
retained rollback. Keep build, deployment and ongoing runtime logs distinct,
with bounded reads, reconnect gaps and stale observation status. Verify failure,
response loss, canceled authority and preservation of the serving release.

Desired runtime-configuration persistence/API source
`5b435d4e8e35562ab6c9a7116a6b0b5f16e85751` is accepted at
`bdd5e0168655edd31df48a7818a560a49876147e`, including migration 0052.
It saves explicit public nonsecret literals and exact managed-secret version
references without decrypting or injecting values, and has no UI.
The Docker protected-environment helper `8f33c86126f3eb52492490b3e7e3a11962a27e65`,
integrated at `8e6c9c28a91f6c76981b683f444886d9ed7a193b`, was unwired at that checkpoint.
The accepted runtime integration now wires it.
Admission source `7c51350083f6fa448e8043e32158ecf9427be6d6` is accepted at
`1ddfb1f43416ff8109c31a852963942448035583`, including migration 0053.
Protected delivery source `c7d3a0d5a4fdb00bf9b2ab5e6bd0cdfd05e6b30e` and
journal source `804d4f460c6f106993c0a223f36eb42831f6a7f2` are accepted at
`66b58845dab1fdee6f8b12809f0434a2a94326ac`.
Delivery checks exact prepared authority, current actor, configuration and secret
versions, commits a metadata-only audit before decrypting, and retains the original
bounded delivery window on retry. Journal format 8 persists only nonsecret
references and delivery ID; older formats retain legacy behavior.
Runtime source `420300352ba14f40f082c519f6a75d04f82129c1` and original-credential
repair `b9836b8a626fc12c5a6e4dde44ebc40e80b870a2` are accepted at
`0a50debdfa5ab14191e52025cd1bb62684e86cd6`. Explicit CLI/API opt-in applies
literals and exact managed-secret versions through protected Docker stdin, with
fixed delivery/replay windows and deadlines. After authorization commits and
before returning environment bytes, the delivery endpoint rechecks the original credential and rejects delivery if it detects
rotation. It cannot recall bytes already sent or values already delivered to the
runner or container.
Status-control source `56baaa7ec5f155ca490fa7cc989833ed92de9189` is accepted at
`b55bbcf7b2313e32391a4390dcbb8562de2617b5`, including migration 0054.
It provides bounded challenge/report and viewer freshness controls. Collector
implementation, status UI and disk/CPU metrics remain pending.
The reviewed configuration UI candidate remains unaccepted pending actual browser
proof and integration. Docker administrators can inspect environment values;
controlled log scans do not prove arbitrary output is secret-free. Broader value
inheritance, provider delivery, SSH, webhooks and production TLS qualification
remain separate hosting work.

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

Preserve these and unrelated work without deletion, reversion or history
rewriting. Finish only the cleanup needed for coherent recoverable checkpoints.
Do not integrate them merely because implementation effort has already been spent.
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

Use the existing Go application layer, PostgreSQL transactions, River lifecycle,
runner fences and journals. Queue dispatch completion is distinct from workload
completion. The controller must never interpret an agent narrative as trusted
check evidence or a database fence as proof that a host process stopped.

The Leader freezes interfaces and seven-part briefs, uses the current native
HOLD/GO gates, assigns bounded disjoint ownership and integrates approved
candidates serially. Children do not spawn children. Independently review
code hygiene and specification fit at each exact candidate; add security or
cryptographic review for the relevant critical boundaries. A repair requires
new reviews and affected checks. Required missing/failed checks block acceptance.

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
