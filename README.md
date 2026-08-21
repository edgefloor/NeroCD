# NeroCD

NeroCD is an open source automation control plane intended as a modern Semaphore UI alternative.

The first principle is a small trusted dependency surface: Go binaries for the API, runner, and CLI; embedded web assets; and explicit integration boundaries for corporate auth, audit, secrets, and source control.

## Goals

- Fast REST API with a stable `/api/v1` contract.
- Snappy web UI without a large browser dependency tree.
- Single binary per architecture for CLI, server, and runner modes.
- Open source only, with no proprietary build, auth, UI, or packaging requirements.
- Corporate-ready primitives: local auth, OIDC, SAML, LDAP, RBAC, service accounts, API tokens, audit events, secret-backend design, runners, schedules, approvals, and policy hooks.

## Current Scaffold

```text
cmd/nerocd        single binary entrypoint
internal/api      HTTP API and routing
internal/app      application service layer
internal/auth     auth interfaces and local dev provider
internal/domain   core domain models
internal/store    repository interfaces and in-memory bootstrap store
web/static        embedded browser UI
docs              architecture and roadmap
db                SQL migrations and local development seeds
```

## Run

```sh
go run ./cmd/nerocd server
```

Then open `http://localhost:8080`.

If the server is running on a different port, set the CLI default once:

```sh
export NEROCD_ADDR=http://127.0.0.1:18080
```

## Docker Dev Environment

```sh
docker compose up --build
```

This starts PostgreSQL, runs tracked migrations through `nerocd migrate`, applies development seed data, and starts the NeroCD server.

### Production Compose profile

`compose.production.yaml` is deliberately separate from the development stack.
Copy `.env.production.example` outside the repository, supply a canonical
`repository@sha256:<digest>` server image, an existing external proxy network,
two absolute URL secret-file paths, and distinct owner/app PostgreSQL role
names. Protect the inputs with mode `0400`; the one-shot initializer places
the owner and app URL in separate private runtime volumes. The migrator sees
only the owner file, the server sees only the app file, and role-init is the
only short-lived service that sees both. Production rejects environment/argument
database URLs, equal credentials, mutable image names, development seeding,
and the memory store.

```sh
docker compose --env-file /secure/nerocd-production.env -f compose.production.yaml up -d
# Run doctor on the deployment host before `up`, with both strict secret-file
# paths and both configured role names from the production env file available.
NEROCD_MODE=production NEROCD_IMAGE_REF="$NEROCD_IMAGE" \
  NEROCD_OWNER_DATABASE_URL_FILE="$NEROCD_DATABASE_URL_SECRET" \
  NEROCD_APP_DATABASE_URL_FILE="$NEROCD_APP_DATABASE_URL_SECRET" nerocd doctor
```

The production stack publishes no database, metrics, or application host port.
Attach only an approved reverse proxy to `NEROCD_PROXY_NETWORK`. `nerocd doctor`
validates configuration without connecting to the database and never prints a
secret value.

Docker builds the Vite WebUI with Bun, embeds `web/dist` into the Go binary, and serves the compiled app from the NeroCD server.

Development data is intentionally not embedded in that binary. To seed an
explicit local database, provide the checked-out fixture (or your own safe
fixture) explicitly: `NEROCD_DEV_SEED_FILE=./db/seeds/dev.sql nerocd seed-dev`.
Production rejects `seed-dev`; initialize its first administrator exactly once
with `nerocd bootstrap-admin --email <email> --name <name> --password-file <owner-only-file>`.
Before that command completes, the sign-in screen intentionally offers only
this CLI guidance. The unauthenticated `GET /api/v1/bootstrap-status` response
is limited to `{"status":"required"}` or `{"status":"complete"}`; it never
identifies users or exposes database state.

Useful CLI commands:

```sh
go run ./cmd/nerocd version
go run ./cmd/nerocd health
go run ./cmd/nerocd migrate --database-url postgres://nerocd:nerocd_dev@127.0.0.1:5432/nerocd?sslmode=disable --seed=false
go run ./cmd/nerocd session --email '<bootstrap email>' --password '<bootstrap password>'
NEROCD_TOKEN=ncd_... go run ./cmd/nerocd projects
NEROCD_TOKEN=ncd_... go run ./cmd/nerocd templates
NEROCD_TOKEN=ncd_... go run ./cmd/nerocd runs
NEROCD_TOKEN=ncd_... go run ./cmd/nerocd run-logs
NEROCD_TOKEN=ncd_... go run ./cmd/nerocd runner --tags local --capabilities shell
NEROCD_TOKEN=ncd_... go run ./cmd/nerocd runner --tags local --capabilities shell --once
go run ./cmd/nerocd smoke
```

`health`, `ready`, and `session` are public bootstrap routes. Other `/api/v1`
routes require the bearer token returned by `session`; pass it with `--token` or
`NEROCD_TOKEN`. `/metrics` is deliberately not public: it requires an
authenticated `system_admin` bearer/session credential and is reached only
through the approved proxy network in production. API JSON requests are capped
at 1 MiB and must be a single `application/json` document; list pages are
bounded to 100 items (offset at most 100,000).

Global administrators can use the WebUI **Operations** page or
`GET /api/v1/operations/status` for a short, database-clock operational
summary. It is all-or-nothing: authenticated non-admins are denied, and a
readiness or snapshot failure returns the fixed `operations_unavailable` 503
instead of stale or partial metrics. Raw Prometheus remains the protected
`/metrics` endpoint; Operations intentionally shows only fixed aggregate cards.

## Build

```sh
make build
```

`make build` installs the frontend dependencies from `web/app/bun.lock`, builds `web/dist`, and compiles a single Go binary with the WebUI embedded.

## API Contract

The public API contract is versioned under `/api/v1` and documented in `openapi.yaml`.

Run local contract validation with:

```sh
go run ./cmd/nerocd contract
```

`make check` runs Go tests, frontend tests, the dependency policy, the WebUI build, and the contract check. The contract check verifies that implemented public routes match `openapi.yaml`, that CLI/WebUI route usage is documented, that protected routes document auth/error behavior, that mutations declare JSON request bodies, and that representative handler responses match the documented envelopes. List endpoints return a stable envelope with `items`, `limit`, `offset`, `count`, and `total`.

PostgreSQL integration tests are skipped locally unless a test database is provided. CI provisions Postgres and runs them with `make postgres-test`:

```sh
NEROCD_TEST_DATABASE_URL=postgres://nerocd:nerocd_dev@127.0.0.1:5432/nerocd?sslmode=disable go test ./internal/store
```

The test creates and drops an isolated schema inside that database.

The dependency policy writes release metadata to `artifacts/`, including a
CycloneDX JSON SBOM and SHA-256 checksums for available release artifacts.

## Web Application

The TypeScript WebUI lives in `web/app` and builds with Vite to `web/dist`.

```sh
cd web/app
bun install
bun run dev
bun run build
```

The Vite dev server proxies `/api` requests to `http://127.0.0.1:8080`, so run the Go server or Docker stack separately during frontend development. Production and Docker builds serve the compiled `web/dist` app from the Go binary.

## Dependency Policy

The supply-chain policy is enforced by:

```sh
make web-policy
make release-artifacts
```

The policy requires committed lockfiles, forbids frontend runtime dependencies and CDN imports, blocks package lifecycle scripts unless explicitly reviewed, checks allowed licenses, and requires new top-level dependencies to be documented in `docs/dependency-exceptions.md`.

`make release-artifacts` builds `bin/nerocd` and writes:

```text
artifacts/sbom.json
artifacts/checksums.txt
```

## Backend Primitives

The current backend supports both memory-backed bootstrap mode and PostgreSQL-backed dev mode.

Implemented primitives:

- Users and sessions.
- Password-backed local sessions and bearer-token authentication for protected API routes.
- Projects.
- Repository metadata and runner primitive plans.
- Task templates.
- Task runs.
- Immutable `RunSpec`, workflow capture, and sequential workflow step state on runs.
- First mutations for projects, templates, run requests, and run approvals.
- Approval records.
- Runner registration with dedicated runner tokens, token rotation/revocation, heartbeat, tag/capability-based claim, log append, lease status, and lease completion APIs.
- Persisted global `system_admin` role for runner registration and token management.
- Admin-minted service-account API tokens for unattended runner bootstrap, with
  token kind, roles, optional expiry, and `runner_admin` scope.
- Long-running local runner polling, git checkout execution, constrained shell/process execution, sequential workflow step execution, cooperative cancellation, local environment-backed secret injection, filesystem artifact checks, durable artifact records, stdout/stderr/system log streaming, and timeout-aware completion.
- Append-only audit events for mutations.
- Run logs.
- Run artifact records.
- Tracked SQL migrations and development seed data.
- Gated PostgreSQL integration coverage for roles, API tokens, runner lifecycle, leases, JSON fields, logs, and audit persistence.
- CLI/API smoke checks for login, paginated reads, workflow run creation,
  runner registration, two-step claiming/completion, logs, artifact records, and
  final run verification.
- Playwright browser smoke for login and the first operator navigation path.

Production secret-backend rules are documented in `docs/secrets.md`. The local
runner resolves only `env` provider secret bindings today; database/vault
providers remain reference-only until encrypted value storage and read audit are
implemented.

Run the full local smoke check against a running server:

```sh
NEROCD_ADDR=http://127.0.0.1:8080 ./bin/nerocd smoke
```

PostgreSQL backup and restore operations are documented in
`docs/postgres-operations.md`.
