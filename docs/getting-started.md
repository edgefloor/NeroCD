# Getting started

NeroCD needs PostgreSQL for normal development. Docker Compose is the fastest
local path and builds its own Go/Bun environment. Source builds need Go 1.25 or
newer and Bun 1.3.6, the version pinned by the web package and CI.

## Docker Compose

```sh
docker compose up --build
```

Open <http://localhost:8080>. This development-only stack starts PostgreSQL,
runs migrations, and loads `db/seeds/dev.sql`. Stop it with `docker compose
down`; add `-v` only when you want to remove the local volume.

## Local server with PostgreSQL

```sh
export NEROCD_DATABASE_URL='postgres://nerocd:nerocd_dev@127.0.0.1:5432/nerocd?sslmode=disable'
make build
./bin/nerocd migrate --seed=false
./bin/nerocd bootstrap-admin --email admin@example.test --name 'Local Admin' \
  --password-stdin < /secure/nerocd-bootstrap-password
./bin/nerocd server --addr :8080
```

Choose a strong password outside the checkout, store it in the private input
file, and remove the file after the one-time bootstrap succeeds. To load the
development fixture instead, use
`NEROCD_DEV_SEED_FILE=./db/seeds/dev.sql ./bin/nerocd seed-dev`.

## Explicit disposable memory

```sh
umask 077; printf '%s\n' '<operator-chosen-password>' > /secure/nerocd-dev-password
unset NEROCD_DATABASE_URL
export NEROCD_DEV_MEMORY=true
export NEROCD_DEV_BOOTSTRAP_EMAIL='admin@example.test'
export NEROCD_DEV_BOOTSTRAP_PASSWORD_FILE=/secure/nerocd-dev-password
./bin/nerocd server --addr :8080
```

This mode is disposable and rejected by production configuration.
