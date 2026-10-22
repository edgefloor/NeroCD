# Compatibility ledger

Originally recorded on 2026-10-22 against NeroCD `a763a90b3896257b784c9498d01650ccdad2427a` and [Coolify `b12d1386f3d458618b4c579babcd1e339c6c40b3`](https://github.com/coollabsio/coolify/tree/b12d1386f3d458618b4c579babcd1e339c6c40b3). Current accepted NeroCD assessment: `08c1aaf0b6622648fd373a7e65c053bfc8a151eb`, dated 2026-10-22. Official documentation is a separately dated capability reference. It can change independently of the pinned source.

The [full specification](product-spec.md) defines the target. The [plan](plan.md) assigns P00–P18 and M0–M4. This ledger distinguishes feature compatibility, migration compatibility, and unchanged-client compatibility. Success in one dimension does not prove another.

## Status and evidence

- `planned`: required target with no validated implementation claim.
- `partial`: existing foundations cover only the stated limits.
- `implemented`: code exists for the declared boundary, without complete acceptance evidence.
- `validated`: declared fixtures pass on a named release candidate with bounded evidence.
- `intentionally different`: an explicit behavior difference remains part of the supported contract.

Short fixture identifiers in the product and adapter matrices below remain
planned acceptance fixtures. They are not existing test files or passing
results. The accepted-foundation table separately links real source tests and
their implementation commits. This documentation update runs no application
tests and makes no new runtime pass claim. Future validation must name candidate
SHA, tool and engine versions, architectures, result, evidence location and
limits. Missing and failed required checks block acceptance.

## Accepted foundations and evidence

This assessment covers accepted code through
`08c1aaf0b6622648fd373a7e65c053bfc8a151eb`. The linked tests describe bounded
coverage. They do not establish full product-area or milestone completion.
The [destination guide](hosting-destinations.md), [secret guide](secrets.md) and
[job infrastructure guide](hosting-jobs.md) describe the operational contracts.

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
| Source/build persistence | Store candidate `273987babc404d163063419169e636a65230b93d`, accepted schema repair `5f6a2a0d3087f0bf531a65628009db68280e069d`, integration `836a3a92b0ff32242bba94bf8cf0800dc638d96e`; [PostgreSQL/fence/schema/restore tests](../internal/store/postgres_hosting_build_test.go), [domain tests](../internal/domain/hosting_build_test.go), [guide](hosting-build-control.md) | Persisted immutable sources/settings, purpose-specific secret-version grants, requests, fenced receipts and local artifacts via internal repository methods; no production build API/consumer, typed runtime executor, receipt bridge or deployment handoff |
| Proxy control-plane dispatch | Candidate `471d063f5062c4214f531b8ee9a41658e1c2c48c`, integration `ad82174f01da8ac964adb20cda57039484af8189`; authority repair `d7d65911be23658315596b0c392be481a9fec995`, integration `12bbbd4be048a418217cfaf1b69637307979fd98`; [API tests](../internal/api/hosting_proxy_handlers_test.go), [persisted authority tests](../internal/store/postgres_hosting_proxy_authorization_test.go), [guide](hosting-proxy-control.md) | Authorized grants/routes/generations, settled binding-derived targets, proxy/restore holds and exact-target River dispatch; backend tests use typed engine receipts. Explicitly enrolled proxy-capable Linux agents now execute this protocol, with the separate runtime/UI evidence below; image-only grants remain unchanged |
| Private Git credential delivery component | Candidate `88e7ec8767baa6cc534bda015ed5336b5687b535`, integration `12087174f7747f51f6d30f8314ef86c4183cb53d`; [application tests](../internal/app/hosting_build_delivery_service_test.go), [PostgreSQL delivery tests](../internal/store/postgres_hosting_build_delivery_test.go), [contract](hosting-build-credentials.md) | Exact live sourcing authority, immutable secret version, audit-backed delivery window and owned bytes; no credential endpoint, production controller or runtime bridge |
| Fenced proxy agent runtime | Repaired candidate `2037ad55912be8950e2b81e8e31847a98d5ff048`, integration `332b03942d7ada1cf094468620dfd069eadefcf1`; [combined gate](../acceptance/runtime-proxy-control/gate.sh), [agent integration tests](../internal/runnerruntime/hosting_proxy_integration_test.go), [runtime contract](hosting-proxy-control.md) | Explicit persisted `proxy_apply` enrollment and Linux configuration, journal/replay and lost-response recovery, HTTP-01/trusted-test-CA TLS and autonomous renewal during control-plane outage; unresolved and restored holds remain, with no override |
| Optional BuildID queue adapter | Candidate `13ec1392681721ffce1f9f54e7f5f690268ab534`, integration `80e17670d8e5be3bcb461eea0ff62bd8182e735b`; [real River tests](../internal/hostingjobs/build_test.go), [adapter contract](hosting-build-jobs.md) | BuildID-only transactional queue jobs with optional real handler, bounded retry and lifecycle; not wired to a production build controller and does not run Git, Docker or the build engine |
| Managed routing WebUI | Candidate `7c220c7932d452f31a830bf773f291894231c510`, integration `eef72d3b182acdb66f81c24787f2df8bd82abb85`; [browser scenarios](../web/app/tests/browser/hosting-proxy.spec.ts), [real-backend fixture](../web/app/scripts/hosting-proxy-fixture.sh), [user guide](hosting-proxy-ui.md) | Proxy configuration, hostname grants/revokes, exact settled-release routes, CAS/retry, withdrawal/disable and viewer denial; actual HTTP/trusted HTTPS in isolated DNS/test-CA fixtures, not public DNS or production ACME |
| Public HTTPS Git configuration | Integration `904834bb41df350405776ec983529250f86f1e91`, UI conflict-review candidate `77b91e518739f9c7e85aa8f97bc77ae4354324a6`; [PostgreSQL tests](../internal/store/postgres_hosting_git_test.go), [UI tests](../web/app/src/pages/ProjectHosting.test.tsx), [real-backend browser fixture](../web/app/tests/browser/hosting-git-resources.spec.ts), [guide](hosting-git-resources.md) | Public HTTPS defaults to github.com under operator allowlist; atomic 0049 resource/source versions and full-settings CAS review. PostgreSQL required. Saving configuration does not fetch/build/deploy; private configuration and source-kind conversion unsupported |
| Source credential-window boundary | Integration `f25030f2b79859e456d0ab7840f1cd1764af72c2`; [checkout regressions](../internal/runnerruntime/source_checkout_test.go), [contract](hosting-source-checkout.md) | Credential window governs acquisition, receipt acceptance and confirmed transport-file removal/byte clearing before use. Build use may outlast cleared credentials; parent deadline, cancellation and budgets remain. Final checkout cleanup follows use |
| Nonsecret build phase protocol | Integration `ca5f48956087fe21a3aff6c511af45f4148651a2`; [protocol tests](../internal/hostingbuildprotocol/protocol_test.go), [contract](hosting-build-protocol.md) | Shared structural DTOs and exact receipt/body hashes; no HTTP endpoints, execution authority, advertised capability or executor |
| Durable build journal | Accepted `18c45e820974563ec75af842713b7d9c97e78ce0`; [journal tests](../internal/runner/journal_build_test.go), [contract](hosting-build-journal.md) | Additive v5 build intent/source/result records preserve exact immutable requests and older families; explicit proof-based acknowledgment required. No automatic replay controller or build execution |
| Nonsecret build client | Candidate `652fe3e311dd1fac07aafee21ecd6a13d102136f`, integration `08c1aaf0b6622648fd373a7e65c053bfc8a151eb`; [client tests](../internal/runnerclient/hosting_build_test.go), [contract](hosting-build-client.md) | Typed authenticated transport, strict bounded decoding and exact acknowledgment checks; no automatic retries, credential response transport, server endpoint registration, default capability or executor |

At `904834bb41df350405776ec983529250f86f1e91`, accepted PostgreSQL and actual
backend browser evidence covers public Git creation/update, atomic source
references, immutable reads, authorization, CAS and complete current-settings
review at 320, 390, 430 and 1440 pixels. It does not establish fetching, private
source configuration, build execution, artifact handoff or a Git-to-HTTPS
journey. Source settings are configuration, not proof of source ownership or
built content.

The accepted source-window repair `f25030f2` permits successful use after
credential expiry only after transport cleanup and byte clearing are confirmed.
Expiry during acquisition or receipt acceptance still prevents use. The
protocol, journal and client evidence is component-level and revision-specific;
no production build endpoint, controller or executor is installed at
`08c1aaf0b6622648fd373a7e65c053bfc8a151eb`. No application tests were rerun
for this documentation update.

At `eef72d3b182acdb66f81c24787f2df8bd82abb85`, the retained acceptance report
records 593 frontend tests in 61 files and three of three real-backend Chromium
cases, no skips, across widths 320, 390, 430 and 1440. The cases verify actual
HTTP and certificate-verified HTTPS, grants/revokes, withdrawal/disable,
viewer 403, CAS conflicts and identical-request recovery. The accepted generated
asset manifest is
`f81fb43828a841933afcc6dc6983c34e342d97a2fb17255e5bc49a58f64dde19`;
Go build and 145-route embedding verification followed asset generation.

The retained `332b03942d7ada1cf094468620dfd069eadefcf1` report covers the merged
Linux runtime gate with real HTTP-01, trusted fixture CA, renewal while the
control plane is unavailable, response loss, holds and capability filtering.
PostgreSQL store race and serial restore checks passed on that integration.
The earlier checkpoint cleanup failure is not a pass. The accepted runtime
report is tied to the repaired merged candidate and verified owned fixture
cleanup. Production builds do not expose the fixture CA injection.

The retained `12087174f7747f51f6d30f8314ef86c4183cb53d` report includes actual
PostgreSQL store/application race and schema-restore checks for private source
delivery. The retained `80e17670d8e5be3bcb461eea0ff62bd8182e735b` report includes
actual PostgreSQL River-job and application checks for the optional adapter.
All four new capabilities passed their required independent review lenses at
their accepted candidates. These reports are source-revision evidence, not
application tests rerun by this documentation worker or performance benchmarks.

The earlier accepted integration report at
`836a3a92b0ff32242bba94bf8cf0800dc638d96e` records passed PostgreSQL-backed
race suites for store, domain, app, API, runner, runnerclient and hostingjobs,
plus the real CLI dump/restore regression. It also records changed-code lint
with zero findings, generation checks, vet, build and Go embedding of 145 routes.
The [source/build schema tests](../internal/store/postgres_hosting_build_test.go)
cover owning-schema functions and restore/replay; [proxy authority tests](../internal/store/postgres_hosting_proxy_authorization_test.go)
cover persisted legacy-token authorization, revocation and expiry after waits.
These are retained acceptance reports, not commands rerun for this documentation
update or runtime throughput measurements.

The persistence merge alone did not pass combined checks. Accepted evidence is
the repaired `836a3a92` integration: additive 0048 binds 0047's functions to
their owning schema and the regression fixtures account for build dependencies.
Original migration SQL/checksums remain unchanged. Internal fixture queue/task
callbacks are not a product build consumer.

The earlier image-port/cleanup UI report remains associated with
`1582114b6330341153bdce03c7f44f7d2961c7ff` and integration
`e1eaf13aaaf3749c460f2c7ccfb3ff5c033d4a01`. Current routing UI acceptance uses
the newer merged `eef72d3b` report above. Results and counts from these
revisions are not added together.

The execution [guide](hosting-execution.md) documents the accepted
`make image-hosting-gate` coverage at integration
`9822119a10d95c3108f4018bfaf8f516b0c88ca8`: real public-image preparation,
tag freeze, ordinary HTTP, failed pull preservation, post-apply readiness
restoration, preparation-response loss, control-plane restart and lease loss.
The [deployment guide](hosting-image-deployments.md) defines the UI's role and
request-recovery contract. At `69735f6ca34e68147b00cae4afbfe135b2b5fa1b`, the
accepted UI handoff reports 552 unit tests in 58 files and real-backend Chromium
coverage at widths 320, 390, 430 and 1440. These are revision-specific retained
reports, not application tests rerun by this documentation worker.

The [build guide](hosting-build-engine.md) and source runtime gate describe the
accepted `e72a504863c4001d5e9c0e192c40aca971f05f93` evidence: real
Dockerfile/Railpack/static builds and HTTP content, observed descendant CPU
throttling and ancestor OOM events, expanded-output rejection, cancellation,
owned cleanup and independent workload survival. This is enforcement and
failure-path evidence, not a performance claim or a hostile-code sandbox.
The engine requires Linux cgroup v2 and a fixed rootful daemon; its runtime
fixture uses Docker 29.4.0 with the containerd image store.

The [proxy guide](hosting-proxy-engine.md) and runtime gate describe the accepted
`ea8bdc547f1ecda4d95739697eeb33efbdea942d` evidence: real Traefik/Pebble
HTTP-01 issuance, trusted TLS, renewal, generation/ownership failures, rollback
and coordinated state restoration with the CA offline. This does not validate
the separate control-plane checkpoint's runtime execution or end-user HTTPS UI. DNS-01, wildcard and custom
certificate support remain outside this component.

Retained validation reports supplied with the accepted handoffs are
revision-specific. At `45d7fa9a0bd20fb3733ac97c0bc93966c803dc4a`, the accepted
WebUI report records 522 unit tests in 56 files and four Chromium browser cases
covering widths 320, 390, 430 and 1440. At
`751b41c4c7594fbc678f906583495325ae2be84c`, the secret-UI handoff records six
browser cases and 501 unit tests. The destination repair at
`169db49909035924b5ba8f71b0b90c0a807a5d64` records passed PostgreSQL migration
0044 and race checks. These are retained handoff results, not reruns by this
documentation worker; the linked source tests are durable descriptions of
coverage, not archived execution logs.

The repository uses the Go 1.26.10 compiler with Go 1.25 language support.
River's pinned queue and migration contract is documented in the
[hosting job guide](hosting-jobs.md). No evidence above completes A01, A05,
A08 or A27. A36 requires the complete declared core workflows on one release
candidate; results from different commits cannot be added together to satisfy it.
The public-image path, adapters, internal persistence and proxy backend above
are accepted only within their stated bounds. Private Git delivery, optional
BuildID dispatch, proxy agent runtime and managed routing UI are accepted
components at their cited revisions. Public HTTPS Git configuration and
nonsecret protocol/journal/client infrastructure are now accepted as recorded
above. Private Git configuration and controller/executor work outside this base
remain unaccepted. There is no A01–A37 or M0–M4 completion claim, and R65 remains
ongoing. Build execution APIs and production orchestration, private configuration,
the credential endpoint and
runtime bridge, artifact handoff, public DNS/production ACME verification,
managed runtime-secret delivery and continuous observations/runtime
logs, automatic triggers, optional hosting approval and trusted restore
resumption remain required for the full journey.

## Product areas

The C references resolve in the specification's [source list](product-spec.md#13-sources). Source labels identify the reference capability, not proof of NeroCD support.

| Product area | Upstream reference | Status and supported limits at baseline | Required fixtures | Package and deliberate difference |
| --- | --- | --- | --- | --- |
| Server management | C4, C11 | partial: enrolled server/default destination admission, project grants and immutable desired bindings through API/UI; SSH automation, actual host/runtime validation and full lifecycle planned | A01, A13, A14, A27; server-reenroll, host-key-mismatch, old-agent-capability | P02; persistent Go agent instead of upstream deployment over SSH |
| Resource organization | C1, C10 | partial: shared project environments, image configuration and per-binding public-image mutation admission through API/UI; additional kinds, team ownership, legacy mappings and broader shared-resource concurrency planned | A08, A34, A35; legacy-membership-preservation | P01; preserve legacy IDs and explicit environment grouping |
| Git sources | C2 | partial: public HTTPS Git configuration API/UI with immutable source references and controlled checkout; private configuration, credential endpoint/runtime bridge, provider integration and complete source choices planned | A03, A09; provider matrix below | P04, P18; no-Git inputs need no artificial repository |
| Builds | C2, C12, C13 | partial: confined local build engine plus internal requests/receipts/artifacts; build API/controller/consumer/executor, log streaming and deployment handoff planned | A02–A07; builder and architecture matrices below | P05; immutable local receipts do not establish an integrated build/release workflow |
| Inputs without Git | C2, C3, C8 | partial: public-image API/UI deployment freezes selected output without a repository; stored-Dockerfile product input, pasted Compose and catalog sources planned | A05, A15, A21; no-git-source | P04, P08, P11 |
| Build placement | C4 | partial: standalone build on one fixed local daemon; scheduling, registry publication and dedicated-server image delivery planned | A07, A13; registry-push-pull, architecture-mismatch | P02, P05; tags never relocate stateful destinations |
| Domains and HTTPS | C5 | partial: standalone Traefik TLS engine and authorized whole-generation backend/dispatch; explicitly capable agent execution and managed routing UI accepted with local test-CA fixtures; public DNS/production ACME and additional proxy modes remain unvalidated | A20, A27; proxy matrix below | P07; proxy reconciliation and route ownership have separate generations |
| Deployment automation | C2, C14, C15 | partial: manual image API/UI with autoports, explicit fixed ports, exact request/receipt replay and selected retained cleanup; public traffic switch, push triggers and optional hosting approval planned | A06, A09–A14; approval-invalidation, retained-release | P04, P06; optional approval must bind prepared release and destination |
| Previews | C6 | planned: derived resources, isolated configuration, routes and storage | A23, A24; late-close-event, quota-exhaustion | P12; hostile forks require isolated pools or rejection |
| Persistent storage | C17 | planned: managed volumes, directories, managed files and external host files | A19, A26, A37; external-volume-retention, ownership-transfer | P08; resource deletion retains data by default |
| Databases | C7 | planned: engine-specific lifecycle; no managed database claim from Compose alone | A17, A18, A26; engine matrix below | P09; replication and an availability SLA are not implied |
| Service catalog | C8 | planned: versioned definitions, stable generated values and explicit upgrades | A21, A22; catalog-version, component-upgrade | P11, P18; initial tested catalog expands through explicit version fixtures |
| Variables and secrets | C16 | partial: runner_file execution plus encrypted project-managed versions, create/rotate/owner reveal/revoke and protected UI; managed execution delivery, broader scopes, recovery workflows and external providers planned | A03, A18, A35; scope-denial, redaction, key-recovery, revoked-version | P03; separate build/runtime/preview grants; external-provider versions explicit |
| Ongoing operations | C9 | partial: run logs and runner evidence; continuous runtime observation and resource actions planned | A28, A29, A32; stale-observation, log-reconnect | P13, P15; desired state, operation result and observation remain separate |
| Recovery | C18–C20, C15 | partial: platform backup identity, image mutation/restore holds, bounded readiness-failure restoration and standalone proxy state recovery; trusted restored-ownership resumption and hosted data recovery planned | A25–A27, A37; resume-failure, new-resource-restore | P10; platform, database, storage and release rollback are distinct |
| Automation | C21, C22 | planned: resource-bound schedules and durable notifications | A25, A30, A31; notification matrix below | P14; occurrence identity, DST, overlap and missed-run policies required |
| Programmatic access | C1 | partial: native REST/CLI, generated client, image/secret/placement/retained-cleanup UI and proxy backend API; source/build APIs and complete hosting CLI/MCP planned, managed routing UI accepted within fixture bounds, R65 ongoing | A35, A36; interface-parity, keyboard-mobile | P16; every interface uses the same authorized application operations |
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
| Generic Git and deploy keys | C2 | partial component, P04 | Controlled public HTTPS, callback basic credentials or pinned-host SSH keys; immutable receipts and internal grants exist, public HTTPS configuration exists, but private configuration, delivery endpoint/runtime bridge and controller are absent; git-generic, git-deploy-key |
| Signed manual deploy webhook | C2 | planned, P04 | Authentication, durable delivery identity, retries and supersession; deploy-webhook, A09 |
| Registry image tag/digest | C2 | partial: public-image API/UI preparation and execution, P04/P05/P06 | Anonymous public pull records digest, local image, platform and daemon identity before apply; private credentials and public routing remain planned; image-tag-digest, A05 |
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
| Dockerfile | C2 | partial, standalone P05 engine | Caller supplies immutable checkout; confined relative context/Dockerfile and named target settings bind receipts. No build arguments/secrets or integrated API/Git/deploy handoff; build-dockerfile, A02, A06 |
| Railpack | C12 | partial, standalone P05 engine | Pinned 0.40.1 planner/frontend with bounded helper and no secrets; upstream Beta label retained. Product source/settings/credential workflow and language matrix remain planned; build-railpack, A03 |
| Nixpacks | C2 | planned, separate M3 adapter | Never infer support from Railpack; version/language fixtures; build-nixpacks |
| Prebuilt static files | C13 | partial, standalone P05 engine | Selected sanitized publish directory and opt-in SPA fallback packaged with pinned nginx, legacy receipt replay preserved; no compilation or integrated product/deployment handoff; build-static, A04 |
| Compiled static/SPA | C13 | planned, M1 | Explicit build command and output directory; baked configuration requires rebuild; build-spa, A04 |
| Compose build sections | C3 | planned, P05/P08 | Resolve all required images before mutation; initialization and optional-component semantics; build-compose, A15, A16 |
| Dedicated builder | C4 | planned, M3 | Authorized registry delivery, cache identity, architecture/pull validation; build-remote, A07 |

The [build-engine toolchain table](hosting-build-engine.md#host-and-toolchain-contract)
pins the accepted component and executable identities. The engine accepts one
Linux platform on its fixed native daemon; package receipts and the real fixture
cover that bounded local output. This does not claim a complete linux/amd64 and
linux/arm64 build-to-destination matrix or cross-host transfer. Each product
strategy still needs explicit architecture/version fixtures, credentials,
cache/retention controls and integrated build-to-deploy evidence. Disk mode is
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
coverage; this does not validate public DNS or production ACME. Source-build
handoff, continuous monitoring and additional certificate/proxy modes remain required.
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
