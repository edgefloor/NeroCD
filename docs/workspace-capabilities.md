# Workspace capabilities

Shared workspaces, durable collaborative Tasks, coding agents, machine pools and
model services are deferred product scope. Ordinary hosting requires none of
these. The [specification](product-spec.md) retains all R01–R102 and A01–A66;
the [plan](plan.md) prioritizes hosting R01–R68 and A01–A37 first.

`planned` means a target has no accepted product implementation. `partial`
identifies specific reusable accepted foundations, not completion of a
requirement. No workspace row is validated by ordinary runner or hosting
tests alone. Every future qualification must name candidate, backend/tool
versions, architecture, fixture, result and limits.

Before any workspace-track change, identify its named hosting requirement,
why current hosting needs it now, its minimum scope and its hosting acceptance
test. [The four-part test](plan.md#hosting-dependency-test) must precede work;
future usefulness or sunk effort is insufficient. Remote builders, registry
delivery, cache, release/approval and observations can ship their bounded
hosting contracts without the broader workspace platform. Trusted-host hosting
does not wait for VM qualification; keep untrusted execution disabled until
its declared isolation requirements are met.

## Deferred product capability matrix

| Capability | Current implementation | Required target and acceptance | Track and dependency |
| --- | --- | --- | --- |
| Durable Task and activity | deferred; existing `TaskRun` and operational templates are execution records | Goal/collaborators/source lineage, human/integration/agent activity, multiple runs/candidates/reviews; R69–R70, R74; A38–A39, A66 | Deferred partial R69/R74 checkpoint; no active hosting dependency established |
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

## Deferred gates and preserved checkpoints

D0 covers machine/authority foundations, D1 retained work and shared builds,
D2 an independent preview and handoff, and D3 reviewed deployment. All four are
future workspace gates. Their combined acceptance still includes applicable
hosting A01–A37 and workspace A38–A66.

The [plan](plan.md#preserved-deferred-checkpoints) identifies recoverable backend
and UI experiments. They are unaccepted and outside main. Temporary DTO wiring
and unrun Go/browser checks do not establish a Task product. Sunk effort does not
justify integration.

## Retained accepted foundations

The Go application, PostgreSQL/River, authorization, audit, leases and journals
provide reusable implementation. [Hosting support](compatibility.md#accepted-foundations-and-evidence)
includes public/private HTTPS Git builds, same-host artifact deployment, managed
routing, build output, runtime configuration/status/logs and selected stateful
recovery. These bounded paths do not establish workspace, VM, task-grant, remote
cache, artifact-service or preview contracts.

Any workspace change must first pass the
[hosting-dependency test](plan.md#hosting-dependency-test). Build delivery,
permissions, checks, approval or observation work can satisfy a specific hosting
need without creating the broader workspace platform.
