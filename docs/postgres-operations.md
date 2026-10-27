# PostgreSQL operations

This guide backs up and restores the NeroCD control plane. Hosted application databases
use the separate [database backup workflow](hosting-database-backups.md).

NeroCD uses PostgreSQL for durable state. The memory store is disposable local
development only. In production, use the owner and app credential files from the
[production guide](production.md).

## Migrations

Run migrations before starting a durable server:

```sh
NEROCD_DATABASE_URL=postgres://nerocd:nerocd_dev@127.0.0.1:5432/nerocd?sslmode=disable nerocd migrate --seed=false
```

The migrator records applied SQL files in `schema_migrations` with a checksum. If an
already-applied migration file changes, migration fails instead of silently rewriting
history.

## Backup

Use the bounded NeroCD wrapper for logical backups. It creates a new atomic backup
directory, writes a versioned manifest, records a SHA-256 checksum and the applied
migration ledger, and can inventory runner-file metadata without copying any secret
contents. The parent directory must already be owned by the process running the command
and have mode exactly `0700`.

The wrapper supplies database passwords to `pg_dump` and `pg_restore` through a
temporary libpq password file in a private directory. It removes the file after the
tool exits and excludes the password and NeroCD credentials from child arguments and
environment. Passwords containing line breaks or NUL are rejected because libpq
password files cannot represent them. SSL key passwords must be configured outside
the database URL. See the [libpq password file format](https://www.postgresql.org/docs/current/libpq-pgpass.html).

```sh
mkdir -m 0700 /secure/nerocd-backups
nerocd backup --database-url "$NEROCD_DATABASE_URL" --output-dir /secure/nerocd-backups \
  --runner-file-root /secure/runner-files
```

The production profile has an opt-in, non-starting `tools` service, which runs as UID
10001. For a host bind, provision the directory for that effective UID:

```sh
sudo install -d -m 0700 -o 10001 -g 10001 /secure/nerocd-backups
```

Rootless engines can map container UID 10001 to a different host UID. In that case,
create the bind directory with the engine's documented UID mapping, then verify that
`/backups` is owned by UID 10001 and mode `0700` inside the tools container before
backing up:

```sh
docker compose --env-file /secure/nerocd-production.env -f compose.production.yaml \
  --profile tools run --rm -T --entrypoint /bin/sh \
  -v /secure/nerocd-backups:/backups database-tools \
  -ec 'test "$(id -u)" = 10001; test "$(stat -c %u:%a /backups)" = 10001:700'
```

Do not change ownership of the operator-owned credential inputs. The tools service
receives only the migration-owner secret and no proxy network; never mount the Docker
socket.

```sh
docker compose --env-file /secure/nerocd-production.env -f compose.production.yaml \
  --profile tools run --rm -T \
  -v /secure/nerocd-backups:/backups \
database-tools backup --output-dir /backups
```

## Off-host export and verification

The local archive volume is on the same host as PostgreSQL. For host-loss recovery,
mount an operator-managed encrypted destination on another machine, removable media, or
a backup service, then export a completed archive. NeroCD does not require a cloud
vendor or receive credentials for that destination. It verifies the private manifest,
exact dump checksum, and archive shape before copying, then verifies the staged copy
before atomically publishing it with `0700` directory and `0600` file permissions.

```sh
mkdir -m 0700 /mnt/off-host/nerocd
nerocd backup-export \
  --input-dir /secure/nerocd-backups/backup-YYYYMMDDTHHMMSSZ \
  --output-dir /mnt/off-host/nerocd
nerocd backup-verify \
  --input-dir /mnt/off-host/nerocd/export-YYYYMMDDTHHMMSSZ
```

Perform this export after every scheduled backup and retain the exact NeroCD image
digest that created it. At least quarterly, create an isolated empty PostgreSQL target
on a different host, run `backup-verify`, then use `restore` against that disposable
target and confirm the restored schema ledger. Runner file content is intentionally
never copied; restore that material separately from its own encrypted backup before
performing the database restore.

## Local backup schedule

This schedule protects the control-plane database. It is separate from hosted resource
schedules configured through the application UI.

The production profile also contains a separate, non-root `backup-scheduler` service. It
is disabled by default: set `NEROCD_BACKUP_SCHEDULE_ENABLED=true` only after
provisioning the private `backup-data` volume and an approved local collection
procedure. The scheduler has only the migration-owner credential, the internal database
network, and that one owner-only volume; it cannot be called through the API or browser.

It uses PostgreSQL time and a session advisory lock, so a restart continues the stored
next-run time and concurrent scheduler containers cannot overlap. A missed interval
causes at most one catch-up archive. Failed runs back off from one minute up to the
configured interval, with a durable cap of eight failures. Successful runs retain only
the configured number of newest canonical backup directories; unsafe, unexpected, or
symlinked archive content stops rotation without deleting anything further. The manual
`backup` and `restore` commands remain unchanged and are still the procedure for an
immediate operator action.

`/api/v1/operations/status` and protected `/metrics` expose only aggregate schedule
state (`disabled`, `waiting`, `due`, `running`, or `backoff`), time to the next attempt,
and the bounded failure count. They never expose archive paths, database URLs,
runner-file values, or tool diagnostics. Investigate a `backoff` state by examining the
private scheduler container logs, repairing the local destination, and allowing the next
DB-clock attempt; do not manually delete unknown files from a backup directory.

## Restore

Restore only into a newly created, empty database. The command verifies the manifest
checksum and exact migration ledger before invalidating every active session and runner
lease. In the same transaction, it settles all interrupted work, including leases with
future deadlines and work spanning multiple expiry batches. Generic runs return to the
queue with their prior deployment attempts failed. Build runs fail and their builds
require cleanup with `authority_lost`. A deployment or cleanup operation that holds a
binding requires reconciliation; its run fails and the mutation hold remains. Binding
and proxy restore holds still require explicit recovery admission. Operators must issue
fresh credentials and reconcile uncertain host effects before resuming work.
It never uses `--clean` against a live target. Restore requires both
`--allow-disposable-target` and an exact `--confirm-target-database` name. Before
`pg_restore`, NeroCD holds a restore-admission advisory lock, rejects any other active
DB session, and rejects non-public schemas and objects in `public` or another user
schema. It does not terminate sessions for you.

```sh
createdb nerocd_restore
nerocd restore --database-url "$NEROCD_RESTORE_DATABASE_URL" \
  --input-dir /secure/nerocd-backups/backup-YYYYMMDDTHHMMSSZ \
  --allow-disposable-target --confirm-target-database nerocd_restore \
  --runner-file-root /secure/runner-files
```

For production Compose, make a separate restore environment outside the checkout. It
needs a distinct project name, PostgreSQL password secret, owner URL secret, app URL
secret, owner role, app role, and proxy-network name. Set the owner URL to the isolated
project's `postgres` host and its `nerocd` database; omit `NEROCD_DATABASE_URL`
entirely. `compose.production.yaml` hardcodes that database name, so confirming `nerocd`
is safe only because this is a separate cluster, network, and volumes.

`/secure/nerocd-restore.env` must explicitly set `NEROCD_IMAGE`, `NEROCD_PROXY_NETWORK`,
`NEROCD_PUBLIC_ORIGIN`, `NEROCD_OWNER_DATABASE_USER`, `NEROCD_APP_DATABASE_USER`,
`NEROCD_DATABASE_URL_SECRET`, `NEROCD_APP_DATABASE_URL_SECRET`, and
`NEROCD_POSTGRES_PASSWORD_SECRET` for the restore project. It must name distinct restore
secret files and roles, not the production inputs. Source it in a subshell so values
exported earlier from the production environment cannot override Compose's restore
inputs.

Start only the isolated PostgreSQL service and its required secret/data initializers. Do
not run `up` without a service name: that would run migration or server services against
the empty target.

```sh
(
  set -a; . /secure/nerocd-restore.env; set +a
  unset NEROCD_DATABASE_URL
  docker compose --project-name nerocd_restore_20260905 \
    --env-file /secure/nerocd-restore.env -f compose.production.yaml up -d postgres
  docker compose --project-name nerocd_restore_20260905 \
    --env-file /secure/nerocd-restore.env -f compose.production.yaml \
    --profile tools run --rm -T \
    -v /secure/nerocd-backups/backup-YYYYMMDDTHHMMSSZ:/restore:ro \
    -v /secure/runner-files:/runner-files:ro \
  database-tools restore --input-dir /restore --runner-file-root /runner-files \
    --allow-disposable-target --confirm-target-database nerocd
)
```

The restore command itself rejects non-empty targets and other active database sessions
before `pg_restore`. After its checks and restore finish, stop and remove this restore
project; do not reuse its database for production.

## Lease clock and development tests

PostgreSQL uses `clock_timestamp()` for lease creation, renewal, expiry and completion.
Caller timestamps cannot extend production authority. Restore revokes active authority
regardless of its deadline, without changing its attempt or fence.

The disposable memory store uses caller time for deterministic tests. It matches the
running-run expiry and deployment-attempt transitions, including failure of malformed
build tasks. It does not store hosting builds, operations or binding mutation holds and
cannot validate their recovery. Use PostgreSQL integration tests for build cleanup and
held-operation reconciliation.

## Runtime checks

- `/api/v1/health` reports process liveness.
- `/api/v1/ready` checks that the configured store is reachable.
- `/metrics` exposes bounded-cardinality plain-text request counters and
  aggregate latency only to authenticated `system_admin` callers. In production
  it remains behind the approved proxy network; never publish it as a host port.
