# NeroCD

NeroCD is a self-hosted automation control plane. It manages projects,
repositories, templates, runs, approvals, runners, logs, and audit events.
The API, CLI, runner, and embedded web application are built from one Go
codebase.

## Available now

- PostgreSQL-backed state, migrations, bootstrap administration, local-password
  sessions, and one explicitly configured OIDC provider.
- Project-scoped resources, audit events, API tokens, runner enrollment,
  fenced leases, sequential shell and Docker Compose execution, and artifact
  records.
- A React/Vite/TanStack web application embedded from `web/dist`, plus the
  versioned `/api/v1` contract in [openapi.yaml](openapi.yaml).
- Offset-paginated list APIs. Use `limit` and `offset`; do not assume cursors.

NeroCD does not currently provide SAML, LDAP, SCIM, claim or group mapping,
automatic OIDC signup, multiple issuers, general workflow parallelism, or a
production database/vault secret backend. See [the plan](docs/plan.md).

## Quick start

With Docker Compose installed, start the development stack:

```sh
docker compose up --build
```

Open <http://localhost:8080>. This starts PostgreSQL, migrates it, loads the
development fixture, and starts NeroCD. Stop it with `docker compose down`; add
`-v` only when you intend to discard the local database volume.

To run from source outside Compose, use Go 1.25 or newer and Bun 1.3.6 (the
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

Tag the reviewed revision with the next unused annotated `vMAJOR.MINOR.PATCH`
tag. GitHub Actions validates evidence, publishes an immutable multi-architecture
image after protected-environment approval, and verifies release trust. Local
release gates are unsigned and do not publish. See [release trust](docs/release-trust.md).

## Documentation

[Documentation index](docs/index.md) · [Architecture](docs/architecture.md) ·
[Product plan](docs/plan.md) · [API contract](openapi.yaml)
