# Workspace capability ledger

Assessment: accepted NeroCD `852f8e6b8d5624a95865c276dcea4a239d1f8fea`,
2026-10-22. This ledger covers revision 3's workspace product. The
[hosting ledger](compatibility.md) retains full Coolify scope at
`b12d1386f3d458618b4c579babcd1e339c6c40b3`.
The [specification](product-spec.md) preserves R01–R102 and A01–A66.
The [plan](plan.md) orders D0–D3 and later H/P/M/R/O/V tracks.

`planned` means a target has no accepted product implementation. `partial`
identifies specific reusable accepted foundations, not completion of a
requirement. No workspace row is validated by ordinary runner or hosting
tests alone. Every future qualification must name candidate, backend/tool
versions, architecture, fixture, result and limits.

## Product capability matrix

| Capability | Status at the accepted baseline | Required target and acceptance | Track and dependency |
| --- | --- | --- | --- |
| Durable Task and activity | planned product; additive project-scoped metadata/activity backend in progress and unaccepted, Task UI planned; existing `TaskRun` and operational templates are execution records | Goal/collaborators/source lineage, human/integration/agent activity, multiple runs/candidates/reviews; R69–R70, R74; A38–A39, A66 | First partial R69/R74 metadata/activity slice, then D0 admission |
| Workspace and checkpoint | planned; controlled hosting checkout is a bounded component | Owned mutable checkout, one writer, immutable portable checkpoint, excluded secrets and declared resume/restart/evidence-only limits; R71–R73; A40–A41 | D0–D1; host/guest fencing and grants |
| Source handoff | partial foundations only; Git source receipts and immutable versions exist | Authorized patch/commit/review-branch operations, actual provider/broker ref enforcement and stable lineage; R75; A45 | D1; task grants and provider adapter |
| Machine pool and reservations | partial hosting Server/destination/grant identity; no general Machine reservation product | Typed native/emulated capabilities, trust/location/capacity filters, atomic reservations and local enforcement; R03–R07, R76–R77; A42–A44 | D0, then P |
| Budget and availability | partial attempt deadlines/leases; no aggregate task/model/cloud budget | Limits shared across retries, observation gaps, persistent capacity, drain/rejoin, blocked eligible capacity and explicit cloud authority; R78–R80; A42, A57, A64 | D0–D1, then P/M |
| Qualified isolation and grants | planned; rootful build Docker is an explicitly trusted host mode | One tested Linux/KVM/Firecracker profile first, deny host/LAN/metadata exposure, actual provider/broker restrictions and revocation; R81–R83; A43–A45 | D0; never inferred from tags or Docker fixtures |
| Agent adapter | planned; generic shell execution is not an agent lifecycle adapter | Versioned start/observe/interrupt/collect, model/authentication requirements, source checkpoint and continuation limits; R84; A38–A41 | D1 |
| Human production decision | partial legacy approvals and hosting holds; no new Task production-plan policy | Agent work cannot self-authorize via UI/CLI/MCP/webhook/auto-deploy or redefine trusted checks; R17–R24, R85; A46–A47 | D3 |
| Model endpoint and lifecycle | planned | Authenticated scoped endpoint, explicit identity/limits/usage/availability; installation/GPU serving separately qualified; R86; A57 | Endpoint may precede broader M |
| Effective build identity | partial source/build/daemon/platform receipts and same-host artifact binding | Freeze mutable workspace and all effective inputs, opaque output-affecting secret generations and dependency refresh policy; R87–R88; A49–A50 | D1, then R |
| Shared cache and remote delivery | planned; accepted builds retain local artifacts on one daemon | Authorized registry-backed cache on a second builder, separate read/write trust, immutable exports and remote artifact publication; R14–R16, R89, R92; A48, A51, A65 | D1; no S3/Garage cache guarantee |
| Verification and reuse | planned as a product; existing CI/provenance are reusable evidence foundations | Exact source/final artifact subjects, trusted check/policy/fixture/freshness identity, native/emulated coverage and sealed attestations; R90–R91; A46, A52–A53 | D1–D3, then R |
| Artifact service and retention | partial local content-addressed helper and hosting artifact records | Authorized bounded publication/transport, completion state, corruption handling and protected retained references; R74, R92; A56, A65 | D1; local helper alone has no shared-service authority |
| Useful private preview | planned; local deployment/routing are reusable hosting paths | Application plus isolated seeded PostgreSQL and needed worker, private ingress, exact release and component health, independent lifetime/expiry/data policy; R54–R56, R93; A54–A56 | D2 |
| Handoff and teammate review | planned | Current checkpoint, diff/log/check evidence and preview; short-lived attachment; quiesced writer or separate workspace; R72–R74, R93; A40–A41, A54–A55 | D2 |
| Reproduce and move applications | planned | Authorized diagnostics/production-data copy, side-effect restrictions, exact available evidence, writer-coordinated migration/cutover; R94, R96; A58–A59 | O; storage/recovery foundations |
| Supported upgrades and restore tests | planned; platform restore invalidates stale authority but does not resume held mutations | Version-path contracts, backup/data preconditions, meaningful restored reads/writes and owned cleanup; R95, R97–R98; A60–A61 | O; engine/application-specific evidence |
| Progressive delivery and drift | planned | Eligibility, observations and persisted recovery decisions; external data/resources remain protected; R99–R100; A62–A63 | O |
| Vendor packages | planned | Publisher/artifact identity, customer-controlled policy, recovery, revocation and compromised-update handling; R101; A63 | V |
| Evidence-based AI assistance | planned | Link measured baseline, actual change, executed checks/benchmarks and uncertainty; narrative cannot approve or create trusted execution evidence; R102; A66 | Every agent/reuse/optimization slice |

## Machine and backend qualification

All rows below are planned NeroCD qualifications. The dependency documentation
in [W1–W13](product-spec.md#workspace-implementation-references) supports design
constraints; it is not a passed integration test.

| Host/backend | Proposed roles | Qualification before support |
| --- | --- | --- |
| Linux/KVM/Firecracker, one native architecture | First isolated agent/build/test work | Real KVM access, supported kernel/guest/helper, restricted filesystem/network, resource enforcement, lifecycle interruption and patch policy |
| Additional Linux architecture | Compatible native work | Separate build/runtime/artifact tests; cross-compilation or emulation is labeled and does not establish native execution |
| Native Mac Virtualization.framework | Native ARM64/Linux guests and supported persistent environments | Explicit sharing profile, VM lifecycle/network/data recovery and actual host-version tests |
| Restricted Windows VM | Untrusted Linux agent work where qualified | Separately managed isolation boundary and host exposure tests; nested virtualization needs separate qualification |
| WSL | Explicit trusted work only | Host-drive/interoperability settings are not sufficient for an untrusted-code boundary |
| NAS | Storage first; compute only where qualified | OS/architecture/filesystem, capacity, backup consistency, virtualization and maintenance matrix |
| GPU/native model host | Supported device or constrained model endpoint | Device identity, driver/model/license support, VRAM/capacity ownership, isolation profile, upgrade and interruption |
| VPS/gateway | Ingress/control services or eligible applications | Connectivity and authenticated tunnel/data policy; no assumption that a cheap VPS provides KVM or keeps an offline host running |
| Manually enrolled cloud capacity | Authorized compatible jobs/environments | Same identity/grant/trust/location/capacity rules; no implicit private-data upload |
| Provider provisioning/fallback | Explicit overflow policy | Provider credentials, locations, spend cap, data policy and owned cleanup |

A pool schedules separate jobs. It provides neither shared memory across arbitrary
hosts, distributed GPU inference nor universal process/live-data migration.

## Completion gates

D0 establishes machine/authority foundations, D1 retained work and shared builds,
D2 a useful independent environment and handoff, and D3 reviewed deployment.
All four are planned. Their combined acceptance uses applicable hosting cases
A01–A37 plus workspace A38–A66, scoped as required by the demonstration and
later tracks. Each later capability still needs its own fixtures.

The first proposed implementation is the additive Task metadata/activity slice
in the [plan](plan.md#next-implementation-slice). It must not advertise a
Workspace, reservation, VM sandbox, agent execution or private preview merely
because the UI can link an existing Run or project environment.

## Retained accepted foundations

Current reusable hosting and control-plane evidence is documented in
[the hosting ledger](compatibility.md#accepted-foundations-and-evidence) and
[specification section 12](product-spec.md#12-evidence-and-review-limits).
It includes real public Git builds, same-host artifact deployment, controlled
Traefik routing, durable typed build logs and the bounded output viewer. The
Go application, PostgreSQL/River queue, authorization, audit, leases and journals
remain the implementation foundation. No parallel job queue is proposed.

General application-runtime logs, terminals, managed database lifecycle,
persistent data recovery, provider events and complete Coolify compatibility
remain planned hosting dependencies. Direct hosting stays usable without a Task.
