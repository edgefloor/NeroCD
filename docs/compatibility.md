# Compatibility ledger

Originally recorded on 2026-10-22 against NeroCD `a763a90b3896257b784c9498d01650ccdad2427a` and [Coolify `b12d1386f3d458618b4c579babcd1e339c6c40b3`](https://github.com/coollabsio/coolify/tree/b12d1386f3d458618b4c579babcd1e339c6c40b3). Current accepted NeroCD assessment: `45d7fa9a0bd20fb3733ac97c0bc93966c803dc4a`, dated 2026-10-22. Official documentation is a separately dated capability reference. It can change independently of the pinned source.

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
`45d7fa9a0bd20fb3733ac97c0bc93966c803dc4a`. The linked tests describe bounded
coverage. They do not establish full product-area or milestone completion.
The [destination guide](hosting-destinations.md), [secret guide](secrets.md) and
[job infrastructure guide](hosting-jobs.md) describe the operational contracts.

| Foundation | Accepted commits and durable source evidence | Boundaries |
| --- | --- | --- |
| Shared environments and image configuration | Backend `6940a8d2385e9ab63f63e40dc1068305a50da7c3`, UI `aa7e59b8422bff5bd640548fef2b02947c6e9bda`; [application tests](../internal/app/hosting_service_test.go), [API tests](../internal/api/hosting_handlers_test.go), [UI tests](../web/app/src/pages/ProjectHosting.test.tsx) | Persisted project environments and generation-checked image settings; image syntax validation does not resolve a registry tag, run an image or prove independent deployments |
| Managed secret versions and protected UI | Backend integration `3d6583e1fff9de3e5bc05fa9097034f3d915fcd8`, UI integration `f08b790fbed77740d7e59ea4b05fbbcf2e964e05`; [lifecycle/fail-closed tests](../internal/app/managed_secrets_service_test.go), [UI tests](../web/app/src/pages/ProjectSecrets.test.tsx), [browser tests](../web/app/tests/browser/managed-secrets.spec.ts) | Encrypted project-owned immutable versions, audited owner reveal, revocation and strict keyring file; no managed-secret execution injection, team/environment value inheritance or external-provider lookup |
| Servers, destinations and bindings | API integration `02a6514b2ce0a9f2ae50dfc2e5ec7ac8ef2410f8`, canonical-capability repair `169db49909035924b5ba8f71b0b90c0a807a5d64`, UI `72bc485e54867df0785e7f6052b4aa8c6e2a2e8a`, browser repair `45d7fa9a0bd20fb3733ac97c0bc93966c803dc4a`; [application tests](../internal/app/destinations_service_test.go), [PostgreSQL migration/constraint tests](../internal/store/postgres_destinations_test.go), [browser tests](../web/app/tests/browser/hosting-destinations.spec.ts) | Admission verifies a consumed enrollment and eligible credentialed runner, then creates a stable default destination; project grants and immutable bindings record desired placement. Browser onboarding executes the real manual enrollment CLI. No SSH automation, Docker capability/runtime validation or resource deployment |
| Authoritative runner restart identity | Integration `9025ee814dd5de354a7465e39d59f3f90014d2b3`; [identity API tests](../internal/api/runner_identity_handlers_test.go) | Dedicated runner credential resolves authoritative identity before journal startup; the identity endpoint does not heartbeat or validate host runtime readiness |
| Transactional hosting job infrastructure | Integration `037b621b2647bd9cb5dec234330239ed6373c0ae`, implementation `c59a43e6fd174c3ae546b5ca3fef8b15f633247d`; [transaction/retry/lifecycle tests](../internal/hostingjobs/jobs_test.go), [migration checksum tests](../internal/hostingjobs/migrations_test.go) | River OSS v0.44.1 transactional enqueue and bounded adapter behavior, owner migrations and backup identity; no production consumer registered or started, hosting runtime queue readiness or restore admission hold |

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
Image execution, hosting deployment UI and proxy-engine candidates outside this
accepted assessment remain unaccepted.

## Product areas

The C references resolve in the specification's [source list](product-spec.md#13-sources). Source labels identify the reference capability, not proof of NeroCD support.

| Product area | Upstream reference | Status and supported limits at baseline | Required fixtures | Package and deliberate difference |
| --- | --- | --- | --- | --- |
| Server management | C4, C11 | partial: enrolled server/default destination admission, project grants and immutable desired bindings through API/UI; SSH automation, actual host/runtime validation and full lifecycle planned | A01, A13, A14, A27; server-reenroll, host-key-mismatch, old-agent-capability | P02; persistent Go agent instead of upstream deployment over SSH |
| Resource organization | C1, C10 | partial: shared project environments and image-config resources through API/UI; additional kinds, team ownership, legacy mappings and runtime concurrency planned | A08, A34, A35; legacy-membership-preservation | P01; preserve legacy IDs and explicit environment grouping |
| Git sources | C2 | partial: repository inputs exist; provider installation and complete source choices planned | A03, A09; provider matrix below | P04, P18; no-Git inputs need no artificial repository |
| Builds | C2, C12, C13 | planned: prebuilt Compose execution does not validate builders | A02–A07; builder and architecture matrices below | P05; immutable image outputs retained independently of deployment retries |
| Inputs without Git | C2, C3, C8 | partial: image reference settings without a repository; digest resolution/execution, stored Dockerfiles, pasted Compose and catalog sources planned | A05, A15, A21; no-git-source | P04, P08, P11 |
| Build placement | C4 | planned: same-host and dedicated builders with explicit image delivery | A07, A13; registry-push-pull, architecture-mismatch | P02, P05; tags never relocate stateful destinations |
| Domains and HTTPS | C5 | planned: managed routes, domains, certificates and independent host traffic | A20, A27; proxy matrix below | P07; proxy reconciliation and route ownership have separate generations |
| Deployment automation | C2, C14, C15 | partial: approvals and durable deployment evidence; trusted automatic hosting releases planned | A06, A09–A14; approval-invalidation, retained-release | P04, P06; optional approval binds prepared release and destination |
| Previews | C6 | planned: derived resources, isolated configuration, routes and storage | A23, A24; late-close-event, quota-exhaustion | P12; hostile forks require isolated pools or rejection |
| Persistent storage | C17 | planned: managed volumes, directories, managed files and external host files | A19, A26, A37; external-volume-retention, ownership-transfer | P08; resource deletion retains data by default |
| Databases | C7 | planned: engine-specific lifecycle; no managed database claim from Compose alone | A17, A18, A26; engine matrix below | P09; replication and an availability SLA are not implied |
| Service catalog | C8 | planned: versioned definitions, stable generated values and explicit upgrades | A21, A22; catalog-version, component-upgrade | P11, P18; initial tested catalog expands through explicit version fixtures |
| Variables and secrets | C16 | partial: runner_file execution plus encrypted project-managed versions, create/rotate/owner reveal/revoke and protected UI; managed execution delivery, broader scopes, recovery workflows and external providers planned | A03, A18, A35; scope-denial, redaction, key-recovery, revoked-version | P03; separate build/runtime/preview grants; external-provider versions explicit |
| Ongoing operations | C9 | partial: run logs and runner evidence; continuous runtime observation and resource actions planned | A28, A29, A32; stale-observation, log-reconnect | P13, P15; desired state, operation result and observation remain separate |
| Recovery | C18–C20, C15 | partial: platform backup/restore and linked rollback foundations; hosted data recovery planned | A25–A27, A37; resume-failure, new-resource-restore | P10; platform, database, storage and release rollback are distinct |
| Automation | C21, C22 | planned: resource-bound schedules and durable notifications | A25, A30, A31; notification matrix below | P14; occurrence identity, DST, overlap and missed-run policies required |
| Programmatic access | C1 | partial: native REST/CLI, generated client and hosting configuration/secret/placement API/UI; runtime hosting operations and MCP planned | A35, A36; interface-parity, keyboard-mobile | P16; every interface uses the same authorized application operations |
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
| Generic Git and deploy keys | C2 | planned, P04 | Connection test, selected branch/commit, operation-scoped credential; git-generic, git-deploy-key |
| Signed manual deploy webhook | C2 | planned, P04 | Authentication, durable delivery identity, retries and supersession; deploy-webhook, A09 |
| Registry image tag/digest | C2 | partial: saved image configuration; runtime path planned, P04/P05 | Explicit tag/digest syntax accepted without registry contact; digest resolution, architecture and destination pull checks remain planned; image-tag-digest, A05 |
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
| Dockerfile | C2 | planned, first P05 path | Context, target, monorepo base, command overrides, secret grants, pinned BuildKit/Buildx; build-dockerfile, A02, A06 |
| Railpack | C12 | planned, first automatic builder | Upstream Beta label retained; pin tool version and supported languages; build-railpack, A03 |
| Nixpacks | C2 | planned, separate M3 adapter | Never infer support from Railpack; version/language fixtures; build-nixpacks |
| Prebuilt static files | C13 | planned, M1 | Package existing assets; publish path, web-server configuration, SPA fallback; build-static, A04 |
| Compiled static/SPA | C13 | planned, M1 | Explicit build command and output directory; baked configuration requires rebuild; build-spa, A04 |
| Compose build sections | C3 | planned, P05/P08 | Resolve all required images before mutation; initialization and optional-component semantics; build-compose, A15, A16 |
| Dedicated builder | C4 | planned, M3 | Authorized registry delivery, cache identity, architecture/pull validation; build-remote, A07 |

Each strategy requires separate supported CPU architecture and tool-version rows before validation. Initial CPU fixtures are planned for linux/amd64 and linux/arm64; actual support remains unclaimed until both build and destination execution pass. Cover cancellation, time/CPU/memory/disk/concurrency bounds, changed secret references, force rebuild, cache invalidation and active/rollback image retention.

Compose compatibility includes managed and raw modes, service fields, interpolation required/default behavior, profiles, networks, volumes, operator labels, advanced host capabilities and versioned Docker Compose differential tests. Raw mode remains planned in P18; preflight rejects a protected policy if its release guarantees cannot be satisfied.

## Proxy adapters

| Adapter | Reference | Status and planned slice | Limits and planned fixtures |
| --- | --- | --- | --- |
| Traefik | C5 | planned, first P07/M1 | Route reservation, domains/path/port, redirects, WebSockets, headers, validated config, rollback, ACME renewal; proxy-traefik, A20, A27 |
| Caddy | C5 | planned, P18 | Explicit configuration and migration adapter; proxy-caddy |
| Custom proxy | C5 | planned, P18 | Declare operator/platform ownership and unavailable managed features; proxy-custom |
| Proxy migration | C5 | planned, P18 | Validate ownership transfer, traffic continuity, certificate retention and recovery; proxy-migration |

All managed modes need HTTP validation, separately configured DNS challenge for wildcards, custom certificate rotation, DNS credential protection and control-plane restart fixtures. A server's existing external proxy network does not prove managed routing support.

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
