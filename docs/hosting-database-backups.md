# Hosted PostgreSQL logical backups

Project owners can request an immediate logical backup from a healthy hosted
PostgreSQL database and restore a completed backup into a new private managed
database. Project viewers can read the nonsecret history. This family is separate
from backups of NeroCD's own controller database and from upload-volume archives.

The database panel offers **Back up now**, completed backup selection, a new
database name, and **Restore into new database**. History includes the operation
state, saved byte and timeout limits, archive digest and size, and the new target
resource identity. It never includes credentials, local filesystem locations, or
runner commands. A completed restored database can be selected when connecting a
distinct application; it does not replace an existing database binding.

## Snapshot and restore behavior

The supported recipe is the pinned PostgreSQL 17.11 application database. A backup
uses PostgreSQL custom format with ownership and ACL export disabled. The source
remains writable: PostgreSQL provides one consistent logical snapshot, rather
than a stop-the-world copy or a promise to include later writes. NeroCD holds
source maintenance, credential changes, and replacement while the live dump owns
that source. Application reads and writes remain available.

Restore creates a new resource, binding, owned volume, and generated encrypted
credentials on the backup's same authorized destination. The dump is imported
with `pg_restore --exit-on-error --single-transaction --no-owner --no-acl` under
the target's `ncd_app` role. Source roles and passwords are not imported. The new
target remains unavailable for public deployment or application connection until
import and readiness complete. A failed or uncertain import retains that target's
hold; it does not hold the original source or take its runtime lock.

A completed backup preserves source provenance but does not require the original
source container to remain running, or its old credential versions to remain
current. Restore checks current project ownership, destination grants, runner and
daemon identity, the immutable local artifact, and the exact held target. It does
not stop or mutate the source. The destination and retained runner storage must
still exist. This first version cannot move a dump to another destination.

## Limits and local retention

Protected server options `--database-backup-max-bytes` and
`--database-backup-timeout-seconds` default to 1 GiB and 900 seconds. The byte bound
must be positive (at least the five-byte format header) and cannot exceed 1 GiB;
the timeout must be 1 through 3600 seconds. Invalid settings prevent startup.
Ordinary API requests cannot override them. Each admission saves its limits
immutably, and changing server settings does not rewrite accepted operations.

The agent verifies the exact existing container, image, daemon, private network,
process user, owned volume and creation provenance. It invokes argument arrays
inside the pinned container as the `postgres` Unix user using its local socket;
credentials are not transported in command arguments or environment variables.
Each guest tool has an explicit timeout in addition to the fenced local attempt.
Stopping the local Docker client does not establish that a guest import stopped.
Uncertain effects therefore retain ownership for reconciliation.

Dump bytes are bounded, synced to a private staging file, and published through
the existing content-addressed artifact store. Publication checks digest, size,
owner, file type and link count and does not overwrite an existing artifact.
A completed staging dump is retained if publication fails. The public history
records `publication_failed`; this is a local publication failure, not an S3
error. Configure copy cleanup separately through the [retention policy](hosting-database-backup-retention.md).
Operators must preserve the runner's protected state and storage; a local backup
is not protection against loss of that destination.

## Requests, replay and unresolved execution

Public API paths are:

- `POST /api/v1/resources/{id}/database-backups`, with `request_id` and
  `expected_resource_generation`;
- `GET /api/v1/resources/{id}/database-backups` and
  `GET /api/v1/database-backups/{id}` for safe history/status;
- `POST /api/v1/database-backups/{id}/restores`, with `request_id` and `name`.

Retry an ambiguous request with its original request ID and unchanged body. An
admission replay returns the same operation and, for restore, the same target.
The UI retains the request for an explicit retry after a transport failure and
allows correction after a definitive input rejection.

The runner requires `--hosting-database --hosting-database-backups` and advertises
`hosting_postgresql_backup_v1`. River carries only the operation ID. The normal
run/lease/attempt/fence system authorizes execution; backup is not a separate
weaker queue. A restore deployment child can cross its target hold only through
its saved exact parent relationship.

The durable journal records an intent before tool execution and a result before
sending completion. A lost HTTP acknowledgement replays the exact saved result
and retires evidence after acknowledgement, without repeating the tool. An intent
without a durable result cannot establish whether an import committed before a
crash. Such an execution remains visibly unresolved and held; it is never inferred
successful or blindly imported again. This version does not offer a general
administrative resolution for that crash window.

## Related workflows and limits

Use [schedules](hosting-database-backup-schedules.md) for recurring dumps,
[remote backups](hosting-database-remote-backups.md) for recorded S3 copies,
[retention](hosting-database-backup-retention.md) for copy cleanup, and
[notifications](hosting-backup-notifications.md) for failure alerts.

Only the pinned PostgreSQL recipe is supported here. Restore creates a new database
on the same destination. It does not accept arbitrary SQL uploads or URLs, import
source roles, replace a live database, or move data across destinations.

<a id="accepted-qualification-and-evidence"></a>
<a id="deliberate-limits-and-upstream-reference"></a>

## Tests and supported environments

The [runtime tests](../internal/api/hosting_database_backup_runtime_integration_test.go)
and [live browser scenario](../web/app/tests/browser/database-backups-live.spec.ts)
cover immediate backup, new-database application reads and writes, response-loss
replay after runner restart, corrupt and truncated archives, revoked authority,
and guest `pg_restore` timeout. The [schedule workflow](hosting-database-backup-schedules.md)
also tests actual `pg_dump` timeout. History layouts are checked at 320, 390, 430,
and 1440 pixels.

The tested profile is native Linux arm64, PostgreSQL 17.11, trusted rootful Docker
with the classic image store, one destination, and private fixture-CA HTTPS.
Other engines, architectures, production ACME, cross-host recovery, and destination
disk loss are unqualified.
