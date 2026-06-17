# PostgreSQL Operations

NeroCD stores durable control-plane state in PostgreSQL when `NEROCD_DATABASE_URL`
is set. The in-memory store is for local development only.

## Migrations

Run migrations before starting a durable server:

```sh
NEROCD_DATABASE_URL=postgres://nerocd:nerocd_dev@127.0.0.1:5432/nerocd?sslmode=disable nerocd migrate --seed=false
```

The migrator records applied SQL files in `schema_migrations` with a checksum.
If an already-applied migration file changes, migration fails instead of
silently rewriting history.

## Backup

Use `pg_dump` for logical backups:

```sh
pg_dump "$NEROCD_DATABASE_URL" --format=custom --file=nerocd-$(date +%Y%m%d%H%M%S).dump
```

For Docker Compose:

```sh
docker compose exec postgres pg_dump -U nerocd -d nerocd --format=custom --file=/tmp/nerocd.dump
docker compose cp postgres:/tmp/nerocd.dump ./nerocd.dump
```

## Restore

Restore into an empty database, then run migrations:

```sh
pg_restore --dbname "$NEROCD_DATABASE_URL" --clean --if-exists nerocd.dump
NEROCD_DATABASE_URL="$NEROCD_DATABASE_URL" nerocd migrate --seed=false
```

For Docker Compose:

```sh
docker compose cp ./nerocd.dump postgres:/tmp/nerocd.dump
docker compose exec postgres pg_restore -U nerocd -d nerocd --clean --if-exists /tmp/nerocd.dump
docker compose run --rm nerocd migrate --seed=false
```

## Runtime Checks

- `/api/v1/health` reports process liveness.
- `/api/v1/ready` checks that the configured store is reachable.
- `/metrics` exposes plain text request counters and aggregate latency.
