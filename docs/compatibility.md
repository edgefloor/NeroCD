# Hosting compatibility ledger

Hosting-first documentation baseline: `65311f30470f34d0d26165d1cd13325fbd05fca0`.
Accepted application/source assessment:
`b55bbcf7b2313e32391a4390dcbb8562de2617b5`, 2026-10-23.
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
| Private HTTPS Git WebUI | Source `513fe8d71b0f8297cceb3a26ab4e077c8eb49088`, accepted `4ba9fcac1b484244cb2c662e53f588c2dce6b71e`; [private browser test](../web/app/tests/browser/private-git-build-deployments.spec.ts), [workflow](hosting-git-resources.md#request-a-private-https-git-build) | Owner creates a new reference grant, recovers an identical retry, selects its ID, builds Dockerfile, reads typed output, deploys exact artifact and verifies test-CA HTTPS; keyboard, 320/390/430/1440px, viewer 403 and no reveal; browser 25.0s/Go 34.03s. Public browser regression 22.2s/Go 30.63s; no automatic-builder private UI qualification or complete A03 |
| Desired runtime configuration | Source `5b435d4e8e35562ab6c9a7116a6b0b5f16e85751`, accepted `bdd5e0168655edd31df48a7818a560a49876147e`; [service](../internal/app/hosting_runtime_config_service.go), [API tests](../internal/api/hosting_runtime_config_handlers_test.go) | Migration 0052; `GET`/`PUT /api/v1/resources/{id}/runtime-configuration` for application image/Git resources; generation compare-and-swap and idempotency; explicit public nonsecret literals and exact managed-secret version references; current owner writes/viewer reads; no decryption, injection or UI |
| Runtime-configuration admission | Source `7c51350083f6fa448e8043e32158ecf9427be6d6`, accepted `1ddfb1f43416ff8109c31a852963942448035583`; [admission persistence](../internal/store/postgres_hosting_runtime_environment.go) | Migration 0053; prepares exact configuration authority for deployment; does not apply values to containers |
| Protected runtime-environment delivery and journal | Delivery source `c7d3a0d5a4fdb00bf9b2ab5e6bd0cdfd05e6b30e`, journal source `804d4f460c6f106993c0a223f36eb42831f6a7f2`, accepted `66b58845dab1fdee6f8b12809f0434a2a94326ac`; [delivery service](../internal/app/hosting_runtime_environment_delivery.go), [endpoint tests](../internal/api/hosting_runtime_environment_handlers_test.go) | `POST /api/v1/runners/hosting/runtime-environment` checks exact prepared authority and current actor/configuration/secret versions, commits metadata audit before decryption and preserves the original bounded window; journal format 8 persists only nonsecret references and delivery ID; older formats remain legacy; runtime consumption was absent at that checkpoint; accepted execution is below |
| Protected runtime execution | Runtime source `420300352ba14f40f082c519f6a75d04f82129c1` and credential repair `b9836b8a626fc12c5a6e4dde44ebc40e80b870a2`, accepted `0a50debdfa5ab14191e52025cd1bb62684e86cd6`; [qualification guide](../acceptance/runtime-hosting-environment/README.md), [gate](../acceptance/runtime-hosting-environment/gate.sh) | Explicit CLI/API opt-in; literals and exact managed-secret versions reach Docker through protected stdin; fixed delivery/replay windows and deadlines; original credential rechecked after authorization commit to prevent post-rotation disclosure; Docker administrators can inspect environment values |
| Bounded runtime status control | Source `56baaa7ec5f155ca490fa7cc989833ed92de9189`, accepted `b55bbcf7b2313e32391a4390dcbb8562de2617b5`; [store](../internal/store/postgres_hosting_runtime_observation.go) | Migration 0054; bounded challenge/report and viewer freshness controls only; no collector, status UI or disk/CPU metrics |

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
The reviewed configuration UI remains unaccepted pending actual browser proof and
integration. Collector implementation and status UI remain pending.

Retained runtime evidence includes the 11.34-second combined gate and 20.691-second
PostgreSQL race check at runtime integration `0a50debdfa5ab14191e52025cd1bb62684e86cd6`.
Status integration `b55bbcf7b2313e32391a4390dcbb8562de2617b5` retains store/API
race checks of 59.679/2.931 seconds and full PostgreSQL store checks of 287.732
seconds. Its contract has 167 routes; sqlc generation compared 36 generated files. The
canonical web artifact manifest remains
`2031889de69af8ec37d3999505414fa99e375b9a0e6b72a74c7d014e5d50200e`.
These checks establish the named boundaries, not continuous collection or browser
qualification of runtime configuration.

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
| Server management | C4, C11 | partial: enrolled server/default destination admission, project grants and immutable desired bindings through API/UI; SSH automation, onboarding-time host capability/readiness validation and full lifecycle planned | A01, A13, A14, A27; server-reenroll, host-key-mismatch, old-agent-capability | P02; persistent Go agent instead of upstream deployment over SSH |
| Resource organization | C1, C10 | partial: shared project environments, image configuration and per-binding public-image mutation admission through API/UI; additional kinds, team ownership, legacy mappings and broader shared-resource concurrency planned | A08, A34, A35; legacy-membership-preservation | P01; preserve legacy IDs and explicit environment grouping |
| Git sources | C2 | partial: public HTTPS build UI/runtime and controlled checkout; private HTTPS configuration/grants/credential endpoint/client accepted; private HTTPS runtime and Dockerfile UI accepted; provider integrations and complete source choices planned | A03, A09; provider matrix below | P04, P18; no-Git inputs need no artificial repository |
| Builds | C2, C12, C13 | partial: production UI/API/River/controller and explicitly configured agent build public and granted private HTTPS Git into immutable local artifacts; same-host artifact deployment and typed build output accepted; private automatic-builder UI qualification, remote delivery and shared cache planned | A02–A07; builder and architecture matrices below | P05; bounded public source-to-local-artifact/deployment evidence does not establish the complete hosting or workspace journey |
| Inputs without Git | C2, C3, C8 | partial: public-image API/UI deployment freezes selected output without a repository; stored-Dockerfile product input, pasted Compose and catalog sources planned | A05, A15, A21; no-git-source | P04, P08, P11 |
| Build placement | C4 | partial: controller dispatches to the exact configured enrolled build agent and fixed daemon; registry publication and dedicated-server image delivery planned | A07, A13; registry-push-pull, architecture-mismatch | P02, P05; tags never relocate stateful destinations |
| Domains and HTTPS | C5 | partial: standalone Traefik TLS engine and authorized whole-generation backend/dispatch; explicitly capable agent execution and managed routing UI accepted with local test-CA fixtures; public DNS/production ACME and additional proxy modes remain unvalidated | A20, A27; proxy matrix below | P07; proxy reconciliation and route ownership have separate generations |
| Deployment automation | C2, C14, C15 | partial: manual image API/UI with autoports, explicit fixed ports, exact request/receipt replay and selected retained cleanup; automatic public traffic switching, push triggers and hosting approval planned | A06, A09–A14; approval-invalidation, retained-release | P04, P06; optional approval must bind prepared release and destination |
| Previews | C6 | planned: derived resources, isolated configuration, routes and storage | A23, A24; late-close-event, quota-exhaustion | P12; hostile forks require isolated pools or rejection |
| Persistent storage | C17 | planned: managed volumes, directories, managed files and external host files | A19, A26, A37; external-volume-retention, ownership-transfer | P08; resource deletion retains data by default |
| Databases | C7 | planned: engine-specific lifecycle; no managed database claim from Compose alone | A17, A18, A26; engine matrix below | P09; replication and an availability SLA are not implied |
| Service catalog | C8 | planned: versioned definitions, stable generated values and explicit upgrades | A21, A22; catalog-version, component-upgrade | P11, P18; initial tested catalog expands through explicit version fixtures |
| Variables and secrets | C16 | partial: runner_file execution plus encrypted project-managed versions, create/rotate/owner reveal/revoke and protected UI; managed execution delivery, broader scopes, recovery workflows and external providers planned | A03, A18, A35; scope-denial, redaction, key-recovery, revoked-version | P03; separate build/runtime/preview grants; external-provider versions explicit |
| Ongoing operations | C9 | partial: run logs, runner evidence and durable typed build output/viewer; continuous application-runtime observation/logs and broader resource actions planned | A28, A29, A32; stale-observation, log-reconnect | P13, P15; desired state, operation result and observation remain separate |
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
| GitHub | C2, C6 | planned, first provider in P04 | Public/private Git, installation credentials, branch/commit selection, signed webhook receipts, duplicate/delayed events, PR status and cleanup; git-github, A03, A09, A23 |
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
| Railpack | C12 | partial, public/private CLI path | Pinned 0.40.1 planner/frontend executes through public or granted private HTTPS Git/controller/CLI with bounded helper and immutable artifact output; upstream Beta label retained. Same-host deployment and typed output are accepted; private CLI credential use is accepted; private automatic-builder UI qualification, full language matrix and remote publication remain planned; build-railpack, A03 |
| Nixpacks | C2 | planned, separate M3 adapter | Never infer support from Railpack; version/language fixtures; build-nixpacks |
| Prebuilt static files | C13 | partial, public build path | Public Git/controller/CLI packages selected prepared assets and opt-in SPA fallback with pinned nginx and retained receipts; same-host artifact deployment is accepted; frontend compilation remains planned; build-static, A04 |
| Compiled static/SPA | C13 | planned, M1 | Explicit build command and output directory; baked configuration requires rebuild; build-spa, A04 |
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
