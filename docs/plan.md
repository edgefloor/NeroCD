# Workspace and hosting implementation plan

Revision 3 adopts every R01–R102 requirement and A01–A66 scenario in the
[product specification](product-spec.md). Current accepted baseline:
`852f8e6b8d5624a95865c276dcea4a239d1f8fea`, assessed on 2026-10-22.
The supplied `09d65d4ddcbfdf27e97158a9c08beb02a721818c` is historical.
No delivery dates or performance improvements are asserted.

The first complete demonstration is the [task-to-reviewed-deployment journey](product-spec.md#56-task-to-reviewed-deployment-first-complete-demonstration).
D0–D3 are the primary delivery gates. Direct hosting remains independently
accessible and the complete Coolify target stays in track H. Earlier P00–P18
package names and M0–M4 hosting milestones are historical sequencing vocabulary,
not a substitute for the new first demonstration. Track P means the owned-machine
pool; a label such as P05 below identifies an earlier hosting work package.

## Accepted starting point

| Foundation | Accepted bounded behavior | Work still needed for the workspace demonstration |
| --- | --- | --- |
| Application and persistence | Go API/application/store layers, PostgreSQL through migration 0051, River OSS v0.44.1, authorization, audit and React UI | Extend these contracts instead of creating a parallel queue or control plane |
| Projects and placement | Shared project environments, typed resources, enrolled server/default destination, project grants and binding mutation holds | New durable Task/Workspace model, stable Machine mapping, team access, capacity/trust qualification and reservations |
| Secrets | Encrypted project-secret versions, scoped lifecycle and protected reveal; separate runner-file execution | Short-lived task/provider grants, managed execution delivery, network/data boundaries and key recovery |
| Builds | Operator-enabled public HTTPS Git, Dockerfile, Railpack and prepared-static builds into recorded local artifacts | Mutable workspace snapshots, remote publication, shared cache, complete build-input manifest and reuse eligibility |
| Deployment | Exact same-host artifact deployment through CLI/API/UI, ordinary HTTP readiness and selected managed routing | Remote image delivery, independent private previews, target-specific plans and protected human production decisions |
| Build output | Typed receipts and retention cursors, redaction, journal format 7, delivery/replay before completion, bounded opt-in viewer | General task activity, durable checkpoints, command/check evidence and runtime application logs |
| Routing | Explicit proxy capability, Traefik generation/holds and isolated trusted-test-CA HTTPS fixtures | Private preview access/tunnels, broader certificate modes, public DNS/production ACME qualification and continuous observations |

The [hosting ledger](compatibility.md#accepted-foundations-and-evidence) and
[workspace ledger](workspace-capabilities.md) carry exact revisions and limits.
Actual local builds, deployment, route selection and output delivery are
accepted. They are not a qualified VM sandbox, a shared artifact service or a
complete workspace product.

## Next implementation slice

The next frozen backend slice is in progress and unaccepted. It adds durable
Task metadata and activity. The accompanying Task UI is planned. This makes the
new collaboration object visible without pretending that a generic run is a
Workspace or that an agent already executes remotely.

1. Freeze the Task/run distinction and same-project link rules before shared
   schema/API/UI edits. Preserve existing `TaskRun`, template and hosting
   execution contracts and their identifiers.
2. Add a project-scoped Task with immutable owner, title, goal, status
   (`open`, `closed`, `archived`) and revision. Server activity is distinct
   from user comments. Create/comment receipts provide idempotency; edits use
   compare-and-swap. Collaborator management, source lineage and review are
   later parts of the full R69 target.
3. Link authorized existing runs and project environments using stable
   identities and project consistency. A link is a relationship, not authority
   to claim work, mutate a deployment or access a terminal.
4. Enforce current project viewer reads and maintainer writes through the
   existing application layer, including same-project run/environment links.
   Task status changes never mutate execution or resources. Add the planned
   Task UI only after the accepted backend contract, with the same access rules.
5. Validate real PostgreSQL persistence and migration, cross-project denial,
   retries/concurrency, audit/activity behavior, API/client agreement and
   responsive keyboard-accessible UI. No workspace execution, VM, agent,
   reservation, task-grant or preview claim belongs to this slice.

This is a partial R69/R74 slice, not completion of either requirement, D0 or
the first demonstration. Migration 0052 is allocated to this unaccepted slice;
it is not part of the accepted schema yet. Migration 0051 is already applied
and must not be edited. Inspect accepted state before allocating later schema.

## Dependency order after Task metadata

| Slice | Required boundary before expansion | Product evidence |
| --- | --- | --- |
| Machine identity and reservations | Preserve Server/destination IDs and data ownership; qualify one Linux architecture/KVM backend; atomically couple capacity to attempt authority | R03–R07, R70, R76–R79; A39, A42–A44 |
| Task grants and isolation | Enforce restrictions at host/helper/network/provider boundaries, not only metadata; separate task, preview and production authority | R75, R81–R85; A44–A47 |
| Workspace and agent | One adapter, one writer, bounded disk, cancellation, durable source checkpoint and explicit continuation limits | R69–R74, R84; A38–A41 |
| Remote build and artifacts | Frozen effective inputs, authorized bounded publication, registry-backed cache and independent artifact/check reuse records | R13–R16, R87–R92; A48–A53, A65 |
| Independent review environment | Application plus isolated seeded PostgreSQL, optional worker, private ingress, observations/runtime logs and trusted final-artifact checks | R54–R56, R58–R59, R90, R93; A54–A56 |
| Review and production plan | Human decision binds the exact artifact, target configuration, required checks and data preconditions; all interfaces enforce it | R17–R24, R85; A46–A47 |
| Combined demonstration | Exercise disconnect, disappearance, revocation, expiry, missing artifacts and cleanup on the same candidate | D0–D3; applicable A01–A37 and A38–A56, A64–A66 |

Model endpoint integration can start independently when its access/usage contract
is explicit. Managed model installation or GPU access cannot inherit that result.

## First complete demonstration and later tracks

### D0–D3

|Gate                                |Deliverable                                                                                                                                                                   |Exit conditions                                                                                                                               |
|------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------|
|D0: Machine and authority foundation|Shared environment/resource model, Task/run distinction, stable machines, one qualified Linux VM backend, capability checks, reservations, managed secrets and task grants    |A bounded remote task survives client disconnect; unauthorized/stale work is rejected; legacy data and destination ownership remain valid     |
|D1: Retained work and shared builds |One coding-agent adapter, isolated checkout, source checkpoints, BuildKit on remote workers, registry cache, authorized artifact publication and explicit build identity      |Task produces a retained candidate; a second compatible builder imports cache; interruption and budget behavior are demonstrated              |
|D2: Working environment and handoff |App plus seeded isolated PostgreSQL preview, private ingress/TLS, runtime observations/logs, trusted checks against the final artifact, teammate review and supported takeover|Another user opens the task and working application after the agent run ends; evidence and permissions agree; cleanup preserves data ownership|
|D3: Reviewed deployment             |Target-specific plan, human production approval, same-artifact promotion, mutation fencing and supported failure recovery                                                     |The full journey passes together, including changed-source/plan, lost-worker, revoked-grant, missing-artifact and expiry cases                |

D0 is enabling work. D1 proves retained execution and shared output. D2 proves useful collaboration. D3 completes the first product demonstration. One supported architecture and one backend are sufficient for this milestone, provided limits are explicit.

Implement conservative complete-artifact reuse and verification reuse after input/evidence recording is dependable. The initial demonstration may rebuild through shared layer cache and rerun checks. Its schema and UI must already distinguish these outcomes so later reuse does not require misleading history.

### H, P, M, R, O and V

These tracks are separate acceptance claims and may advance in parallel when their prerequisites are satisfied.

|Track                            |Deliverable                                                                                                                                                               |Completion evidence                                                                                                 |
|---------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------|
|H: Core hosting and compatibility|Direct source/image/Compose hosting, automatic builders, broader databases/catalog, storage, operations, schedules, notifications and Coolify migration                   |Original hosting cases A01–A37 and declared parity matrices pass for the release candidate                          |
|P: Broader owned-machine pool    |Native Mac adapter, qualified Windows VM and trusted WSL paths, qualified NAS roles, additional architectures, power/availability policies and explicit cloud provisioning|Backend-specific isolation, placement, recovery, networking and capacity fixtures pass                              |
|M: Owned model services          |Endpoint integration, supported model lifecycle, native/GPU execution profiles, model storage and quotas                                                                  |Model/driver/device matrix, authorization and interruption/upgrade behavior pass; measured resource use is available|
|R: Reuse and larger workloads    |Conservative full-artifact/check reuse, additional cache transports where justified, retention/garbage collection and measured concurrency                                |Reuse invalidation, trust, source/fixture identity, artifact availability and resource accounting cases pass        |
|O: Application care              |Version-specific upgrades, guided data migration, tested restores, database-aware plans, eligible progressive delivery and drift reconciliation                           |Application/version-path fixtures and failure/recovery cases pass                                                   |
|V: Vendor packages               |Verified package identity, customer-controlled installation/update policy, compatibility and recovery metadata                                                            |Publisher/customer authority, revocation, version pinning and compromised-update scenarios are defined and tested   |

An existing authenticated model endpoint can be connected before managed GPU/model lifecycle is ready. A manually enrolled cloud machine can add capacity before provider provisioning is ready. Neither shortcut should be presented as completion of the broader track.

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
