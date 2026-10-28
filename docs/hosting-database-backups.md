# Hosted database logical backups

Project owners can request an immediate logical backup from a healthy hosted
PostgreSQL or MySQL database and restore a completed backup into a new private
managed database. Project viewers can read the nonsecret history. This family is separate
from backups of NeroCD's own controller database and from upload-volume archives.

The database panel offers **Back up now**, completed backup selection, a new
database name, and **Restore into new database**. History includes the operation
state, saved byte and timeout limits, archive digest and size, and the new target
resource identity. It never includes credentials, local filesystem locations, or
runner commands. A completed restored database can be selected when connecting a
distinct application; it does not replace an existing database binding.

## PostgreSQL snapshot and restore behavior

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

## MySQL local backup and restore

The native MySQL recipe is `mysql-8.4.13-oraclelinux9.v1`. Its immediate or
scheduled local backup and separate new-database restore require `hosting_mysql_backup_v1` and
`hosting_mysql_v1`, with the existing database backup runner opt-in. Use **Back up
now**, select a completed backup, enter a **New database name**, and choose
**Restore into new database**.

The backup records version `mysql.logical-local.v1` and blob format `mysql_sql`.
It captures the `app` database with an InnoDB transaction snapshot while the source
remains writable. The runner refuses non-InnoDB tables before dumping and does
not promise consistency for concurrent DDL. Source users and passwords are excluded. The dump is
bounded and published through the same protected local artifact store.

Restore creates a separate MySQL resource with the same recipe, distinct generated
credentials, and a new owned volume on the backup's authorized destination.
PostgreSQL archives cannot be restored into MySQL, and MySQL SQL dumps cannot be
restored into PostgreSQL or Redis. The target stays unavailable for application
connections until import and readiness succeed. Failed, canceled, or uncertain
imports retain target ownership. Reconciliation must not replay an import whose
effects are unknown.

MySQL supports immediate and [scheduled local backups](hosting-database-backup-schedules.md#schedule-native-mysql-backups).
[Remote copies](hosting-database-remote-backups.md#native-mysql-remote-copy-qualification)
can upload the completed MySQL artifact. Retention policies remain PostgreSQL-only.
For local restore, preserve the archive on its original backup runner. A saved
remote upload can instead supply [MySQL remote-only restore](hosting-database-remote-backups.md#native-mysql-remote-only-restore-qualification)
into a separate target. Database archives do not restore application uploads.
This workflow does not import arbitrary uploaded SQL, overwrite
a live database, upgrade an engine, or move data across destinations.

## Limits and local retention

Protected server options `--database-backup-max-bytes` and
`--database-backup-timeout-seconds` default to 1 GiB and 900 seconds. The byte bound
must be positive (at least the five-byte format header) and cannot exceed 1 GiB;
the timeout must be 1 through 3600 seconds. Invalid settings prevent startup.
Ordinary API requests cannot override them. Each admission saves its limits
immutably, and changing server settings does not rewrite accepted operations.

The agent verifies the exact existing container, image, daemon, private network,
process user, owned volume and creation provenance. PostgreSQL tools run inside
the pinned container as the `postgres` Unix user through its local socket. MySQL
tools run as the `mysql` Unix user with a protected local credential file. Neither
adapter transports passwords in command arguments or environment variables.
Each guest tool has an explicit timeout in addition to the fenced local attempt.
Stopping the local Docker client does not establish that a guest import stopped.
Uncertain effects therefore retain ownership for reconciliation.

Dump bytes are bounded, synced to a private staging file, and published through
the existing content-addressed artifact store. Publication checks digest, size,
owner, file type and link count and does not overwrite an existing artifact.
A completed staging dump is retained if publication fails. The public history
records `publication_failed`; this is a local publication failure, not an S3
error. For PostgreSQL, configure copy cleanup separately through the
[retention policy](hosting-database-backup-retention.md).
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
`hosting_postgresql_backup_v1` and `hosting_mysql_backup_v1`. River carries only
the operation ID. The normal run/lease/attempt/fence system authorizes execution; backup is not a separate
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

Use [schedules](hosting-database-backup-schedules.md) for recurring PostgreSQL or
native MySQL local dumps. Use [remote backups](hosting-database-remote-backups.md)
for recorded PostgreSQL or MySQL S3 copies. PostgreSQL also supports
[retention](hosting-database-backup-retention.md) for copy cleanup. Both engines
support [notifications](hosting-backup-notifications.md) for accepted backup and
upload failures.

Only the pinned PostgreSQL and MySQL recipes are supported here. Restore creates
a new database on the same destination. It does not accept arbitrary SQL uploads
or URLs, import source roles, replace a live database, or move data across destinations.

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
That PostgreSQL qualification does not establish other engines, architectures,
production ACME, cross-host recovery, or destination disk loss.


## MySQL qualification

At `f2b4e471` on 2026-10-28, the native MySQL workflow published a local
`mysql_sql` backup and restored it into a distinct same-recipe resource. The
backup browser scenario passed. Direct database checks and application reads and
writes verified the recovered records and source/target isolation. The connected
application retained records, uploads, and trusted HTTPS through redeployment
in the [same qualification](hosting-database-connections.md#native-mysql-qualification).

This profile covers immediate local backup and new-resource restore on native
Linux arm64 with one destination and fixture-CA HTTPS. It does not qualify
MySQL schedules, remote copies, retention policies, credential rotation, engine
upgrades, arbitrary SQL import, or cross-host recovery. PostgreSQL schedule,
remote-copy, retention, and timeout evidence above retains its original scope.

The [evidence index](/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-mysql-20261028/f2b4e471df39b5483326cba4b09e0134328dc5b7/evidence-index.json)
records the qualified revision, source checks, browser and runtime assertions,
retained earlier evidence, and verified fixture cleanup.

Native MySQL now has a separate [local and remote retention qualification](hosting-database-backup-retention.md#native-mysql-retention-qualification).


[Native MySQL schedules](hosting-database-backup-schedules.md#native-mysql-schedule-qualification)
have separate qualification at `8d85d70b`, including restore from the exact
scheduled artifact. The earlier immediate-backup evidence above retains its scope.
