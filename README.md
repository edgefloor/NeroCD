# NeroCD

NeroCD is becoming a self-hosted application platform in Go, with the complete
Coolify replacement scope recorded in the [product specification](docs/product-spec.md).
The target journey is to connect a server, choose a repository, configure an
application, deploy it, and open its HTTPS URL. Stateful hosting, previews,
recovery, operations and migration are part of the same product.

The current implementation is an automation control plane for projects,
repositories, templates, runs, approvals, runners, logs and audit events. The
Go API, CLI and runner share the codebase with an embedded React/TypeScript
web application. The [implementation plan](docs/plan.md) and
[compatibility ledger](docs/compatibility.md) distinguish the target from
validated capabilities. Repository-to-HTTPS is the first hosting milestone;
it is not a complete Coolify parity claim.

## Available now

- PostgreSQL-backed state, migrations, bootstrap administration, local-password
  sessions, and one explicitly configured OIDC provider.
- Project-scoped resources, audit events, API tokens, runner enrollment,
  fenced leases, sequential shell and Docker Compose execution, and artifact
  records.
- Shared project environments and image-application configuration in the API
  and WebUI. Saving an image reference records desired configuration.
- Project-managed encrypted secret versions with create, rotate, owner reveal,
  revoke and protected WebUI controls. Managed-secret execution delivery remains
  planned. See [secret storage and key configuration](docs/secrets.md).
- [Hosting servers and destinations](docs/hosting-destinations.md), explicit
  project grants and immutable desired resource placement. Manual enrollment
  uses the runner CLI; these controls do not validate Docker or deploy resources.
- [Transactional hosting job infrastructure](docs/hosting-jobs.md) using River
  OSS v0.44.1, with checked migrations and backup schema identity. The accepted
  server does not start a production hosting consumer.
- A React/Vite/TanStack web application embedded from `web/dist`, plus the
  versioned `/api/v1` contract in [openapi.yaml](openapi.yaml).
- Offset-paginated list APIs. Use `limit` and `offset`; do not assume cursors.

NeroCD does not currently provide SAML, LDAP, SCIM, claim or group mapping,
automatic OIDC signup, multiple issuers, general workflow parallelism,
managed-secret runner delivery or Vault lookup. Server readiness checks,
image-resource execution, managed proxy/TLS and the remaining full hosting
journey are still required. See the [dated progress assessment](docs/compatibility.md#accepted-foundations-and-evidence)
and [plan](docs/plan.md).

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
