# Hosting-first implementation plan

Active delivery priority: complete the functional self-hosted hosting workflows
of Coolify at `b12d1386f3d458618b4c579babcd1e339c6c40b3`.
The [specification](product-spec.md) retains all R01–R102 and A01–A66.
Prioritize R01–R68, A01–A37 and the declared hosting matrices. The broader
revision 3 workspace/agent product is subsequent scope, not a prerequisite.

Hosting-first documentation baseline: `65311f30470f34d0d26165d1cd13325fbd05fca0`.
Historical accepted application/source assessment:
`9a69228f9a2a8324e25b1c366ee4eba913b8dba2`, 2026-10-25, through migration 0072.
Current bounded connected-application/owned-volume qualification:
physical source `09a432407c41671517fa342d28c07e9c79978aea`, physically verified on 2026-10-25,
with documentation accepted at `3b192e4f9602d606f9f51c921adbce8870401588`, through migration 0074 and journal 17. This qualification does not establish full hosting
acceptance or parity.
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
| Application and persistence | Go API/application/store layers, PostgreSQL through migration 0074, River OSS v0.44.1, authorization, audit and React UI | Extend these contracts instead of creating a parallel queue or control plane |
| Projects and placement | Shared project environments, typed resources, enrolled server/default destination, project grants and binding mutation holds | Broader SSH/host qualification, complete team/server access and legacy mappings |
| Secrets | Encrypted project-secret versions, protected reveal, private Git credential delivery and bounded managed runtime delivery/execution; separate runner-file execution | Broader delivery and variable scopes, provider coverage and key recovery |
| Desired runtime configuration | Accepted migration 0052 and resource GET/PUT API, generation compare-and-swap, idempotent requests, public nonsecret literals and exact managed-secret version references; current owner writes/viewer reads | Broader value scopes and complete hosting qualification |
| Admission and protected delivery | Migration 0053 admission and protected runner endpoint; metadata authorization/audit before decryption, bounded original window; journal format 8 persists only nonsecret references and delivery ID | Broader runtime qualification |
| Runtime execution and status controls | Opt-in CLI/API applies literal/exact managed versions via protected Docker stdin; bounded challenge/report and viewer freshness API, migration 0054 | Broader log qualification and disk/CPU metrics |
| Runtime configuration UI and collector | Accepted authenticated browser deployment journey and opt-in independent Docker/loopback collector; four targets per 10-second cycle with fair selection | Read-only status dashboard is accepted; runtime-log control and migration 0055 are accepted; bounded live collector/viewer are accepted; no per-resource 10-second guarantee |
| Status dashboard and enrollment commands | Bounded current/stale/historical target display under current viewer authority; accepted manual UI enrollment/generated-command collector with persisted capabilities and diagnostic readiness | Broader SSH automation, readiness/log qualification and general metrics |
| Builds | Public builds and private HTTPS Basic/PAT checkout; compiled CLI Dockerfile/Railpack and pinned Vite SPA qualification, owner grant selection and Dockerfile/Railpack static-provider UI through exact artifact deployment and test-CA HTTPS | Full language/provider coverage, broader compiled frontend qualification, remote publication and hosting cache |
| Deployment | Guarded verified-route API/controller with explicit agent opt-in and strict trusted-TLS rollback; exact same-host artifact deployment through CLI/API/UI, ordinary HTTP readiness and selected managed routing | Public production TLS qualification, protected deployment approval, remote image delivery and supported hosting previews |
| Build output | Typed receipts and retention cursors, redaction, journal format 7, delivery/replay before completion, bounded opt-in viewer | Broader runtime log/status coverage, resource actions and terminals |
| Routing | Explicit proxy capability, Traefik generation/holds and isolated trusted-test-CA HTTPS fixtures | Public DNS/production ACME qualification, broader certificate modes, continuous observations and hosting preview access |


The [hosting ledger](compatibility.md#accepted-foundations-and-evidence)
contains exact revisions and limits. Public Git builds, same-host artifact
deployment, explicit route selection and typed build output are accepted.
Private provider breadth, broader variable/managed-secret delivery, production TLS
qualification, broader runtime-log/status coverage and the broader hosting
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

The connected application, PostgreSQL/Redis and owned-upload-volume workflow is
qualified within the [connection guide's profile](hosting-database-connections.md).
The next H2 gap identified here now has a bounded accepted result. Immediate
PostgreSQL logical backup and restore into a new private database are accepted at
`56cce329e620472e3c82e9ed7ef0ae0618f124e4`. The embedded UI, compiled runner,
application reads and new writes, completion replay and failure holds passed
within the [backup qualification profile](hosting-database-backups.md#accepted-qualification-and-evidence).

Scheduled PostgreSQL backups and the guest `pg_dump` timeout are accepted at
`60b86e47504533280170028cbc0b9b632c7b67db`, with physical source
`c76019739ffbde0bcf7eb357c9740ddd35bf9a57` retained by exact source correspondence. See the [schedule qualification and limits](hosting-database-backup-schedules.md#physical-qualification-and-retained-evidence).

Remote PostgreSQL backup and application-verified recovery are accepted at
`6621204964e96039558c0f267dbac50475a7ad98`. The [remote qualification](hosting-database-remote-backups.md#accepted-qualification-and-durable-evidence)
proves failed-upload local preservation, same-archive retry, acknowledgment replay
and a forced remote GET before fresh-target restore. The pinned Silo fixture uses
explicitly trusted loopback HTTP; production S3 HTTPS remains unqualified.

PostgreSQL retention/recovery is accepted at
`5003e16c0c0fc48d31c29f444f47a97b94e10339`. Its [qualification](hosting-database-backup-retention.md#accepted-qualification-and-durable-evidence)
covers independent local and remote retention, immutable-batch replay,
explicit continuation and application recovery from retained copies. The profile
uses pinned Silo with trusted loopback HTTP. Production S3 HTTPS remains
unqualified. This closes the active bounded workflow without a new subsystem or
UI expansion.

PostgreSQL backup/upload failure notifications are accepted at
`1eb2d01c9262c6bad5a03579545226189f88f1be`. The [notification qualification](hosting-backup-notifications.md#accepted-qualification-and-durable-evidence)
proves actual SMTP and HTTPS webhook alerts, durable retry/history and authenticated
explicit recovery with application reads and new writes. The physical dump
failure is uncertain and retains its observed source state. The two-event slice
uses the existing PostgreSQL 17.11/Linux arm64/rootful Docker/Silo profile and
private-CA notification TLS. Other events/channels, public-CA deployment and
complete R63/A25 remain open.

Owned-upload remote backup and verified new-volume recovery are accepted at
`58716fea31420f1f10258385de25e2c712e12770`. The
[storage remote workflow](hosting-storage-remote-backups.md) qualifies direct
uploads-only target setup and validation, separate local/upload outcomes,
same-archive retry, fresh GET before extraction, and application reads/new writes
with source preservation. The 87.774-second gate includes real PostgreSQL
atomicity/replay/revocation tests and live browser evidence. It qualifies one
native Linux arm64 trusted classic-store Docker/Silo same-destination profile,
not production S3 HTTPS, storage schedules/retention/notifications, cross-host
recovery, or full A25–A27/A37. The existing user UI binary was preserved.

Continue H2 from the remaining recovery gaps. Full upstream cron grammar,
broader notification events and channels, other engines,
cross-destination and in-place restore need their own contracts and acceptance.
Broader R42 profiles, rotation,
production ACME and aggregate stateful hosting gates remain open. This accepted
slice does not change the deferred workspace gates.

## Earlier hosting checkpoints

Accepted private HTTPS work at `4ba9fcac1b484244cb2c662e53f588c2dce6b71e`
includes source grants, protected credential transport, compiled CLI Dockerfile
and Railpack execution, and a Dockerfile browser workflow through build output,
exact local artifact deployment and test-CA HTTPS. See the
[scope and revision-specific evidence](compatibility.md#accepted-foundations-and-evidence).
The private Railpack static-provider UI qualification is accepted at
`a92d100c6ba654e6797432e7ef7a6cfd55527272`. It covers UI creation/editing,
exact-version grant retry after committed-response loss, compiled CLI build,
exact artifact deployment, test-CA HTTPS and preservation of the old application
through a genuine failed rebuild. Viewer denial and responsive layouts are covered.
A03 remains partial. Arbitrary language providers, Nixpacks, compiled frontend
coverage beyond the pinned SPA fixture, provider integrations and public production ACME remain unqualified.
See the [runtime limits](hosting-build-runtime.md#enable-private-https-git) and
[fixture contract](../acceptance/runtime-hosting-local-build/README.md).

The live runtime-log slice has bounded accepted evidence for R58–R60 and A28–A29
within the limits below. [GitHub build-on-push](hosting-github-push.md) is accepted
at `be85bf4c4d42c0b2ddb9e9935f2fa74332656bde`. Its signed fixed-branch/current-head checks,
deduplication, delayed-delivery handling, owner recovery and viewer history
provide bounded R09/R10/R13/R57 and A09 evidence. The local TLS fixture uses
registered API/River, compiled CLI and Docker; deployment was manual at that build-only checkpoint.
See the [accepted evidence and limits](compatibility.md#accepted-foundations-and-evidence).

The [readiness diagnostic backend](server-readiness.md) is accepted at
`3cb62858e3d1e263d89d5e3c6aecd0a30792e938`, through migration 0057. Its API
reports bounded evidence. Manual UI enrollment, the generated command and compiled
native Linux arm64 agent are accepted at `0771f1b56befcd30b195c45121dba4d3efeec974`.
The [retained qualification evidence and limits](compatibility.md#accepted-foundations-and-evidence)
cover fixture-CA HTTPS, current ready/blocked/stale recovery, viewer denial,
role loss and persisted-credential restart. CLI checks inspect metadata only;
Docker API 1.54 and 1 GiB per work/journal root are required. amd64 is compile-only.
Production TLS, SSH, application deployment and complete A01 remain open.
The [verified route-switch engine](hosting-route-switch.md) is accepted at
`99250c2634109cdd2008c56dae1b1740d377c0a1`. It verifies old/new trusted TLS and
rollback under an opt-in engine contract. The guarded API/controller is accepted
at `5687b88465839b8e341181ba8b47f54dc17f006c`, through migration 0058, for exact
healthy/retained-running releases. Authority checks and unresolved metadata
conflict/cancellation/replay holds remain enforced. Resource-scoped reads and the
operator UI are accepted at `fa93d16fa09d5a7929581d5acb206029be9752d0` and
`1c6ddb414d984372bb1f081fb6ff23db8bd81f88`. Explicit review, frozen retries and
pending-operation recovery preserve holds. The bounded
[completed metadata-conflict recovery API](hosting-proxy-recovery.md) is accepted
at `e097ffa706325f55cab7a7b0416a235a0e9ad151`, through migration 0059. Its repaired
concurrent replay preserves one resolution and convergence operation. Uncertain
outcomes remain held. The recovery administrator UI is accepted at
`d238d23e63ea2ae7f1bb0f7ed843f7b71863660f`, with immutable-plan review,
confirmation, scoped exact retry and current authorization. Recorded metadata
settlement does not prove convergence completion. Trusted
[automatic deployment](hosting-auto-deploy.md) is accepted at
`86825ab17f2e2bb0b2b0a26e6e65e2e2769d8fa3` in optional `automatic_unprotected`
mode after a healthy manual release and existing routes. Protected approval is
unsupported. The SSH installer and durable control have bounded accepted evidence;
the full SSH API/UI flow is accepted at `b4951282aefb3ad63d525c9d10037d7120f1d4ba`
for the prepared native arm64 Debian 12 fixture, with pinned SSH identity and
response-loss inspection recovery. The four-capability baseline is image-hosting
only, not source-build/proxy/log/storage qualification. Complete A01 remains open. See the
[revision-specific evidence and limits](compatibility.md#accepted-foundations-and-evidence).
Pinned Vite SPA compilation is accepted at `d29edb3bc8cde064108891f70713423bb57776a3`.
See the [bounded evidence and reliability limits](compatibility.md#accepted-foundations-and-evidence). Hosting navigation is
accepted at `43e9c305f71a267b1d201f7849c12a31f8c532d6`, with Home, Projects and
Servers and runners leading direct hosting and advanced workflows preserved.
See the [navigation evidence and limits](compatibility.md#accepted-foundations-and-evidence).

Automatic deployment and SSH onboarding have bounded accepted evidence. Proceed
with H2 stateful hosting. Bounded uploaded-file persistence/recovery and
standalone PostgreSQL/Redis hosting have accepted evidence. Connected application
binding was unfinished at the following checkpoints; current bounded storage
qualification is summarized below. Broader stateful qualification remains open.
Storage metadata, the private helper and durable materialization admission are
accepted through `8e00631aac26e0787a43398332e31a35980f067d`, with migrations
0062–0063. The opt-in API/compiled-runner uploaded-file workflow now has physical
qualification through migration 0064, including public options/status and typed
materialization acknowledgment. Bounded unqueued-continuity recovery is accepted
at `284f45cf1659a559d55d54342a902d0eeb112685`; its contract/database tests do not
prove proxy convergence. Storage deployment-review/recovery UI is accepted at
`77be63a03c9caf3e95239a751045f0fe7b5918cf` for connected review/retry UX with
intercepted operation responses, not new physical runtime proof. Metadata API/UI is accepted at
`2f8beb9b555e651bcccb47d3c0dbc7ccfdb268db`; saving metadata does not mount a
volume or prove persistence. Database creation/domain, metadata UI, private controller/helpers and protected
credential delivery are accepted through `33a7506f8b5f92f88f455e3cc4029629dfb6c7a9`.
Direct engine qualification is accepted at `b9c4c93ef55d946ca67384599d4c6d6290e36d0e`;
default-off runner coordination is accepted at `f4a54506f388339ffe8acdd6b8ec4133690188fa`.
The exact combined authenticated API/River/compiled-runner PostgreSQL/Redis
workflow was physically qualified at `c731f708e62e2376d174041957b8654fe83d5bcf`.
That evidence remains at its original revision. Later shared database engine
changes mean it does not qualify the current combined tree. Direct deployment UI
is accepted with separate intercepted-browser evidence.
Default-off opt-in and metadata `adapter_unqualified` remain. Internal R42
prerequisites are accepted through `c52c03102d94a8bbc5beaaf91bbf3a54b6c4d923`:
the binding controller through `15fe8877d2e7ee22602d8cc20fb8db29fc83de0c`,
retirement proof at `a87bb68373016509d24724aba6d127738012638c`, private
application/database adapter at `621af78038f7231b43f7d7a7a899b17bde5c06e4`,
origin capture controller at `584cb6c7f4a62821308acad4a5e67192e3608e11`, and
private empty-v2 alias upgrade engine at that checkpoint. Their
integration checks and code hygiene, specification and critical security reviews
passed. The [component ledger](compatibility.md#r42-remains-an-unfinished-hosting-workflow)
links source and tests and records the limits.

Accepted progress now extends through `d3849bb572ce5b7df40ef7324ef6bc71e8e08c92`.
The read-only observer, authenticated capture API/typed client, format 13 journal
and private exact-ID coordinator are implemented. The
[direct-engine origin/upgrade fixture](../acceptance/runtime-hosting-database-origin-upgrade/README.md)
physically passed PostgreSQL 17.11 and Redis 8.2.10 A→B→C in 27.89 seconds, with
private DNS application reads/writes, retained volume/network/credentials and
origin, reopened UID 999 ownership, and wrong-alias rejection. Combined local
race, vet, changed-code lint, Linux arm64 compile and build checks passed.

This direct-engine fixture uses local materialization and proves no controller
provenance for upgrades. Later accepted progress at
`b7771468edf95f531c13d69b361ed901d9efbbc8` qualifies the compiled exact-ID capture
CLI through real API/River source deployment and controller/materialization
provenance. Both PostgreSQL and Redis passed capture/replay in 22.00 seconds and
normal database recovery in 23.40 seconds without skips. Lost origin ACK leaves
the format 13 claim/plan/origin chain retained; ordinary mode holds, and
historical replay with the owned socket absent succeeds without repeating claim,
plan load, observation or credential delivery. Source, volume, network and
credentials remain unchanged. Completion authority-worker joining and persisted
UTC JSON identity are fixed. The
[compiled-runner contract](../acceptance/runtime-hosting-database-runner/README.md)
records the native Linux arm64/trusted Docker/test-CA scope and limits.

Private upgrade control and compiled execution are now accepted through
`79b50e245a756022b2fc0b8ac98428bb1acfcc3b`, including migration 0071. The
owner-scoped controller derives retained capture/origin proof for initial empty-v2
upgrade and maintenance, with durable idempotency and historical ACK replay.
Execution requires explicit runner upgrade opt-in and the matching capability.
Selected actual PostgreSQL control tests passed without skips, store in 104.040
seconds and API in 15.104 seconds, with exact cleanup. The compiled physical
three-fixture gate is accepted at `563fb88f707b75563d03f0408df0267e3af473e9`.
Capture/replay passed in 38.22 seconds, normal recovery in 23.40 seconds and
empty-network upgrade in 26.36 seconds, all without skips. PostgreSQL and Redis
passed initial upgrade and maintenance, retained origin, alias reads/writes,
exact Complete-ACK loss replay and single-writer checks. Owned cleanup and outer
inventories were verified. Qualification remains bounded to the native Linux
arm64 disposable private fixture with a verified test CA. Engine-version upgrades
remain open. Automatic capture dispatch is accepted separately below.
See the [current evidence and limits](compatibility.md#r42-remains-an-unfinished-hosting-workflow).

Automatic database capture is accepted at
`5ff5b7e114fc922e3c3f2a6e93670c133d205923`. The default-off
`--hosting-database-network-capture` flag uses bounded claim-next selection under
current authority, same-claim response-loss recovery, and retained ACK replay
before Docker observation. The physical gate passed both PostgreSQL and Redis
in exact-ID and automatic capture modes, capture 32.92 seconds, normal recovery
23.71 seconds and upgrade 28.23 seconds, without skips. Automatic replay proved
the origin ACK and journal retirement with Docker absent before cancellation;
it does not establish successful exit of the whole continuous runtime. Owned
cleanup and outer inventories passed. See the
[current evidence and limits](compatibility.md#r42-remains-an-unfinished-hosting-workflow).

## Current connected stateful workflow

Current connected application and owned-upload-volume qualification uses physical
source `09a432407c41671517fa342d28c07e9c79978aea`, with documentation accepted at
`3b192e4f9602d606f9f51c921adbce8870401588`, through migration 0074 and journal 17.
One image application connects to PostgreSQL or Redis without storage first, then
attaches one owned named volume through acknowledged interruption. The qualified
UI/API/runner workflow covers routed replacement, failure recovery and a later
fresh deployment retaining database records and uploads. See the
[connection guide](hosting-database-connections.md) for the exact profile,
683.423-second full pass, historical evidence and limits. Qualification uses a
trusted native Linux arm64/root-image fixture and private-CA HTTPS. It does not
qualify production ACME, arbitrary nonroot initialization, database-native
backup/restore, disk loss, cross-host/external storage or full R/A/matrix parity.

### Historical storage-less route qualification

The connected public-image workflow is physically qualified at
`5277ab2ff5258ae6a4aee2fb642ed925d9a827cb`, tree
`a197801c8a7a66dd493f9eef7acd940070d845a2`, through migration 0073 and
journal format 16. An owner can connect one PostgreSQL or Redis database on the
same destination, then redeploy the application through UI or API while continuing
all reviewed existing HTTPS routes. Data and credential references survive app
replacement and database maintenance. The candidate starts and passes readiness
while the old app remains healthy. A strict HTTPS proxy switch precedes a fresh
finalize lease that retires the exact predecessor.

The full gate passed in 333.592 seconds with five runtime groups and zero skips.
Route failure checks took 136.17 seconds, the connected app group 89.70 seconds,
capture 25.11 seconds, normal database recovery 24.91 seconds, and upgrade 24.64
seconds. Four Chromium tests passed per engine, including the owner workflow in
38.4 seconds for PostgreSQL and 37.3 seconds for Redis. Readiness and cutover
failures preserve the old HTTPS app and database reads/writes for both engines.
Lost yield, proxy Complete and final Complete acknowledgments replay the original
retained attempt without repeating effects or credential delivery.

The physical receipt is
`/tmp/nerocd-connected-route-authority-integrated-physical-handoff.json`.
Source and binary identities are retained in
`/tmp/nerocd-connected-route-authority-integrated-sourceproof.json`, with sanitized
output in `/tmp/nerocd-connected-route-authority-integrated-runtime-redacted.log`.
Owned processes and resources were removed. The unrelated 35 containers and 209
volumes retained identical inventories; root graph and 75 generated UI files were
unchanged, and the independent user UI remained ready.

Qualification covers the pinned native Linux arm64 trusted Docker classic-store
fixture and verified private CA. Initial Connect cannot include routes. Source
builds, application storage, cross-server binding presentation, rotation,
database-native backup/restore, disk loss and public ACME remain unqualified.
There is no in-flight request draining or control-plane restart proof. Full R42,
A17 and all full requirement, acceptance and matrix gates remain incomplete.

R42 still needs broader application profiles and explicit unreachable cross-server
binding presentation. The bounded workflow does not complete the requirement.
Database-native backup/restore, rotation, full A17/A26/A37 and broader stateful
parity remain open. Production ACME, untrusted workload isolation and broader
host profiles are not qualified by this pass.

The standalone archive helper is accepted at
`3b34b3920378cc41613bc87ca69eea2d35d2f175`;
controller/config/API and default-off runner are accepted through
`88887e92c0cf7b93b828d3696ec3a5af16547ea7`, with migration 0068 and accepted helper
packaging. Bounded physical uploaded-file recovery is accepted at `45298ea4bbd85c91ee4b0d4d64cd72d9346e1faa`;
remaining stateful recovery cases and broader R42 workflows are unfinished;
current helper config-ID qualification targets the classic Docker image store,
with a recorded containerd-store lookup failure and no runtime fallback;
retained PostgreSQL/River tests use a fake engine. Archive UI is accepted at
`a99a75517c018caa9aa9965cf451f9f826e7b3d6` with intercepted-browser evidence;
bounded physical uploaded-file qualification is accepted at
`45298ea4bbd85c91ee4b0d4d64cd72d9346e1faa`; broader application qualification
remains unfinished. Full application-verified
A26/A37, database-native backup/restore, unknown-writer recovery and complete
A19/A26 remain open. Protected plans
under R17/R19 and applicable A09/A10 checks, production TLS and remaining parity
remain separate unfinished work. Live GitHub webhook configuration,
PR previews, other providers, remote image transfer and complete A09 remain open.
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
references without decrypting or injecting values. UI was absent at that checkpoint
and is accepted below.
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
It provides bounded challenge/report and viewer freshness controls.
Configuration UI source `558d365571630e2a701aef3f60c2e9710771ba54` is accepted at
`46ceb43f77085e9d11c5c6375b79d6cbfc8e1485`, with actual authenticated browser,
compiled-runner and Docker proof within seeded-resource/trusted-artifact limits.
Collector source `bd9638c227db67e10d1ee9647b40c60c5676f0a5` is accepted at
`132012237ee484e9cd6fce2fba656966567a1cc8`. Enable it with
`--hosting-observation` with matching persisted `hosting_observation_v1` capability. It reads narrow
Docker status and loopback readiness, selecting four targets fairly per 10-second
cycle without guaranteeing a 10-second interval for each resource.
The bounded read-only runtime-status dashboard is accepted at
`24c363fd22ac8237a09c12040386a442b0a3ff53`, including source
`e5bc0b6aa85bae5ef409ebf3e8e6758fe30f45f6`. It separates current/stale/historical
target evidence, revalidates current viewer access and bounds timeout/retry behavior.
Enrollment command repair `1c4e7076cde2c79d2aa3d46eb4de2133d72e8a3e` uses exact
persisted capabilities and configuration prerequisites; SSH automation and complete
host readiness remain separate work. Runtime-log control and migration 0055 are
accepted at `aa88f62e76a4ef964900db17bf81515e141da03b`, including decoder repair
`0b450dd55351aa5884b9fe83eb7bb3c0e96084ff`. Actor sessions, renewal, stop,
bounded retained reads, exact runner claims/events, counters, replay gaps and purge
are accepted prerequisites. Live collection and the embedded viewer are accepted
at `3a2b524ca622a7ebfba761c590803d110192cb9f`. The real registered settled
deployment fixture verifies fresh Docker stdout/stderr through the compiled
collector and API into the embedded viewer. Follow admits 30-second demand;
retention/source gaps and revocation remain explicit. Manual enrollment includes
`--hosting-runtime-logs` matched to persisted `hosting_runtime_logs_v1`; it does
not qualify SSH-assisted enrollment or full A01. Known bearer redaction does not
remove arbitrary application secrets, and delivery is not exactly once. The earlier failed integration
`3af3fbb3153d0450bf600e62df40a14faf583b72` retains its historical decoder failure;
the accepted repair resolves that blocker without establishing complete A29. Disk/CPU metrics remain planned.
Docker administrators can inspect environment values;
controlled log scans do not prove arbitrary output is secret-free. Broader value
inheritance, broader provider delivery, broader SSH qualification, live webhook setup and production TLS qualification
remain separate hosting work.

Exact trigger-revision source `21349d3e2492dca8b8b3ca3d99132085889360e8` is accepted
at `0a9422194e8bff7485e7fb20c02f74418ea2ce51`. A trusted internal caller freezes
the intended commit before first checkout, with capability-gated admission and
execution, revision-bound idempotency and no branch fallback. Manual public API
requests retain their existing configured source behavior. The accepted GitHub
integration adds provider event authentication and current-head ordering; webhook
registration remains manual. See the [revision contract](hosting-build-trigger-revision.md)
and [push contract](hosting-github-push.md).

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
