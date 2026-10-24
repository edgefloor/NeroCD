# Hosting compatibility ledger

Hosting-first documentation baseline: `65311f30470f34d0d26165d1cd13325fbd05fca0`.
Accepted application/source assessment:
`45298ea4bbd85c91ee4b0d4d64cd72d9346e1faa`, 2026-10-24.
The Coolify reference remains
[`b12d1386f3d458618b4c579babcd1e339c6c40b3`](https://github.com/coollabsio/coolify/tree/b12d1386f3d458618b4c579babcd1e339c6c40b3).
Earlier planning at `a763a90b3896257b784c9498d01650ccdad2427a` and the
supplied revision 3 baseline `09d65d4ddcbfdf27e97158a9c08beb02a721818c`
are historical. Official documentation is separately dated and can change.

This ledger is the active delivery ledger: complete hosting R01–R68/A01–A37
and the declared matrices first. Direct hosting remains the primary navigation
and onboarding path; a Task or coding agent is not required.
The [workspace ledger](workspace-capabilities.md) separately covers Tasks,
Workspaces, machines, agents, models and shared results. The [plan](plan.md)
orders practical hosting, stateful hosting, remaining core parity and migration/
extended compatibility before the deferred D0–D3 workspace gates, preserving
all R01–R102 and A01–A66
in the [specification](product-spec.md). Feature, migration and unchanged-client
compatibility remain separate claims.

Any workspace-track work requires a [named hosting requirement, need now,
minimum scope and hosting acceptance test](plan.md#hosting-dependency-test).
Hosting contracts for remote builders, cache, releases, approvals and observations
can proceed without speculative workspace infrastructure. Trusted-host deployment
does not require a new VM backend; untrusted execution remains disabled until
its declared isolation requirements are satisfied.

## Status and evidence

- `planned`: required target without accepted implementation evidence.
- `partial`: bounded accepted foundations cover part of the capability.
- `implemented`: code exists without complete acceptance of the declared boundary.
- `validated`: named fixtures pass on a named candidate with stated limits.
- `intentionally different`: a declared behavior difference is supported explicitly.

Short fixture names in matrices are acceptance targets, not nonexistent tests
presented as passing. Real source paths below describe bounded evidence. Reports
retain their original revision; this documentation candidate runs no application
or runtime tests. No complete hosting parity or workspace milestone is inferred
by combining separate historical results.

## Accepted foundations and evidence

The latest accepted additions supersede historical absence claims for local
artifact deployment, build output and private Git control-plane contracts:

| Foundation | Accepted revision and durable source evidence | Limits |
| --- | --- | --- |
| Same-host artifact deployment | `61b77e75f2d95f3f015b0275a15ce2e376d82330`; [runtime](hosting-local-build-runtime.md), [real fixture](../internal/runnerruntime/local_build_deployment_integration_test.go) | Explicit capability/configuration; exact daemon/platform/ImageID; no pull, rebuild or cross-host transfer; separate route selection |
| Artifact deployment UI | `08b9ff21f8def396d13f9464a197469e4d59adba`; [UI and real browser gate](hosting-build-deployments.md#deploy-through-the-resource-ui) | Actual build/deployment, retries, current generations, viewer denial and responsive layouts; public DNS/production ACME remain unqualified |
| Typed build output backend | `28fc384bdb8fb641558cd6d20224d207c7095f77`; earlier replay checkpoint `1938a2fb5b41223d5b01303b88807d09b8933e4e`; [contract](hosting-build-logs.md) | Full-authority immutable receipts and retention cursors; old pre-0051 history is explicitly unknown |
| Typed pagination | `3ea2f815f844ff60008ecf22891bec5b370fb07f`; [API regression tests](../internal/api/hosting_build_log_pagination_test.go) | Build log limit 200 works at the global request boundary; legacy limits remain unchanged |
| Runner output delivery | Candidate `65dc15c1462485d84c9d6d524577f26a20048677`, integration `5c0a1329c89f4a784a254aac6679591db3d6b0b0`; [delivery contract](hosting-build-log-delivery.md), [controller gate](../acceptance/runtime-hosting-build-controller/gate.sh) | Real output, redaction, format 7 cursor journal, exact historical replay, delivery before completion, retention, lease renewal and cancellation; unknown secrets are not guaranteed removed |
| Build output viewer | Candidate `07ce38d8230a84d5d1ef469bc8f604c5230afde4`, accepted `852f8e6b8d5624a95865c276dcea4a239d1f8fea`; [viewer contract](hosting-build-logs.md#resource-build-output-viewer), [browser tests](../web/app/tests/browser/local-build-deployments.spec.ts) | Opt-in persisted output with bounded local plaintext and access revalidation; runtime application logs and terminals remain planned |
| Private HTTPS Git control plane | Source `f89e019da9a2641da9dc36592fd54908e896b003`, accepted `edf8f14c8262c896e4c886088f8058878d77e965`; [grant/API tests](../internal/store/postgres_hosting_source_grant_test.go), [capability tests](../internal/store/postgres_hosting_private_capability_test.go), [credential client tests](../internal/runnerclient/hosting_source_credential_test.go) | Private HTTPS configuration, owner-created/revoked source grants, gated build admission/dispatch and typed credential endpoint/client; final expiry, exact replay and current capability checked. At that checkpoint compiled CLI refused private capability; current runtime/UI additions are below; complete A03 remains partial |
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
| Default-off database runner coordinator | Source `9040572132e60787186fd59ffddc57d765d6c752`, accepted `f4a54506f388339ffe8acdd6b8ec4133690188fa`; [typed coordinator](../internal/runnerruntime/hosting_database_execution.go), [replay tests](../internal/runnerruntime/hosting_database_replay_test.go), [CLI configuration tests](../cmd/nerocd/runner_database_config_test.go) | Journal v11, protected delivery before ApplyBegin, current-authority callback, native Linux arm64 and verified HTTPS for database runner-to-control-plane requests; capability disabled by default; accepted code and race/build evidence, API/runner physical workflow and direct deployment UI were not qualified at this coordinator checkpoint; later accepted evidence is below |
| Private database API/runner workflow and direct UI | Accepted `c731f708e62e2376d174041957b8654fe83d5bcf`; [compiled-runner fixture contract](../acceptance/runtime-hosting-database-runner/README.md), [actual API/runtime fixture](../internal/api/hosting_database_runtime_integration_test.go), [UI browser scenarios](../web/app/tests/browser/database-deployments.spec.ts) | Authenticated API/River/compiled runner physically qualified for private PostgreSQL and Redis on native Linux arm64: A write, B redeploy/read, C restart and committed Complete-ACK loss/read retain volume/network/credential references and the same operation receipt, without repeated Apply or secret delivery; direct UI accepted separately with intercepted four-width browser evidence; default-off opt-in and metadata `adapter_unqualified` remain; no application binding, archive restore or full A17 |
| Internal upload archive helper | Accepted `3b34b3920378cc41613bc87ca69eea2d35d2f175`; [format and ownership contract](../internal/storagearchive/README.md), [CLI tests](../cmd/nerocd-storage-archive/main_test.go) | Standalone fixed-root Linux helper using strict USTAR `nerocd-storage-v1`; package filesystem and CLI/vector evidence only; no controller/runner coordination, archive API/UI, user backup/restore workflow or application-verified A26/A37; helper-image packaging was absent at this helper checkpoint and is accepted below |
| Owned-upload archive controller/API/runner | Controller `1a721b270306c9816f3b91788407c8a937e51756`; config `5d6badc6cfb3aea3c51256167ebe5d9f9858d5bc`; API `1f7a26eee611fc45d685f45f88587f2e55e7b9c2`; runner `88887e92c0cf7b93b828d3696ec3a5af16547ea7`, source `f0a618218c09e14eba5aad252e31f29941275fb0`; [operator contract](hosting-storage-archive.md), [API tests](../internal/api/hosting_storage_archive_handlers_test.go), [runner tests](../internal/runnerruntime/hosting_storage_archive_controller_test.go) | Migration 0068; accepted fenced controller, startup configuration, public API and default-off runner code; actual PostgreSQL/River/controller tests use a fake engine, so physical qualification was absent at this code checkpoint; the later bounded uploaded-file pass is below; full A26/A37 remain incomplete; archive UI is accepted separately below with intercepted-browser evidence |
| Archive helper packaging | Accepted `b796ce483e99d0dec29bb3e769982dc30da64668`; [packaging contract](../internal/storagearchive/README.md) | Fixed helper-image packaging and local image qualification accepted; qualification image was removed and is not an installed pin; operator must build/install and verify the exact intended identity; current config-ID qualification targets classic image store, with containerd-store lookup failure and no runtime fallback |
| Owned-upload archive UI | Source `d98e88c64f84924bd36fb2d4c7c29ad723e80ced`, accepted `a99a75517c018caa9aa9965cf451f9f826e7b3d6`; [operator scope and limits](hosting-storage-archive.md) | Accepted UI code and intercepted-browser behavior; 1,426 frontend tests, TypeScript/generated API/build checks, 75-file embedded manifest and 74 matching served assets; artifacts retained in integration checkout, root assets unchanged; no physical application A26/A37 pass |
| Physical owned-upload archive recovery | Accepted `45298ea4bbd85c91ee4b0d4d64cd72d9346e1faa`, exact source `2ba8041359576a32d616d7f83e0c7eafe4ee4d91`; [physical fixture](../internal/api/hosting_storage_archive_runtime_integration_test.go), [operator limits](hosting-storage-archive.md) | Native Linux arm64/rootful same-host owned uploads, classic Docker image store/overlay2, trusted fixture CA and local CAS; backup/restore committed-ACK recovery starts each helper once, restores into a new resource/volume with application old-read/new-write proof, preserves source and metadata, compensates oversized failure, failed readiness and missing volume; no database backup, S3, host-disk-loss, in-place/containerd/production TLS or browser-to-real-archive qualification; not full A26/A37 |
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
Bounded physical uploaded-file recovery is accepted at
`45298ea4bbd85c91ee4b0d4d64cd72d9346e1faa`, with exact source/tree equality to
`2ba8041359576a32d616d7f83e0c7eafe4ee4d91`. The 25.38-second pass is retained in
`/tmp/nerocd-storage-runtime-20261024.iWwXss/gate.log`, with
`/tmp/nerocd-archive-physical-integrated-handoff.json`,
`/tmp/nerocd-archive-physical-integrated-physical-retention.json` and
`/tmp/nerocd-archive-physical-integrated-main-promotion.json`. The application
retains 65,537 uploaded bytes through redeploy. Committed backup and restore ACK
loss each recover with one helper start. A new resource/volume reads restored
application data and accepts a new write; the source remains unchanged with
UID 12345/GID 12346, files 0640 and directories 0750. Oversized backup failure
resumes only originally running workloads; failed-readiness rollback and
missing-volume hold also pass. Owned objects/processes are absent after cleanup
and host inventories are unchanged. The tested helper image was removed and is
not an installed reusable pin. Earlier failures remain historical.

This is native Linux arm64, rootful same-host owned uploads on the classic Docker
image store/overlay2 with trusted fixture CA and local CAS. It does not qualify
database backup, S3, host disk loss, in-place restore, containerd image store,
production TLS, browser-to-real-archive flow, full A26/A37 or full parity. R42
PostgreSQL/Redis application binding is under implementation and unaccepted.

Archive-helper integration evidence is recorded in
`/tmp/nerocd-main-upload-archive-promotion.json` and
`/tmp/nerocd-upload-archive-integrated-handoff.json`. Linux filesystem package
qualification retains 11 cases at its original source revision; native Linux CLI
qualification retains 14 cases without skips, plus the corrected standalone
vector check, in `/tmp/nerocd-upload-archive-streams-physical/final-handoff.json`
and `/tmp/nerocd-upload-archive-vector-physical/final-handoff.json`. The initial
read-only-transfer failure and invalid-vector failure remain separate historical
failures; later passes do not relabel them. Owned commands joined and cleanup
preserved host state. Integration retained the six root graph files and 75 UI
files; no new installed artifact or UI qualification is claimed. This accepted
helper is a prerequisite, not completed application backup/recovery.

Exact combined physical qualification at `c731f708e62e2376d174041957b8654fe83d5bcf` passed in
29.75 seconds, with both clients and owned outer cleanup passing. Source evidence
is `/tmp/nerocd-database-client-environment-physical-handoff.json` and
`/tmp/nerocd-main-database-qualified-promotion.json`; the stable fixture links above
identify executable assertions. Earlier failed attempts remain historical failures.
The typed-claim 409 was repaired at `664bc50e487f757a5f371c72659425c53b2a47e9`;
that repair's unit/PG evidence is distinct from the later exact physical pass.

Direct database UI evidence retains 1,380 frontend tests and four-width intercepted
keyboard-browser checks at `00ec9b461c625eba7b8fa9a1bc37cae71f987e95`, with unchanged UI inputs
recorded in `/tmp/nerocd-database-ui-claim-handoff.json`. These UI checks do not
replace physical API/runtime proof. The workflow remains default-off and opt-in;
metadata stays `adapter_unqualified`. Application binding is missing, so complete
A17 is not claimed. Archive backup/restore, public ACME and full parity remain
unfinished; the internal archive helper is accepted below; controller/API/config/runner and packaging are accepted below; physical
application backup/restore remains unqualified.

Direct-engine evidence in `/tmp/nerocd-database-qualified-physical-handoff.json`
belongs to `b9c4c93ef55d946ca67384599d4c6d6290e36d0e`: six cases passed in
30.52 seconds on native Linux arm64. The coordinator handoff
`/tmp/nerocd-database-runner-integration-handoff.json` records full combined
runner/client/runtime/CLI race checks in 81.189 seconds, compile, vet and Linux
arm64 build at `f4a54506f388339ffe8acdd6b8ec4133690188fa`. Eight engine files
remain identical to the qualified engine revision, but the shared volume helper
changed; the earlier physical pass is not qualification of the final combined
tree. The later exact combined workflow pass is recorded below. Application
binding, archive backup/restore, major upgrade, public database, AMD64
qualification, complete A17 and full parity remain unfinished.

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
default-off coordinator evidence is recorded below. Database UI was metadata
only at that checkpoint; later direct deployment UI and combined workflow evidence
are below. Application binding, complete A17, backup/archive restore and production
ACME remain unqualified.

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
accepted below; the later exact API/runner workflow and direct UI are accepted below; metadata
remains `adapter_unqualified` and application binding remains unfinished. Backups,
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
the later exact API/runner workflow and direct UI are accepted below; metadata
remains `adapter_unqualified` and application binding remains unfinished.

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

Reviewed source `f89e019da9a2641da9dc36592fd54908e896b003` passed unit/race, PostgreSQL authority/expiry/replay/capability and transport checks. Accepted integration separately passed focused race/vet, 161-route contract, frontend/embed checks and a 43.35-second public CLI build-to-HTTPS fixture with failed-build/missing-image preservation, lost receipts and protected cleanup. The HTTPS fixture uses a trusted test CA; it established public-path regression coverage at that checkpoint. Later private runtime/UI evidence is recorded above; production ACME and complete A03 remain unqualified.

Retained acceptance at `65dc15c1462485d84c9d6d524577f26a20048677`
records a real controller gate with Dockerfile, Railpack and static builds,
response loss before completion, default-lease renewal, retention cursor,
SIGTERM/cancellation and owned cleanup. Local deployment and its UI have
separate real API/PostgreSQL/River/CLI and trusted-test-CA routing/browser
evidence. They do not qualify a VM, GPU, remote builder/cache, private preview
or model adapter. See [specification evidence limits](product-spec.md#12-evidence-and-review-limits).

### Retained earlier foundations

The following rows retain the original candidate/component boundaries through
`df515f3c5819627c1ee6877b2d22e1f79895f46b`. A statement that an older
component supplied no controller, deployment or log stream describes that
revision and component only. The accepted additions above establish current
local deployment and typed build output. Their absence claims must not be
read as the current aggregate product status.

| Foundation | Accepted commits and durable source evidence | Boundaries |
| --- | --- | --- |
| Shared environments and image configuration | Backend `d38b91a77b34b271d70f7cb65de8591ecda697f9`, UI `aa7e59b8422bff5bd640548fef2b02947c6e9bda`; [application tests](../internal/app/hosting_service_test.go), [API tests](../internal/api/hosting_handlers_test.go), [UI tests](../web/app/src/pages/ProjectHosting.test.tsx) | Persisted project environments and generation-checked image settings; image syntax validation does not resolve a registry tag, run an image or prove independent deployments |
| Managed secret versions and protected UI | Backend integration `3d6583e1fff9de3e5bc05fa9097034f3d915fcd8`, UI integration `f08b790fbed77740d7e59ea4b05fbbcf2e964e05`; [lifecycle/fail-closed tests](../internal/app/managed_secrets_service_test.go), [UI tests](../web/app/src/pages/ProjectSecrets.test.tsx), [browser tests](../web/app/tests/browser/managed-secrets.spec.ts) | Encrypted project-owned immutable versions, audited owner reveal, revocation and strict keyring file; no managed-secret execution injection, team/environment value inheritance or external-provider lookup |
| Servers, destinations and bindings | API integration `02a6514b2ce0a9f2ae50dfc2e5ec7ac8ef2410f8`, canonical-capability repair `169db49909035924b5ba8f71b0b90c0a807a5d64`, UI `72bc485e54867df0785e7f6052b4aa8c6e2a2e8a`, browser repair `45d7fa9a0bd20fb3733ac97c0bc93966c803dc4a`; [application tests](../internal/app/destinations_service_test.go), [PostgreSQL migration/constraint tests](../internal/store/postgres_destinations_test.go), [browser tests](../web/app/tests/browser/hosting-destinations.spec.ts) | Admission verifies a consumed enrollment and eligible credentialed runner, then creates a stable default destination; project grants and immutable bindings record desired placement. Browser onboarding executes the real manual enrollment CLI. No SSH automation, Docker capability/runtime validation or resource deployment |
| Authoritative runner restart identity | Integration `9025ee814dd5de354a7465e39d59f3f90014d2b3`; [identity API tests](../internal/api/runner_identity_handlers_test.go) | Dedicated runner credential resolves authoritative identity before journal startup; the identity endpoint does not heartbeat or validate host runtime readiness |
| Transactional hosting job infrastructure | Foundation integration `037b621b2647bd9cb5dec234330239ed6373c0ae`, image-consumer integration `9822119a10d95c3108f4018bfaf8f516b0c88ca8`; [transaction/retry/lifecycle tests](../internal/hostingjobs/jobs_test.go), [migration checksum tests](../internal/hostingjobs/migrations_test.go), [consumer/agent fixture](../internal/store/postgres_hosting_execution_docker_test.go) | River OSS v0.44.1 with live image consumer, exact-target dispatch, schema readiness and backup identity; restored bindings remain held, with no trusted recovery proof or unlock API |
| Public image execution | Integration `9822119a10d95c3108f4018bfaf8f516b0c88ca8`; [API tests](../internal/api/hosting_execution_handlers_test.go), [real PostgreSQL/River/API/agent/Docker fixture](../internal/store/postgres_hosting_execution_docker_test.go), [Docker image fixture](../internal/runnerruntime/image_engine_docker_test.go), [gate command](../Makefile) | Anonymous public tag/digest input, committed digest/image/platform/daemon identity, ordinary HTTP readiness, fixed loopback host port, fenced apply, cancellation and exact receipt replay; the image primitive itself supplies no TLS, private registry credentials, managed variables/secrets or zero-downtime strategy |
| Image deployment WebUI | Integration `69735f6ca34e68147b00cae4afbfe135b2b5fa1b`, retained-request repair `f3d11b82b09de27d97a47af5bf45fe6b33eec253`; [browser scenarios](../web/app/tests/browser/hosting-deployments.spec.ts), [real-backend fixture](../web/app/scripts/hosting-deployment-fixture.sh), [client tests](../web/app/src/api/hosting-deployments.test.ts) | Role-aware deployment/history, fixed-port interruption acknowledgement, exact retained-request recovery under a mutation hold, cancel and separate read-only reconciliation; completed-operation readiness is not continuous health, the separate routing UI is recorded below |
| Standalone local build engine | Engine integration `7bf3ed1a0362fd4b1fa9c5d0bf0af6e532be9d1b`, settings candidate `e89179f6a554645dcbe587e33f6a3bbc90cc0149`, integration `02378fe56d44f2d37d5c64d1aa4051dbe8edc1aa`; [runtime gate](../acceptance/runtime-build/gate.sh), [real build/HTTP/resource-limit fixtures](../internal/hostingbuild/runtime_test.go), [archive tests](../internal/hostingbuild/archive_test.go) | Dockerfile/Railpack/prebuilt static on one trusted Linux/rootful Docker host, confined context/Dockerfile/target and publish-directory/SPA settings; retained local receipts. No orchestration, deployment handoff, registry publisher or log stream; disk budgets are monitored, not quotas |
| Standalone Traefik engine | Accepted integration `ea8bdc547f1ecda4d95739697eeb33efbdea942d`; [runtime gate](../acceptance/runtime-proxy/gate.sh), [real Traefik/Pebble fixtures](../internal/hostingproxy/runtime_test.go), [plan tests](../internal/hostingproxy/plan_test.go) | Linux typed routes, HTTP-01 TLS issuance/renewal, verified config rollback and coordinated persistent-state backup/restore; no public route API, database authorization, agent commands, fenced controller proxy holds, binding selection or WebUI integration |
| Per-release ports and retained cleanup | Backend `37189cad71c696ec19446423760f5fa590e347bd`, integration `4d65a72a04523f2333f826c1e313545be3a257ff`; UI `1582114b6330341153bdce03c7f44f7d2961c7ff`, integration `e1eaf13aaaf3749c460f2c7ccfb3ff5c033d4a01`; [store/Docker integration](../internal/store/postgres_hosting_execution_docker_test.go), [browser fixture](../web/app/tests/browser/hosting-deployments.spec.ts) | Automatic loopback ports retain earlier containers; explicit fixed ports require interruption acknowledgment. Exact retained cleanup protects serving references, live effects and restore holds; no automatic public traffic switch |
| Controlled Git checkout | Accepted candidate `7466fe407a9bdc279ab5afe0fb4bd1df994e713a`, integration `d63108b5dc4882fd6471fa8867d7278a3ccced78`; [checkout tests](../internal/runnerruntime/source_checkout_test.go), [transport/DNS tests](../internal/runnerruntime/source_checkout_transport_test.go), [guide](hosting-source-checkout.md) | Immutable checkout component with public HTTPS, callback-supplied HTTPS basic or pinned-host SSH keys, bounded transport and receipt acknowledgment before use; no build invocation or credential endpoint/runtime bridge; the separate public configuration API is recorded below |
| Source/build persistence | Store candidate `273987babc404d163063419169e636a65230b93d`, accepted schema repair `5f6a2a0d3087f0bf531a65628009db68280e069d`, integration `836a3a92b0ff32242bba94bf8cf0800dc638d96e`; [PostgreSQL/fence/schema/restore tests](../internal/store/postgres_hosting_build_test.go), [domain tests](../internal/domain/hosting_build_test.go), [guide](hosting-build-control.md) | Persisted immutable sources/settings, purpose-specific secret-version grants, requests, fenced receipts and local artifacts via internal repository methods; used by the accepted public build controller/executor below; at that checkpoint private credential HTTP/runtime delivery and artifact deployment were absent; current accepted additions are listed above |
| Proxy control-plane dispatch | Candidate `471d063f5062c4214f531b8ee9a41658e1c2c48c`, integration `ad82174f01da8ac964adb20cda57039484af8189`; authority repair `d7d65911be23658315596b0c392be481a9fec995`, integration `12bbbd4be048a418217cfaf1b69637307979fd98`; [API tests](../internal/api/hosting_proxy_handlers_test.go), [persisted authority tests](../internal/store/postgres_hosting_proxy_authorization_test.go), [guide](hosting-proxy-control.md) | Authorized grants/routes/generations, settled binding-derived targets, proxy/restore holds and exact-target River dispatch; backend tests use typed engine receipts. Explicitly enrolled proxy-capable Linux agents now execute this protocol, with the separate runtime/UI evidence below; image-only grants remain unchanged |
| Private Git credential delivery component | Candidate `88e7ec8767baa6cc534bda015ed5336b5687b535`, integration `12087174f7747f51f6d30f8314ef86c4183cb53d`; [application tests](../internal/app/hosting_build_delivery_service_test.go), [PostgreSQL delivery tests](../internal/store/postgres_hosting_build_delivery_test.go), [contract](hosting-build-credentials.md) | Exact live sourcing authority, immutable secret version, audit-backed delivery window and owned bytes; at that checkpoint no credential endpoint, production controller or runtime bridge; current endpoint/client, runtime and bounded UI are accepted above |
| Fenced proxy agent runtime | Repaired candidate `2037ad55912be8950e2b81e8e31847a98d5ff048`, integration `332b03942d7ada1cf094468620dfd069eadefcf1`; [combined gate](../acceptance/runtime-proxy-control/gate.sh), [agent integration tests](../internal/runnerruntime/hosting_proxy_integration_test.go), [runtime contract](hosting-proxy-control.md) | Explicit persisted `proxy_apply` enrollment and Linux configuration, journal/replay and lost-response recovery, HTTP-01/trusted-test-CA TLS and autonomous renewal during control-plane outage; unresolved and restored holds remain, with no override |
| Optional BuildID queue adapter | Candidate `13ec1392681721ffce1f9f54e7f5f690268ab534`, integration `80e17670d8e5be3bcb461eea0ff62bd8182e735b`; [real River tests](../internal/hostingjobs/build_test.go), [adapter contract](hosting-build-jobs.md) | BuildID-only transactional queue jobs with optional real handler, bounded retry and lifecycle; now wired through the production public-build handler; dispatch itself does not run Git, Docker or the build engine |
| Managed routing WebUI | Candidate `7c220c7932d452f31a830bf773f291894231c510`, integration `eef72d3b182acdb66f81c24787f2df8bd82abb85`; [browser scenarios](../web/app/tests/browser/hosting-proxy.spec.ts), [real-backend fixture](../web/app/scripts/hosting-proxy-fixture.sh), [user guide](hosting-proxy-ui.md) | Proxy configuration, hostname grants/revokes, exact settled-release routes, CAS/retry, withdrawal/disable and viewer denial; actual HTTP/trusted HTTPS in isolated DNS/test-CA fixtures, not public DNS or production ACME |
| Public HTTPS Git configuration | Integration `904834bb41df350405776ec983529250f86f1e91`, UI conflict-review candidate `77b91e518739f9c7e85aa8f97bc77ae4354324a6`; [PostgreSQL tests](../internal/store/postgres_hosting_git_test.go), [UI tests](../web/app/src/pages/ProjectHosting.test.tsx), [real-backend browser fixture](../web/app/tests/browser/hosting-git-resources.spec.ts), [guide](hosting-git-resources.md) | Public HTTPS defaults to github.com under operator allowlist; atomic 0049 resource/source versions and full-settings CAS review. PostgreSQL required. Saving configuration does not fetch/build/deploy; at that checkpoint private configuration was unsupported; private HTTPS API configuration is now accepted above, source-kind conversion remains unsupported |
| Source credential-window boundary | Integration `f25030f2b79859e456d0ab7840f1cd1764af72c2`; [checkout regressions](../internal/runnerruntime/source_checkout_test.go), [contract](hosting-source-checkout.md) | Credential window governs acquisition, receipt acceptance and confirmed transport-file removal/byte clearing before use. Build use may outlast cleared credentials; parent deadline, cancellation and budgets remain. Final checkout cleanup follows use |
| Nonsecret build phase protocol | Integration `ca5f48956087fe21a3aff6c511af45f4148651a2`; [protocol tests](../internal/hostingbuildprotocol/protocol_test.go), [contract](hosting-build-protocol.md) | Shared structural DTOs and exact receipt/body hashes, now used by reviewed controller/runtime wiring; protocol validation alone supplies no execution authority |
| Durable build journal | Accepted `18c45e820974563ec75af842713b7d9c97e78ce0`; [journal tests](../internal/runner/journal_build_test.go), [contract](hosting-build-journal.md) | Additive v5 build intent/source/result records preserve exact immutable requests and older families; explicit proof-based acknowledgment required. No automatic replay controller or build execution |
| Nonsecret build client | Candidate `652fe3e311dd1fac07aafee21ecd6a13d102136f`, integration `08c1aaf0b6622648fd373a7e65c053bfc8a151eb`; [client tests](../internal/runnerclient/hosting_build_test.go), [contract](hosting-build-client.md) | Typed authenticated transport, strict bounded decoding and exact acknowledgment checks; no automatic retries or credential response transport; accepted nonsecret server endpoints and explicit executor wiring are recorded below, with no default build capability |
| Public build controller/API | Integration `4c1ebeaf05d6e02003bae78e3f9a6ffde56e3e0a`; [application tests](../internal/app/hosting_build_service_test.go), [API tests](../internal/api/hosting_build_handlers_test.go), [controller tests](../internal/store/postgres_hosting_build_controller_test.go), [policy/lifecycle guide](hosting-build-control.md) | Trusted server policy enables public build requests, real transactional River dispatch and nonsecret agent phase endpoints; current authority/placement/source gates and exact task/lease completion; at that checkpoint no private credential endpoint or artifact deployment; current bounded additions are listed above |
| Public Git build WebUI | Accepted `63b6f21b41a095e64dfcb5e26b4be033988aa9d0`; [browser scenarios](../web/app/tests/browser/hosting-builds.spec.ts), [real admission fixture](../web/app/scripts/hosting-builds-fixture.sh), [user guide](hosting-git-resources.md) | Separately requested build, history/detail, exact request retry and revision-bound cancellation at all four widths; browser checks prove real admission/roles/CAS, not seeded build success or artifact deployment; no build-output stream |
| Compiled public build agent | Runtime candidate `2fa37673bc03e9c2d171322b2bda9bd2fdc98847`, integration `a907703572e7e1eb7fb3858998a0cef862645198`, accepted fixture checkpoint `df515f3c5819627c1ee6877b2d22e1f79895f46b`; [real controller/CLI gate](../acceptance/runtime-hosting-build-controller/gate.sh), [runtime fixture](../internal/runnerruntime/hosting_build_controller_fixture_test.go), [operator guide](hosting-build-runtime.md) | Explicit owned agent configuration and persisted capability matching exact server policy; real HTTPS Git/API/PostgreSQL/River/CLI builds Dockerfile, Railpack and prebuilt static into immutable local artifacts, with journal/replay, lease renewal, cancellation and owned cleanup; no artifact-to-deployment handoff |

## Product areas

The C references resolve in the specification's [source list](product-spec.md#13-sources). Source labels identify the reference capability, not proof of NeroCD support.

| Product area | Upstream reference | Status and supported limits at baseline | Required fixtures | Package and deliberate difference |
| --- | --- | --- | --- | --- |
| Server management | C4, C11 | partial: enrolled server/default destination admission, project grants and immutable desired bindings through API/UI; readiness diagnostic API accepted; manual UI enrollment/generated-command agent qualification accepted with the profile limits above; bounded SSH API/UI onboarding accepted for the prepared-host profile; broader SSH/host qualification and full lifecycle planned | A01, A13, A14, A27; server-reenroll, host-key-mismatch, old-agent-capability | P02; persistent Go agent instead of upstream deployment over SSH |
| Resource organization | C1, C10 | partial: shared project environments, image configuration and per-binding public-image mutation admission through API/UI; additional kinds, team ownership, legacy mappings and broader shared-resource concurrency planned | A08, A34, A35; legacy-membership-preservation | P01; preserve legacy IDs and explicit environment grouping |
| Git sources | C2 | partial: public HTTPS build UI/runtime and controlled checkout; private HTTPS configuration/grants/credential endpoint/client accepted; private HTTPS runtime and Dockerfile/Railpack static-provider UI accepted; bounded GitHub push integration accepted; broader provider integrations and complete source choices planned | A03, A09; provider matrix below | P04, P18; no-Git inputs need no artificial repository |
| Builds | C2, C12, C13 | partial: production UI/API/River/controller and explicitly configured agent build public and granted private HTTPS Git into immutable local artifacts; same-host artifact deployment and typed build output accepted; private Railpack static-provider UI accepted; full language matrix, remote delivery and shared cache planned | A02–A07; builder and architecture matrices below | P05; bounded public source-to-local-artifact/deployment evidence does not establish the complete hosting or workspace journey |
| Inputs without Git | C2, C3, C8 | partial: public-image API/UI deployment freezes selected output without a repository; stored-Dockerfile product input, pasted Compose and catalog sources planned | A05, A15, A21; no-git-source | P04, P08, P11 |
| Build placement | C4 | partial: controller dispatches to the exact configured enrolled build agent and fixed daemon; registry publication and dedicated-server image delivery planned | A07, A13; registry-push-pull, architecture-mismatch | P02, P05; tags never relocate stateful destinations |
| Domains and HTTPS | C5 | partial: standalone Traefik TLS engine and authorized whole-generation backend/dispatch; explicitly capable agent execution and managed routing UI accepted with local test-CA fixtures; public DNS/production ACME and additional proxy modes remain unvalidated | A20, A27; proxy matrix below | P07; proxy reconciliation and route ownership have separate generations |
| Deployment automation | C2, C14, C15 | partial: manual image API/UI with autoports, explicit fixed ports, exact request/receipt replay and selected retained cleanup and trusted exact-revision build prerequisite; bounded GitHub build-on-push accepted; verified route-switch engine and guarded controller accepted; bounded read API/operator UI accepted; bounded completed-conflict recovery API accepted; bounded recovery administrator UI accepted; bounded automatic_unprotected deployment accepted; protected approval remains planned | A06, A09–A14; approval-invalidation, retained-release | P04, P06; optional approval must bind prepared release and destination |
| Previews | C6 | planned: derived resources, isolated configuration, routes and storage | A23, A24; late-close-event, quota-exhaustion | P12; hostile forks require isolated pools or rejection |
| Persistent storage | C17 | partial: accepted metadata/admission, explicit storage capability, API/compiled-runner uploaded-file runtime and bounded unqueued-continuity recovery; metadata API/UI accepted; connected deployment-review/recovery UX accepted separately from physical runtime; broader directories/managed files/external files, backups and unknown-writer recovery planned | A19, A26, A37; external-volume-retention, ownership-transfer | P08; resource deletion retains data by default |
| Databases | C7 | partial foundations: metadata/creation UI, private controller/engine helpers and protected credential delivery accepted; direct engine qualification/default-off coordinator accepted; bounded combined API/runner physical workflow and direct deployment UI accepted; application binding, archive backup/restore and broader qualification unfinished; no managed database claim from Compose alone | A17, A18, A26; engine matrix below | P09; replication and an availability SLA are not implied |
| Service catalog | C8 | planned: versioned definitions, stable generated values and explicit upgrades | A21, A22; catalog-version, component-upgrade | P11, P18; initial tested catalog expands through explicit version fixtures |
| Variables and secrets | C16 | partial: runner_file execution plus encrypted project-managed versions, create/rotate/owner reveal/revoke and protected UI; bounded private Git and managed runtime delivery/execution accepted; broader delivery/scopes, recovery workflows and external providers planned | A03, A18, A35; scope-denial, redaction, key-recovery, revoked-version | P03; separate build/runtime/preview grants; external-provider versions explicit |
| Ongoing operations | C9 | partial: run logs, runner evidence, typed build output/viewer and opt-in independent runtime collector; accepted bounded runtime-status dashboard; bounded live runtime logs/viewer are accepted; broader resource actions remain planned | A28, A29, A32; stale-observation, log-reconnect | P13, P15; desired state, operation result and observation remain separate |
| Recovery | C18–C20, C15 | partial: platform backup identity, image mutation/restore holds, bounded readiness-failure restoration and standalone proxy state recovery; trusted restored-ownership resumption and hosted data recovery planned | A25–A27, A37; resume-failure, new-resource-restore | P10; platform, database, storage and release rollback are distinct |
| Automation | C21, C22 | planned: resource-bound schedules and durable notifications | A25, A30, A31; notification matrix below | P14; occurrence identity, DST, overlap and missed-run policies required |
| Programmatic access | C1 | partial: native REST/CLI, generated client, image/secret/placement/retained-cleanup UI and proxy backend API; public source/build API/UI accepted within operator-policy bounds; private-source control API and typed credential client accepted; private HTTPS runtime and bounded UI accepted; complete hosting CLI/MCP planned, managed routing UI accepted within fixture bounds, R65 ongoing | A35, A36; interface-parity, keyboard-mobile | P16; every interface uses the same authorized application operations |
| Migration | C23 | planned: NeroCD upgrade mappings and Coolify compatibility report/cutover | A33, A34; controller-handoff, post-cutover-writes | P17; existing-container adoption requires explicit ownership transfer |
| Broader infrastructure | C1, C4, C5 | planned: multi-server deployment, Swarm, provisioning, Actions runners and log integrations | infra-multi-server, infra-swarm, infra-provisioning, infra-actions-runner, log-drain | P18; explicit extended parity, with no Kubernetes dependency |

## Database engines

The reference backup/restore distinctions below are retained from R44. Every engine remains planned. Common container lifecycle code does not prove engine-specific backup, restore, credential rotation or major-version upgrade support.

| Engine | Status | Reference scheduled backup | Reference dashboard restore | Supported limits and required outcome | Planned fixtures |
| --- | --- | --- | --- | --- | --- |
| PostgreSQL | planned, M2 | Yes | Yes | First stateful slice; generated credentials, application binding, engine-compatible backup and new-resource restore | db-postgresql-lifecycle, db-postgresql-restore, A17, A18, A26 |
| Redis | planned, M2 | No | No | Early hosting; explicitly document persistence and recovery limits | db-redis-lifecycle, db-redis-persistence |
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
| Generic Git and deploy keys | C2 | partial component, P04 | Controlled public HTTPS, callback basic credentials or pinned-host SSH keys; immutable receipts and internal grants exist, public HTTPS configuration and build controller/runtime exist; private HTTPS configuration and credential HTTP/client bridge now accepted; private runtime and Dockerfile UI are now accepted above; git-generic, git-deploy-key |
| Signed manual deploy webhook | C2 | planned, P04 | Authentication, durable delivery identity, retries and supersession; deploy-webhook, A09 |
| Registry image tag/digest | C2 | partial: public-image API/UI preparation and execution, P04/P05/P06 | Anonymous public pull records digest, local image, platform and daemon identity before apply; private registry credentials remain planned; public-image routing uses the separately accepted proxy path; image-tag-digest, A05 |
| Stored Dockerfile | C2 | planned, P04/P05 | Versioned input and context without fake repository; dockerfile-stored |
| Git Compose and pasted Compose | C3 | planned, P08 | Explicit source of truth, controlled interpolation, mixed images/builds, profiles and component identity; A15, A16 |
| Catalog installation | C8 | planned, P11 | Definition version, stable generated credentials, component state; A21, A22 |

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
| Dockerfile | C2 | partial, public/private HTTPS path | Production controller/CLI and bounded UI acquire public or granted private HTTPS Git and runs the confined Dockerfile/context/target path into an immutable artifact. Same-host exact artifact deployment and typed output are accepted; build arguments/secrets and remote publication remain planned; build-dockerfile, A02, A06 |
| Railpack | C12 | partial, public/private CLI and static-provider UI path | Pinned 0.40.1 planner/frontend executes through public or granted private HTTPS Git/controller/CLI with bounded helper and immutable artifact output; upstream Beta label retained. Same-host deployment and typed output are accepted; private CLI credential use is accepted; private static-provider UI qualification is accepted above; full language matrix and remote publication remain planned; build-railpack, A03 |
| Nixpacks | C2 | planned, separate M3 adapter | Never infer support from Railpack; version/language fixtures; build-nixpacks |
| Prebuilt static files | C13 | partial, public build path | Public Git/controller/CLI packages selected prepared assets and opt-in SPA fallback with pinned nginx and retained receipts; same-host artifact deployment is accepted; this row covers prepared assets only; build-static, A04 |
| Compiled static/SPA | C13 | partial, pinned Vite fixture accepted | Source `9b49eb6dac7e3d22b9db81d984ce2da592d27c66`, accepted `d29edb3bc8cde064108891f70713423bb57776a3`; [compiled fixture contract](../acceptance/runtime-hosting-local-build/README.md). Explicit build command/output directory with pinned Node/Bun/Vite/Caddy; parser failure preserves the prior app. Earlier unexplained failures remain a reliability limit; broader frontend coverage, complete A04 and production ACME remain pending; baked configuration requires rebuild; build-spa, A04 |
| Compose build sections | C3 | planned, P05/P08 | Resolve all required images before mutation; initialization and optional-component semantics; build-compose, A15, A16 |
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
at the accepted build candidate cited above.

Compose compatibility includes managed and raw modes, service fields, interpolation required/default behavior, profiles, networks, volumes, operator labels, advanced host capabilities and versioned Docker Compose differential tests. Raw mode remains planned in P18; preflight rejects a protected policy if its release guarantees cannot be satisfied.

## Proxy adapters

| Adapter | Reference | Status and planned slice | Limits and planned fixtures |
| --- | --- | --- | --- |
| Traefik | C5 | partial engine and backend, P07 | Standalone real TLS/renewal/state recovery; control-plane grants, settled binding targets, generation holds and River dispatch accepted. Explicitly capable agent runtime and managed routing UI have real HTTP/trusted-test-CA fixtures; production DNS/ACME and extra certificate/proxy modes remain unvalidated; proxy-traefik, A20, A27 |
| Caddy | C5 | planned, P18 | Explicit configuration and migration adapter; proxy-caddy |
| Custom proxy | C5 | planned, P18 | Declare operator/platform ownership and unavailable managed features; proxy-custom |
| Proxy migration | C5 | planned, P18 | Validate ownership transfer, traffic continuity, certificate retention and recovery; proxy-migration |

Standalone Traefik evidence covers real HTTP-01, trusted TLS renewal and
coordinated state recovery at the accepted proxy candidate cited above.
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
| Email | planned, M2 | Configure credentials, event selection, bounded retry and actionable delivery result | notify-email, A25 |
| Generic webhook | planned, M2 | Scoped endpoint/credential, durable event identity, retry and duplicate handling | notify-webhook, A25 |
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
