# Getting started

Use one of these development-only setups from a fresh checkout. Run every
relative command in this guide from the repository root. For a deployed
service, use the [production guide](production.md).

## Start with Docker Compose

Use this path when Docker Engine and the Docker Compose plugin are available.
It builds NeroCD, starts the existing `postgres` and `nerocd` services, runs
migrations, and loads `db/seeds/dev.sql`.

The Compose database uses the checked-in development credentials:
`nerocd` and `nerocd_dev`. Do not use those credentials outside local
development.

```sh
docker compose up --build -d
docker compose exec nerocd nerocd ready --addr http://127.0.0.1:8080
```

The second command prints `{"status":"ready"}`. Open
<http://localhost:8080> after it succeeds.

To stop the stack and retain the database, run:

```sh
docker compose down
```

`docker compose down` retains the `postgres-data` volume. Run
`docker compose down -v` only when you want to delete that local database.

## Start a local server with PostgreSQL

Use this path when you have PostgreSQL, Go 1.25 or newer, and Bun 1.3.6. The
database must accept the URL below, or set `NEROCD_DATABASE_URL` to the URL
for your local database. `make build` installs the locked web dependencies and
creates `bin/nerocd`.

```sh
export NEROCD_DATABASE_URL='postgres://nerocd:nerocd_dev@127.0.0.1:5432/nerocd?sslmode=disable'
make build
bin/nerocd migrate --seed=false
```

Choose one way to create local data. To create the first administrator, keep
the password in an owner-only file outside the checkout, then run:

```sh
bin/nerocd bootstrap-admin --email admin@example.test --name 'Local Admin' \
	--password-stdin < /secure/nerocd-bootstrap-password
```

Remove the password file after a successful bootstrap. To load the development
fixture instead, run this command and do not run `bootstrap-admin`:

```sh
NEROCD_DEV_SEED_FILE=./db/seeds/dev.sql bin/nerocd seed-dev
```

Start the server in one terminal:

```sh
bin/nerocd server --addr :8080
```

In another terminal, verify that the database-backed server is ready:

```sh
bin/nerocd ready --addr http://127.0.0.1:8080
```

The command prints `{"status":"ready"}`. Stop the server with `Ctrl-C`.
That stops only the server. PostgreSQL keeps the data. See
[PostgreSQL operations](postgres-operations.md) for migrations, backups, and
restore safeguards.

## Start disposable memory

Use this path only for a throwaway local session. You need Go 1.25 or newer,
Bun 1.3.6, and `bin/nerocd` from `make build`. Keep the bootstrap password in
an absolute-path, owner-only file outside the checkout.

```sh
unset NEROCD_DATABASE_URL
export NEROCD_DEV_MEMORY=true
export NEROCD_DEV_BOOTSTRAP_EMAIL='admin@example.test'
export NEROCD_DEV_BOOTSTRAP_PASSWORD_FILE=/secure/nerocd-dev-password
bin/nerocd server --addr :8080
```

In another terminal, check the server:

```sh
bin/nerocd ready --addr http://127.0.0.1:8080
```

Stop the server with `Ctrl-C`. Process exit discards every memory-store record.
This mode is rejected by production configuration. For password-file rules and
other secret handling, see [Secrets](secrets.md).
