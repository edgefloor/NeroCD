# NeroCD

NeroCD is building a self-hosted application platform in Go that replaces the
hosting workflows of Coolify. The active journey is:

> Connect a server → select a repository or image → configure the application → deploy → open its HTTPS URL → operate and recover it.

Hosting requirements R01–R68, acceptance scenarios A01–A37 and the declared
compatibility matrices lead the [implementation plan](docs/plan.md). Direct
hosting remains the primary navigation and onboarding journey until the core
replacement is complete. Deploying or operating an application must not require
a Task, coding agent, workspace or operational template.

The [complete specification](docs/product-spec.md) also retains revision 3's
future shared-workspace vision: people and AI agents build, test, review and
deploy software on machines they control, with durable Tasks and retained work.
R69–R102/A38–A66 and D0–D3 remain deferred scope. They must not delay hosting.
The [hosting ledger](docs/compatibility.md) and
[workspace ledger](docs/workspace-capabilities.md) keep these claims separate.
Feature parity, migration compatibility and unchanged-client compatibility
are different outcomes.

## Available now

Hosting-first documentation baseline: `65311f30470f34d0d26165d1cd13325fbd05fca0`.
Accepted application/source assessment:
`a92d100c6ba654e6797432e7ef7a6cfd55527272`, 2026-10-23. Existing Go API/application services, PostgreSQL, River execution,
authorization, audit and the embedded React/TypeScript UI are the starting system.

- PostgreSQL state, checked migrations through 0055, bootstrap administration,
  local-password sessions and one configured OIDC issuer.
- Projects, operational templates and execution runs, approvals, service
  accounts, API tokens, enrolled runners, fenced leases, replay and audit.
  Existing execution runs are distinct from the proposed durable collaboration Task.
- Shared project environments, typed hosting resources and explicit server,
  destination and project-grant bindings through API and UI. Stable placement
  does not imply host virtualization or broad machine qualification.
- Encrypted project-secret versions with create, rotate, protected owner reveal
  and revoke. [Broader execution delivery and value scopes](docs/secrets.md)
  remain planned; existing runner-file bindings have their own contract.
- Desired runtime configuration through resource GET/PUT APIs, with generation
  compare-and-swap, idempotent requests, explicit public nonsecret literals and
  exact managed-secret version references. Current owners write and viewers read.
  Migration 0052 stores desired input; migration 0053 adds accepted admission.
  Protected runner delivery checks exact prepared authority and current actor,
  configuration and secret versions, then audits metadata before decryption.
  Retries retain the original bounded delivery window.
  Journal format 8 stores only nonsecret references and delivery identity.
  Explicitly enabled CLI/API execution applies literals and exact managed-secret
  versions to Docker through protected stdin, with fixed delivery/replay deadlines.
  After authorization commits and before returning environment bytes, the endpoint
  rechecks the original runner credential and rejects delivery if it detects rotation.
  It cannot recall bytes already sent or values delivered to a runner or container.
  Docker administrators can inspect container environment values.
- Bounded runtime status challenge/report controls and viewer freshness reads,
  through migration 0054. The collector requires `--hosting-observation` with
  matching persisted `hosting_observation_v1` capability. It reads narrow Docker
  status and loopback readiness, selecting four targets fairly per 10-second cycle.
  This is not a 10-second sampling guarantee for each resource. The accepted
  read-only dashboard separates current, stale and historical target evidence,
  checks current viewer access and bounds reads with timeout and retry handling.
  Disk/CPU metrics remain pending.
- Runtime-log control through migration 0055: actor sessions, renewal, stop and
  bounded retained reads, plus exact runner claims/events, counters, replay gaps
  and purge. The accepted live adapter and embedded viewer connect fresh Docker stdout/stderr
  from a registered settled deployment through the compiled collector and API.
  Choose Follow to admit 30-second demand. Reads show source and retention gaps;
  access revocation clears sensitive state. Known bearer redaction is bounded,
  and raw application output can contain secrets. See the
  [collector](docs/hosting-runtime-logs.md) and
  [viewer](docs/hosting-runtime-log-viewer.md) limits.
- Enrollment commands use the exact persisted capabilities and identify runner
  configuration prerequisites, including `--hosting-runtime-logs` matched to
  persisted `hosting_runtime_logs_v1` in the manual hosting profile. This does not
  qualify SSH-assisted enrollment or
  complete host readiness.
- Runtime configuration UI for public literals and exact managed-secret versions,
  generation compare-and-swap, idempotent retries and fresh deployment generations.
  Accepted browser proof covers authenticated API, compiled runner and Docker,
  failed-readiness preservation, viewer authorization and responsive layouts.
  It uses seeded accounts, resources and placement plus a trusted persisted Git
  artifact; fresh Git builds, onboarding and public ACME are separate work.
- [Public registry-image deployment](docs/hosting-execution.md) through the
  API, transactional River dispatch and explicitly enabled agent. It records
  exact image/platform/daemon identity, ordinary HTTP readiness, per-release
  loopback ports and selected retained cleanup.
- [Public HTTPS Git configuration and builds](docs/hosting-git-resources.md)
  under trusted operator policy. The normal compiled agent builds Dockerfile,
  pinned Railpack and prepared static/SPA assets into immutable local artifacts.
  [Private HTTPS Git](docs/hosting-git-resources.md#request-a-private-https-git-build)
  supports explicit Basic/PAT source grants, compiled CLI Dockerfile and Railpack
  builds, and Dockerfile/Railpack static-provider WebUI journeys through output,
  same-host deployment and test-CA HTTPS. Private Railpack qualification covers
  resource creation/editing, exact-version grant retry after a lost response and
  preservation of the old HTTPS application after a genuine failed rebuild.
  Arbitrary language providers, provider breadth, compiled frontend coverage and
  complete A03 remain separate work. See the [accepted limits](docs/compatibility.md#accepted-foundations-and-evidence).
- [Exact trigger revisions](docs/hosting-build-trigger-revision.md) admitted by a
  trusted internal caller are frozen before checkout and require the runner's
  revision capability. Public/private HTTPS checkout builds the selected commit
  even after a branch moves; unavailable commits fail without branch fallback.
  Provider webhook authentication and deploy-on-push control remain planned.
- [Same-host artifact deployment](docs/hosting-local-build-runtime.md) and its
  [resource UI](docs/hosting-build-deployments.md#deploy-through-the-resource-ui).
  An explicitly enabled agent deploys the recorded artifact on the same daemon
  without rebuilding or pulling. Deployment and route selection are separate actions.
- [Durable typed build output](docs/hosting-build-log-delivery.md) with
  redaction, version 7 journal cursors, exact acknowledgment and response-loss
  replay before completion. The [opt-in build output viewer](docs/hosting-build-logs.md#resource-build-output-viewer)
  follows retained sequence pages, reports gaps and bounds local plaintext.
  This is build output, not general application-runtime logging.
- [Managed Traefik routing](docs/hosting-proxy-ui.md) with authorized
  generations and explicitly capable Linux agents. Real isolated fixtures
  verify HTTP and HTTPS using a trusted test CA, including local build
  artifacts. Public DNS and production ACME qualification remain separate.
- An embedded React/Vite/TanStack application and a versioned
  [API contract](openapi.yaml). Most list APIs use bounded offset pagination;
  the typed build log API uses its documented sequence cursor.

The active sequence is practical repository/image-to-HTTPS hosting and
operation/recovery, useful stateful hosting, remaining core parity, then migration
and extended compatibility. The next H1 gaps include the provider-authenticated
deploy-on-push controller; its exact-revision prerequisite is accepted. Other gaps include onboarding,
broader credentials, deployment policy, production TLS
qualification, broader Compose/storage, databases, backups,
restores, previews, terminals, schedules and notifications.

Remote builders, registry delivery, caching, releases, approvals, permissions and
observations can advance through a named hosting contract. Before any workspace
change, record [which hosting requirement needs it now, the minimum scope and
its acceptance test](docs/plan.md#hosting-dependency-test). General future
usefulness and sunk effort are insufficient.

Durable collaboration Tasks, coding-agent adapters, attachment, portable
checkpoints, general reservations, model/GPU services and vendor packages stay
deferred unless a bounded part passes that test. Existing unaccepted Task
checkpoints are preserved, not integrated. Trusted-host deployment does not
wait for a VM backend; untrusted execution stays disabled until its declared
isolation requirements are satisfied.

Existing fixed-port replacement can interrupt service. No narrow deployment
demo is complete Coolify parity.

## Quick start

With Docker Compose installed, start the development stack:

```sh
docker compose up --build
```

Open <http://localhost:8080>. This starts PostgreSQL, migrates it, loads the
development fixture, and starts NeroCD. Stop it with `docker compose down`; add
`-v` only when you intend to discard the local database volume.

To run from source outside Compose, use the Go 1.26.10 compiler with the Go 1.25
language baseline and Bun 1.3.6 (the
locked version used by CI), then build the embedded web assets. An unconfigured
server is rejected. Disposable memory is available only with
`NEROCD_DEV_MEMORY=true` and the required bootstrap email and owner-only
password file. See
[Getting started](docs/getting-started.md).

## Build and verify

`make build` installs locked Bun dependencies, builds `web/dist`, and embeds it
in the Go binary. `make check` runs normal Go, web, policy, and contract checks.

## Operations

[Production](docs/production.md) is the canonical deployment guide. It covers
digest-pinned Compose, separate migration-owner/application database roles,
secret files, HTTPS and proxy boundaries, bootstrap, and OIDC overrides.

- [PostgreSQL operations](docs/postgres-operations.md)
- [OIDC provisioning and offboarding](docs/oidc.md)
- [Secrets and runner bindings](docs/secrets.md)
- [Metrics and operations status](docs/observability.md)

## Releases

Dispatch the release workflow from `main` with the intended version and exact
commit SHA. GitHub Actions validates the commit before tagging, then requests
protected-environment approval and publishes the multi-architecture image.
Failed attempts may reuse a version until an image, release, or release asset
is published. Published versions are immutable. Local release gates are unsigned
and do not publish. See [release trust](docs/release-trust.md).

## Documentation

[Documentation index](docs/index.md) · [Architecture](docs/architecture.md) ·
[Product specification](docs/product-spec.md) · [Product plan](docs/plan.md) ·
[Compatibility ledger](docs/compatibility.md) · [API contract](openapi.yaml)
