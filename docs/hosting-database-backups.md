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
can upload the completed MySQL artifact. Native MySQL also supports
[independent local and remote retention](hosting-database-backup-retention.md#native-mysql-retention-qualification).
For local restore, preserve the archive on its original backup runner. A saved
remote upload can instead supply [MySQL remote-only restore](hosting-database-remote-backups.md#native-mysql-remote-only-restore-qualification)
into a separate target. Database archives do not restore application uploads.
This workflow does not import arbitrary uploaded SQL, overwrite
a live database, upgrade an engine, or move data across destinations.

## Compose MySQL local backup and import

For the pinned WordPress/MySQL stack, use the application's **Database backups**
panel and select the healthy MySQL component. The runner requires
`hosting_compose_mysql_backup_v1`; this is separate from native MySQL resource
backups. The saved version is `mysql.compose-logical-local.v1`, with `mysql_sql`
format and the pinned MySQL 8.4.13 profile.

Select a completed local backup and a distinct, healthy stack's MySQL component
on the same authorized destination. Review the target deployment and resource
generation, then explicitly acknowledge replacement before importing. A changed
target invalidates unsubmitted consent. A submitted retry keeps the original
request and target evidence.

Import changes the target database only. It does not restore WordPress files or
rewrite the site's URL. Once target SQL execution is attempted, a failed or uncertain outcome
retains target ownership and stopped writers; MySQL does not provide the PostgreSQL adapter's transactional
rollback guarantee. [Local component schedules](hosting-database-backup-schedules.md#compose-mysql-schedule-qualification)
use the same archive/import path. [Immediate remote upload](#compose-mysql-immediate-remote-upload-qualification)
can copy a completed SQL archive to a validated target. [Compose MySQL remote schedules](hosting-database-backup-schedules.md#compose-mysql-remote-schedule-qualification)
use that same upload path. [Recorded remote import](#compose-mysql-recorded-remote-import-qualification) recovers a separate target from a successful upload. [Component retention](hosting-database-backup-retention.md#compose-mysql-retention-qualification) supports independent automatic local/remote limits and survivor recovery. Arbitrary S3-path and staged local imports remain unsupported. See the [local qualified scope](#compose-mysql-qualification).

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
error. For native PostgreSQL and MySQL, configure copy cleanup through the
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
for recorded PostgreSQL or MySQL S3 copies. Native PostgreSQL and MySQL support
[retention](hosting-database-backup-retention.md) for independent local and remote
copy cleanup. Both engines
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

## Compose MySQL qualification

At `44f9b1d5` on 2026-10-29, the catalog WordPress/MySQL workflow completed a local
SQL backup and import into a separate stack. Target snapshot reads and independent
writes verified database isolation through the exact target HTTP endpoint using
the source Host context. Source posts and the complete uploaded PNG remained
available through trusted fixture-CA HTTPS, redeployment and Stop/Start. This does
not establish target cross-hostname HTTPS, URL migration or media restoration.

Corrupt archive rejection occurred before SQL effects. It does not promise
uninterrupted target writers. A separate failure dropped a target table and then
denied CREATE; the exact target's writers stayed stopped and ownership stayed
held. The runner exited naturally with code 1, retaining the matching intent and
uncertain journal entries. No additional run was acquired. No physical restart or
reimport was attempted; earlier replay tests retain their own scope. Current
credential scans and responsive browser controls at 320, 390, 430 and 1280 pixels
passed.

The selected test passed in 177.21 seconds; the unchanged gate completed in
269.441 seconds with all four required markers. The [evidence index](/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-compose-mysql-20261029/44f9b1d5836fa589f5f82da70c15494aceed80bc/evidence-index.json)
retains all 17 attempts, including earlier setup, browser and HTTPS failures,
explicit diagnostic cancellation, exact source associations and cleanup. Owned
containers and their Docker volume were removed. Host container/volume, image,
build-cache and user-instance baselines were preserved.

The proxy repair binds a named 10-second HTTP idle transport to each generation,
with exact configuration/readback and historical-hash recovery checks. A focused
109.095-second diagnostic reproduced failure on a new connection left unused for
17 seconds. The separate 57.732-second standalone transport proof failed before
retaining its first arm, so it does not independently prove expiry or a
13-second active response. Implementation and fixture-repair exclusive durations
were not measured; command and review intervals remain separate in the index.
Inherited full-lint failures remain explicit: seven native findings and one Linux
finding. Passing changed-code lint does not replace those results.

Qualification covers one Linux arm64, rootful Docker destination and fixture CA.
That qualification did not cover schedules. [Local Compose MySQL schedules](hosting-database-backup-schedules.md#compose-mysql-schedule-qualification)
passed later at `4c62e6cf`. [Immediate remote upload](#compose-mysql-immediate-remote-upload-qualification)
passed at `0c5ff34c`; [Compose MySQL remote schedules](hosting-database-backup-schedules.md#compose-mysql-remote-schedule-qualification) followed at `3f451479`.
Recorded remote import was qualified later at `b36972a8`, and [component retention](hosting-database-backup-retention.md#compose-mysql-retention-qualification) at `be5ecd83`. Engine upgrades,
credential rotation and cross-destination recovery remain open. Historical native
MySQL and core WordPress qualifications retain their original limits.

## Compose MySQL immediate remote upload qualification

At `0c5ff34c` on 2026-10-29, the existing WordPress/MySQL workflow selected a
validated S3 target for an immediate component backup. The target must be enabled
and validated at its current revision for the current destination. The existing
`remote_target` fields and `mysql.logical-s3-transfer.v1` protocol are reused;
the runner requires the database remote-backup opt-in and MySQL upload capability.

An actual PUT denial left the local `mysql_sql` backup succeeded and released
source ownership. The browser retried the failed transfer by its exact ID. A new
transfer uploaded the same backup, blob digest, object key, daemon and target
revision; the object SHA matched and no second database dump was admitted.
The scheduled local archive still supplied the separate-stack import. Source
post/full-PNG preservation through trusted HTTPS, redeploy and Stop/Start,
corrupt-archive rejection, and held partial-import safety also passed. The runner
exited naturally with code 1 after the deliberately uncertain import, retaining
exact intent/uncertain authority and no subsequent acquired run. No physical
restart or reimport was attempted.

The selected test passed in 192.54 seconds; the gate exited zero after 289.867
seconds with all six markers. Browser remote selection, failure/local success and
same-artifact retry flags passed without interception. Responsive checks at
320, 390, 430 and 1280 pixels and served-asset hashes passed. The credential scan
covered the three source managed values and two S3 credential values in the
journal, public operation metadata and runner logs.

The [safe evidence index](/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-compose-mysql-20261029/0c5ff34cdcb7be1d2d8a3403b00aa61765a16d82/evidence-index.json)
records source/build associations, focused migration upgrades/checksum replay,
authority and transfer tests, earlier test/setup failures, and exact cleanup.
The owned container and Docker data volume were removed; host container, volume,
image, build-cache and user-instance identities matched the baseline. Command
intervals are recorded separately from implementation, product repair, fixture
repair and review/waiting; exclusive durations were not measured. A Linux CLI
contract attempted in an unsuitable PostgreSQL-container host profile failed and
is retained as setup evidence, not a CLI pass. The actual compiled runner options
were exercised by the passing gate. Qualification 18's original cause remains
unclassified; prior accepted qualifications retain their own evidence and limits.

This covers one Linux arm64, rootful Docker destination with trusted-loopback
Silo and fixture-CA source HTTPS. Remote schedules were qualified separately
at `3f451479`; see [Compose MySQL remote schedules](hosting-database-backup-schedules.md#compose-mysql-remote-schedule-qualification). This immediate-upload proof does not qualify
remote-only restore, retention, media restoration, URL migration,
public S3 providers or cross-destination recovery.

## Compose MySQL recorded remote import qualification

At `b36972a8` on 2026-10-30, the browser selected the successful recorded upload
of the scheduled `mysql_sql` archive and an explicitly acknowledged separate
MySQL component target. The fixture verified and removed the exact local CAS
archive before import. The download retained the recorded upload, blob, target
revision, destination and runner authority. The restored target served the saved
post and an independent new write; source post/full-PNG preservation passed.
This does not restore media or migrate URLs.

With local CAS absent again, a deliberately corrupted recorded S3 object failed
with `object_mismatch`. No SQL import run or intent was created. The restore kept
target ownership with `uncertain`/`execution_failed`, and target writers remained
running with their original row. The separate partial-SQL case still held stopped
writers after DROP succeeded and CREATE was denied. Natural runner exit, exact
journal authority, no subsequent acquired run, and credential scans passed.
No physical restart or reimport was attempted.

The shared dispatch barrier now waits for a successful remote download for both
PostgreSQL and MySQL component restores. Focused real PostgreSQL tests cover that
ordering, failed-download non-dispatch, local/remote capability denial, replay and
the populated `0106` to `0107` upgrade. All 107 prior filename versions retained
their bytes; the new migration gives 108 files across 107 numeric prefixes.

The selected test passed in 190.47 seconds; the gate exited zero after 281.070
seconds with all eight markers. Browser controls at 320, 390, 430 and 1280 pixels
and served-asset hashes passed without interception. The [safe evidence index](/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-compose-mysql-20261029/b36972a8d519342889a61b366374ad200f0a4374/evidence-index.json)
retains exact source/build/check associations, the invalid failure-code fixture
attempt and usage-only Bun commands, followed by their actual passing checks.
It separates the download-barrier product repair from fixture repair, implementation,
test execution and review/waiting. Exclusive durations are unknown; the overnight
gap is not counted as active work. Exact host baselines matched after owned cleanup.
Qualification 18's original cause remains unclassified.

This covers recorded successful uploads on one Linux arm64 rootful destination,
trusted-loopback Silo and fixture-CA source HTTPS. Arbitrary S3-path import, staged
local SQL import, public S3 providers and
cross-destination recovery remain outside this qualification.

[Compose MySQL retention](hosting-database-backup-retention.md#compose-mysql-retention-qualification) was qualified later at `be5ecd83`; the recorded-import evidence above retains its original scope.


## External S3 Compose MySQL import

A project owner can import an operator-provided plain or gzip SQL object into a
separate healthy, qualified Compose MySQL target. No existing NeroCD backup or
recorded upload is required. This is database-only replacement, not media restore
or URL migration.

1. In the source application's database controls, choose external S3 as the
   archive source and select the separate MySQL target.
2. Select an enabled, currently validated S3 destination and enter the literal
   object key. The key must contain 1–1024 UTF-8 bytes without control characters.
   NeroCD does not trim, normalize or URL-decode it. The S3 principal's policy
   still controls access to that exact key.
3. Select **Inspect S3 object**. Review the encoded size, detected compression,
   decoded SQL size and digest, intended target, and confirmation deadline.
4. Acknowledge replacement and confirm while the inspected stage is ready.

The runner requires `--hosting-database-external-imports`, database execution,
runtime-environment delivery and a durable journal, plus the existing permitted
S3 endpoint/network policy. Inspection performs one protected GET using the
configured S3 credentials. It detects gzip by magic bytes, verifies the complete
stream and checksum, and limits both encoded and decoded data to 10 GiB. An ETag
or provider version is recorded when supplied; neither substitutes for the
computed SQL digest.

Inspection creates a dedicated immutable private stage, separate from shared
backup CAS. Its receipt binds the source revision/key, observed identity, byte
counts, digest, target deployment/daemon and expiry. The one-hour ready deadline
applies to new confirmation. An import committed before that deadline reserves
the bytes for queued execution and recovery. Controller-authorized cleanup
removes only exact expired unconsumed or completed owned stages; it preserves
consumed reservations and unknown authority. Missing or replaced bytes require
fresh inspection, never an implicit refetch.

SQL executes as the target's configured application account, never root.
The pinned noninteractive client disables local infile, reconnect and client
commands. Current authority and target fences are rechecked before effects.
Inspection failures leave writers running. After SQL may have executed, an
uncertain import retains target ownership and stopped writers for recovery.
Exact request replay returns the original operation after current authorization,
even after expiry or S3 unavailability. Journal replay does not repeat SQL.

### External S3 import qualification

At `7a1ca1b2` on 2026-10-30, qualification28 passed with all twelve markers,
a selected-test time of 213.56 seconds and gate exit zero after 301.431 seconds.
The actual browser imported independent plain and magic-detected gzip objects,
with no backup/upload provenance. SQL recorded `CURRENT_USER()` and matched the
configured nonroot application account. Identical API replay admitted no second
import. Truncated gzip failed inspection before SQL or writer interruption.
Existing post/full-PNG, redeploy, Stop/Start, schedule, retention, corrupt-download
and partial-SQL safeguards passed. Responsive and served-asset checks passed
without interception; credential scans and exact owned cleanup passed.

The [safe evidence index](/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-compose-mysql-20261029/7a1ca1b28292a6ca69c9e7edee4af3f86e4f6681/evidence-index.json)
retains qualification27's failed XML-only fixture observation and unknown original
HTTP status, followed by the actual SDK reproduction and bounded repair. The
original combined `make test` failure remains recorded with repaired schema/SQLC
contracts and passing affected packages. Lock tests cover concurrent requests,
not forced lock interleavings. Replay here is authenticated API replay, not a
physical ambiguous-response UI retry. The focused actual-client partial-SQL test
uses a deterministic writer seam; the full run physically covers the existing
generated-dump partial-import hold. No physical restart/reimport was attempted.

This qualification covers one Linux arm64 rootful Docker destination,
trusted-loopback Silo and fixture-CA HTTPS. Staged local file import, public S3
providers, cross-destination recovery, engine upgrades and full Coolify parity
remain open. Earlier qualifications retain their own scope and limits.
