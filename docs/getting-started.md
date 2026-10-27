# Getting started

Start a local NeroCD control plane with Docker Compose, an existing PostgreSQL
database, or a disposable memory store. Run commands from the repository root.
Use the [production guide](production.md) for a deployed service.

## Start with Docker Compose

Use this path when Docker Engine and the Docker Compose plugin are available.
It builds NeroCD, starts the existing `postgres` and `nerocd` services, runs
migrations, and loads `db/seeds/dev.sql`.

The Compose database uses the checked-in development credentials:
`nerocd` and `nerocd_dev`. Do not use those credentials outside local
development.

```sh
docker compose up --build -d
(
	attempt=0
	until docker compose exec -T nerocd nerocd ready --addr http://127.0.0.1:8080; do
		attempt=$((attempt + 1))
		if [ "$attempt" -eq 30 ]; then
			exit 1
		fi
		sleep 1
	done
)
```

Early readiness attempts can fail while migrations and seed loading run. The
final successful attempt prints `{"status":"ready"}`. Open
<http://localhost:8080> after it succeeds. Sign in with the development fixture:
email `admin@example.local`, password `admin`. These public credentials are for
local development only.

Compose starts the control plane. To deploy applications, also
[enroll a runner and configure a destination](hosting-destinations.md), then
[deploy an image](hosting-image-deployments.md) or
[configure a Git application](hosting-git-resources.md).

To stop the stack and retain the database, run:

```sh
docker compose down
```

`docker compose down` retains the `postgres-data` volume. Run
`docker compose down -v` only when you want to delete that local database.

## Start a local server with PostgreSQL

Use this path when you have PostgreSQL, Go 1.26.10, and Bun 1.3.6. The
database must accept the URL below, or set `NEROCD_DATABASE_URL` to the URL
for your local database. `make build` installs the locked web dependencies and
creates `bin/nerocd`.

```sh
export NEROCD_DATABASE_URL='postgres://nerocd:nerocd_dev@127.0.0.1:5432/nerocd?sslmode=disable'
make build
bin/nerocd migrate --seed=false
```

Choose either a new administrator or the development fixture. To create the
first administrator, keep
the password in an owner-only file outside the checkout. Create the file
without putting the password in shell history, then run:

```sh
(
	password_file=$(mktemp /tmp/nerocd-bootstrap-password.XXXXXX) || exit 1
	chmod 600 "$password_file" || { unlink "$password_file"; exit 1; }
	printf 'NeroCD development password: ' >&2
	if ! read -r -s password; then
		printf '\nPassword input failed.\n' >&2
		unlink "$password_file"
		exit 1
	fi
	printf '\n' >&2
	if [ -z "$password" ]; then
		printf 'Password cannot be empty.\n' >&2
		unlink "$password_file"
		exit 1
	fi
	if ! printf '%s\n' "$password" > "$password_file"; then
		unset password
		unlink "$password_file"
		exit 1
	fi
	unset password
	if bin/nerocd bootstrap-admin --email admin@example.test --name 'Local Admin' \
		--password-stdin < "$password_file"; then
		unlink "$password_file"
	else
		printf 'bootstrap-admin failed. Fix the problem, then run:\n' >&2
		printf "bin/nerocd bootstrap-admin --email admin@example.test --name 'Local Admin' --password-stdin < %s && unlink %s\n" \
			"$password_file" "$password_file" >&2
		exit 1
	fi
)
```

The command removes the password file after a successful bootstrap. On a
bootstrap failure, it retains the file and prints its path for retry. To load
the development fixture instead, run this command and do not run
`bootstrap-admin`:

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

Use this path only for a throwaway local session. You need Go 1.26.10,
Bun 1.3.6, and `bin/nerocd` from `make build`. Keep the bootstrap password in
an absolute-path, owner-only file outside the checkout. Create the file without
putting the password in shell history.

```sh
(
	password_file=$(mktemp /tmp/nerocd-dev-password.XXXXXX) || exit 1
	chmod 600 "$password_file" || { unlink "$password_file"; exit 1; }
	printf 'NeroCD development password: ' >&2
	if ! read -r -s password; then
		printf '\nPassword input failed.\n' >&2
		unlink "$password_file"
		exit 1
	fi
	printf '\n' >&2
	if [ -z "$password" ]; then
		printf 'Password cannot be empty.\n' >&2
		unlink "$password_file"
		exit 1
	fi
	if ! printf '%s\n' "$password" > "$password_file"; then
		unset password
		unlink "$password_file"
		exit 1
	fi
	unset password
	unset NEROCD_DATABASE_URL
	export NEROCD_DEV_MEMORY=true
	export NEROCD_DEV_BOOTSTRAP_EMAIL='admin@example.test'
	export NEROCD_DEV_BOOTSTRAP_PASSWORD_FILE="$password_file"
	if bin/nerocd server --addr :8080; then
		unlink "$password_file"
	else
		printf 'Server startup failed. Fix the problem, then run:\n' >&2
		printf "( unset NEROCD_DATABASE_URL; NEROCD_DEV_MEMORY=true NEROCD_DEV_BOOTSTRAP_EMAIL='admin@example.test' NEROCD_DEV_BOOTSTRAP_PASSWORD_FILE=%s bin/nerocd server --addr :8080 && unlink %s )\n" \
			"$password_file" "$password_file" >&2
		exit 1
	fi
)
```

In another terminal, check the server:

```sh
bin/nerocd ready --addr http://127.0.0.1:8080
```

Stop the server with `Ctrl-C`. The command then removes the password file. If
startup fails, the command retains the file and prints its path for retry.
Process exit discards every memory-store record. This mode is rejected by
production configuration. For password-file rules and other secret handling,
see [Secrets](secrets.md).

## Runner identity and restart

This runner CLI requires a server that implements `GET /api/v1/runners/identity`.
Upgrade the server before deploying this runner binary. An older server returning
404 causes startup to fail; the runner does not fall back to a local identity.
Existing heartbeat and claim clients keep their current API contracts.

After acquiring a dedicated credential through enrollment, registration, or
`--credential-file`, the CLI reads the authenticated runner ID before opening its
journal. Omit `--id` when restarting an enrolled runner from its credential file.
The authenticated ID selects the same default journal under the same `--work-dir`,
including when enrollment assigned an ID other than `runner_local`.

An explicit `--id` must match the authenticated ID. A mismatch, rejected
credential, malformed identity response, or transport failure stops startup before
journal access, heartbeat, claim, or execution. Identity reads do not update
heartbeat or other runner state. After identity succeeds, durable journal replay
still finishes before heartbeat and claim traffic.
