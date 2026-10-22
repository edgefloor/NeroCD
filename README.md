# NeroCD

NeroCD is building a shared workspace where people and AI agents build, test,
review and deploy software on machines they control. The target is a durable
Task with retained changes, checks and a working environment that a teammate
can review and continue after the submitting laptop goes offline.

The [revision 3 product specification](docs/product-spec.md) defines the complete
target. The first demonstration is task-to-reviewed-deployment, ordered through
D0–D3 in the [implementation plan](docs/plan.md). Durable Tasks, Workspaces,
agent adapters, qualified VM isolation, reservations, shared remote cache and
private task previews are planned. They are not current support claims.

Direct application hosting remains a separate user journey. Applications,
databases, Compose, persistent storage, routing, recovery and the complete
Coolify compatibility target remain in scope. Users will not need a coding
task or AI agent to host an application. The [workspace capability ledger](docs/workspace-capabilities.md)
and [hosting compatibility ledger](docs/compatibility.md) track these outcomes separately.

## Available now

Current accepted assessment: `852f8e6b8d5624a95865c276dcea4a239d1f8fea`,
2026-10-22. Existing Go API/application services, PostgreSQL, River execution,
authorization, audit and the embedded React/TypeScript UI are the starting system.

- PostgreSQL state, checked migrations through 0051, bootstrap administration,
  local-password sessions and one configured OIDC issuer.
- Projects, operational templates and execution runs, approvals, service
  accounts, API tokens, enrolled runners, fenced leases, replay and audit.
  Existing execution runs are distinct from the proposed durable collaboration Task.
- Shared project environments, typed hosting resources and explicit server,
  destination and project-grant bindings through API and UI. Stable placement
  does not imply host virtualization or broad machine qualification.
- Encrypted project-secret versions with create, rotate, protected owner reveal
  and revoke. [Managed execution delivery and broader scopes](docs/secrets.md)
  remain planned; existing runner-file bindings have their own contract.
- [Public registry-image deployment](docs/hosting-execution.md) through the
  API, transactional River dispatch and explicitly enabled agent. It records
  exact image/platform/daemon identity, ordinary HTTP readiness, per-release
  loopback ports and selected retained cleanup.
- [Public HTTPS Git configuration and builds](docs/hosting-git-resources.md)
  under trusted operator policy. The normal compiled agent builds Dockerfile,
  pinned Railpack and prepared static/SPA assets into immutable local artifacts.
  Private Git configuration/provider delivery and compiled-static product
  coverage remain separate work.
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

Required work includes durable Tasks/Workspaces and checkpoints, task grants,
qualified Linux VM execution, machine reservations and budgets, remote artifact
publication and cache sharing, agent/model adapters, private multi-resource
previews and human production-plan authorization. Mac, Windows, NAS, GPU and
cloud-provider qualification have separate planned tracks. A machine pool does
not combine hosts into shared memory or keep an offline application host running.

Hosting gaps include private sources, managed runtime-secret delivery,
continuous observations and runtime logs, terminals, databases and data
lifecycle, broader Compose, schedules, notifications, migration and complete
recovery. Existing same-port replacement can interrupt service. No D0–D3 or
full Coolify parity milestone is declared complete.

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
