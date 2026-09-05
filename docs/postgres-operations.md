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

Use the bounded NeroCD wrapper for logical backups. It creates a new atomic
backup directory, writes a versioned manifest, records a SHA-256 checksum and
the applied migration ledger, and can inventory runner-file metadata without
copying any secret contents. The parent directory must already be owner-only
(mode `0700` or stricter).

```sh
mkdir -m 0700 /secure/nerocd-backups
nerocd backup --database-url "$NEROCD_DATABASE_URL" --output-dir /secure/nerocd-backups \
  --runner-file-root /secure/runner-files
```

The production profile has an explicit, non-starting `tools` service. It
receives only the migration-owner secret and no proxy network. Bind the
owner-only backup directory for that one invocation; the Docker socket is not
mounted. A Docker socket grants root-equivalent host access, so never add it to
this service.

```sh
docker compose --env-file /secure/nerocd-production.env -f compose.production.yaml \
  --profile tools run --rm \
  -v /secure/nerocd-backups:/backups \
database-tools backup --output-dir /backups
```

## Off-host Export and Verification

The local schedule is not disaster recovery by itself: its archive volume is
on the same host as PostgreSQL. Mount an operator-managed encrypted destination
(another machine, removable media, or a mounted backup service) and export a
completed archive. NeroCD does not require a cloud vendor or receive credentials
for that destination. It verifies the private manifest, exact dump checksum,
and archive shape before copying, then verifies the staged copy before atomically
publishing it with `0700` directory and `0600` file permissions.

```sh
mkdir -m 0700 /mnt/off-host/nerocd
nerocd backup-export \
  --input-dir /secure/nerocd-backups/backup-YYYYMMDDTHHMMSSZ \
  --output-dir /mnt/off-host/nerocd
nerocd backup-verify \
  --input-dir /mnt/off-host/nerocd/export-YYYYMMDDTHHMMSSZ
```

Perform this export after every scheduled backup and retain the exact NeroCD
image digest that created it. At least quarterly, create an isolated empty
PostgreSQL target on a different host, run `backup-verify`, then use `restore`
against that disposable target and confirm the restored schema ledger. Runner
file content is intentionally never copied; restore that material separately
from its own encrypted backup before performing the database restore.

## Local Backup Schedule

The production profile also contains a separate, non-root `backup-scheduler`
service. It is disabled by default: set `NEROCD_BACKUP_SCHEDULE_ENABLED=true`
only after provisioning the private `backup-data` volume and an approved local
collection procedure. The scheduler has only the migration-owner credential,
the internal database network, and that one owner-only volume; it cannot be
called through the API or browser.

It uses PostgreSQL time and a session advisory lock, so a restart continues the
stored next-run time and concurrent scheduler containers cannot overlap. A
missed interval causes at most one catch-up archive. Failed runs back off from
one minute up to the configured interval, with a durable cap of eight failures.
Successful runs retain only the configured number of newest canonical backup
directories; unsafe, unexpected, or symlinked archive content stops rotation
without deleting anything further. The manual `backup` and `restore` commands
remain unchanged and are still the procedure for an immediate operator action.

`/api/v1/operations/status` and protected `/metrics` expose only aggregate
schedule state (`disabled`, `waiting`, `due`, `running`, or `backoff`), time to
the next attempt, and the bounded failure count. They never expose archive
paths, database URLs, runner-file values, or tool diagnostics. Investigate a
`backoff` state by examining the private scheduler container logs, repairing
the local destination, and allowing the next DB-clock attempt; do not manually
delete unknown files from a backup directory.

## Restore

Restore only into a newly created, empty database. The command verifies the
manifest checksum and exact migration ledger before invalidating every active
session and runner lease; operators must issue fresh credentials and let the
reaper reconcile interrupted work. It never uses `--clean` against a live
target. Restore requires two explicit safeguards: `--allow-disposable-target`
and an exact `--confirm-target-database` name. Before `pg_restore`, NeroCD
holds a restore-admission advisory lock, rejects any other active DB session,
and rejects non-public schemas and objects in `public` or another user schema.
It does not terminate sessions for you.

```sh
createdb nerocd_restore
nerocd restore --database-url "$NEROCD_RESTORE_DATABASE_URL" \
  --input-dir /secure/nerocd-backups/backup-YYYYMMDDTHHMMSSZ \
  --allow-disposable-target --confirm-target-database nerocd_restore \
  --runner-file-root /secure/runner-files
```

For production Compose, create an isolated empty target stack first, mount the
selected backup directory read-only, and invoke the same offline tool with the
owner credential. Do not point it at an existing stack:

```sh
docker compose --env-file /secure/nerocd-production.env -f compose.production.yaml \
  --profile tools run --rm \
  -v /secure/nerocd-backups/backup-YYYYMMDDTHHMMSSZ:/restore:ro \
  -v /secure/runner-files:/runner-files:ro \
database-tools restore --input-dir /restore --runner-file-root /runner-files \
  --allow-disposable-target --confirm-target-database nerocd_restore
```

## Runtime Checks

- `/api/v1/health` reports process liveness.
- `/api/v1/ready` checks that the configured store is reachable.
- `/metrics` exposes bounded-cardinality plain-text request counters and
  aggregate latency only to authenticated `system_admin` callers. In production
  it remains behind the approved proxy network; never publish it as a host port.
