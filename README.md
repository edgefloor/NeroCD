# NeroCD

NeroCD is a self-hosted application platform under development, with a Go control
plane and a React/TypeScript UI. It aims to replace the hosting workflows of
Coolify: connect a server, choose a repository or container image, configure it,
deploy it, and operate the application on machines you control.

Application hosting is the current priority. Team workspaces and coding-agent
collaboration are a later part of the [product plan](docs/plan.md).

## Current status

NeroCD has working hosting workflows, but does not yet provide full Coolify
parity or drop-in compatibility. The tested deployment profiles are narrow,
primarily trusted Linux arm64 hosts with Docker. Public ACME certificates and
broader host support still need end-to-end qualification.

The current implementation includes:

- Image deployments and HTTPS Git builds using Dockerfile, Railpack, and static
  application paths, with build output and deployment history.
- Server enrollment, project environments, destinations, managed secrets, and
  project access controls.
- Traefik routing, runtime status and logs, and opt-in GitHub build-on-push and
  automatic deployment.
- PostgreSQL and Redis hosting, application connections, owned storage, and
  selected backup and recovery workflows.

These capabilities have specific setup requirements and recovery limits. Use the
[compatibility ledger](docs/compatibility.md) for tested profiles and evidence,
and the [implementation plan](docs/plan.md) for remaining work. A passing local
demo does not establish production readiness for every hosting workflow.

## Try it locally

You need Docker Engine and the Docker Compose plugin. Run these commands from
the repository root:

```sh
docker compose up --build -d
docker compose exec -T nerocd nerocd ready --addr http://127.0.0.1:8080
```

The first command builds NeroCD, starts PostgreSQL, applies migrations, and loads
the development fixture. If the readiness command fails during startup, retry
it after a few seconds. A ready server prints `{"status":"ready"}`.

Open <http://localhost:8080> and sign in with the checked-in development account:

- Email: `admin@example.local`
- Password: `admin`

This is a local development setup with public fixture credentials. Use the
[production guide](docs/production.md) for deployment configuration.

Compose starts the control plane, not an application deployment runner. To
deploy an application, follow [server and destination setup](docs/hosting-destinations.md),
then [deploy an image application](docs/hosting-image-deployments.md) or
[configure a Git application](docs/hosting-git-resources.md).

To stop the local stack:

```sh
docker compose down
```

The PostgreSQL volume remains available for the next start. For source-based
startup, readiness troubleshooting, or a disposable memory store, see
[Getting started](docs/getting-started.md).

## Develop

Use Go 1.26.10 and Bun 1.3.6, matching the compiler and frontend tooling in CI.
The Go language baseline in `go.mod` is 1.25.

```sh
make build
```

This installs locked frontend dependencies, builds the UI into `web/dist`, and
embeds it in `bin/nerocd`. Starting the server also requires database or explicit
development-memory configuration from the [setup guide](docs/getting-started.md).

Choose checks for the area you change:

| Command | Purpose |
| --- | --- |
| `make test` | Go tests |
| `make web-test` | Frontend tests |
| `make verify` | Go formatting, vet, tests, lint, and shell checks |
| `make browser-smoke` | Build and browser smoke tests |
| `make check` | Broad Go, web, policy, contract, generated-code, and Docker checks |

`make check` requires Docker and starts by removing generated build output.
Database and hosting runtime checks can require additional services and fixtures.
See the [Makefile](Makefile) for individual targets.

PR CI runs independent Go and web checks concurrently. The runtime race suite
is divided into six deterministic groups discovered from the current test
binary; other product packages run in a separate race job. Every test, example,
and fuzz seed remains included, and the required `check` job succeeds only when
all jobs succeed. Release evidence gates retain their serial execution order.
To reproduce a CI group locally, run `python3 -B scripts/ci-check.py runtime-0`
through `runtime-5`, or select `go`, `race`, or `web`. The Go CI job also runs
`make postgres-test` with its PostgreSQL fixture, and the web job generates
release artifacts after its checks pass.

## Code layout

The control plane stores state in PostgreSQL and dispatches hosting work through
River. Enrolled Go runners execute work on application hosts. The server embeds
the web UI in the same binary.

| Path | Contents |
| --- | --- |
| `cmd/nerocd` | Server, CLI, migrations, runner, and operations commands |
| `internal` | API, application services, persistence, and hosting execution |
| `web/app` | React/TypeScript UI |
| `db` | PostgreSQL migrations and development seed data |
| `acceptance` | Runtime workflow fixtures and gates |

Read the [architecture guide](docs/architecture.md) for component boundaries and
execution flow. The HTTP API contract is [openapi.yaml](openapi.yaml).

## Documentation

- [Documentation index](docs/index.md)
- [Production deployment](docs/production.md)
- [PostgreSQL operations](docs/postgres-operations.md)
- [Secrets](docs/secrets.md) and [OIDC setup](docs/oidc.md)
- [Observability](docs/observability.md)
- [Release trust](docs/release-trust.md)
- [Product specification](docs/product-spec.md)
