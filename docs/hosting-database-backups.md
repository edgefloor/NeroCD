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
error. There is no automatic deletion or remote retention policy in this slice.
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

## Deliberate limits and upstream reference

Scheduling, S3 publication, retention automation, other database engines, arbitrary
SQL uploads or URLs, in-place replacement, cross-destination restore, and backup of
PostgreSQL roles are deferred. These gaps must remain visible when assessing full
hosting parity. No A25/S3 qualification is implied.

The pinned Coolify reference (`b12d1386f3d458618b4c579babcd1e339c6c40b3`)
uses `app/Jobs/DatabaseBackupJob.php` for PostgreSQL custom dumps and separate
local/S3 publication, the scheduled-backup models for history, and
`app/Support/DatabaseImport/DatabaseImportCommandBuilder.php` for transactional
PostgreSQL import. NeroCD reuses those format and tool semantics and the useful
history/import workflow. Persisted Go-agent fencing, exact creation provenance,
protected local artifacts, and a fresh independent target replace SSH commands
and cache locks. This is a narrower recovery product than the upstream import
surface.

## Accepted qualification and evidence

The bounded workflow is accepted on main at
`56cce329e620472e3c82e9ed7ef0ae0618f124e4`, with migration 0075 and runner
journal format 18. The final physical gate ran at source
`c1f7b153a963609336851f94a16b2eac96f86e91`. Integration retained that evidence
by checking all 141 recorded source blobs and modes. Its runtime, tests, gates,
SQL, UI, module and configuration inputs are unchanged. Fresh integration
binaries carry different Git metadata; no binary-equality claim is made.

The physical profile uses native Linux arm64, trusted rootful Docker with the
classic image store, PostgreSQL 17.11, a same-destination public-image client
application and fixture-CA HTTPS. A real cookie-authenticated Chromium session
requests the backup and new-database restore through the embedded UI. The Go
fixture checks the resulting application data. Accounts, environment and
destination are fixture setup; this is not a complete onboarding qualification.

The accepted outcomes are:

- An owner requests an immediate backup and reads its completed local history.
  The live browser checks the completed operation at 320, 390, 430 and 1440 pixels.
- Backup and restore completion acknowledgements are lost after commit. The
  compiled runner restarts and replays each durable result without repeating
  the database tool.
- Restore succeeds while the original source container is stopped. The target
  has a distinct resource, binding, volume and generated credential identity.
  A distinct application reads the pre-backup record and writes and reads new
  data. A restored application-owned sequence remains usable under `ncd_app`.
- The source restarts with its original and post-snapshot records intact and
  accepts another write. This source restart is fixture cleanup, not a restore
  requirement or an automatic source-management feature.
- Corrupt and truncated artifacts and revoked authorization fail. The real
  `pg_restore` guest timeout is exercised. Failed or uncertain effects preserve the
  appropriate hold, rather than fabricating a successful restore.

The behavior assertions live in
[`TestHostingDatabaseBackupCompiledRunner`](../internal/api/hosting_database_backup_runtime_integration_test.go),
`TestHostingDatabaseBackupFailuresCompiledRunner` in that same file, and the
[live browser scenario](../web/app/tests/browser/database-backups-live.spec.ts).
The [dedicated gate](../acceptance/runtime-hosting-postgresql-backup-runner/gate.sh)
runs these against the compiled runner and embedded UI.

Durable receipts are under
`/Users/bubble/.local/share/nerocd/acceptance/postgresql-backup-20261025/`:

| Receipt | Observed result and boundary |
| --- | --- |
| `thirteenth-runtime/driver.json` and `runtime-summary.json` | Final source `c1f7b153a963609336851f94a16b2eac96f86e91`, exit 0, `full_workflow_pass=1`, 130.140 seconds. Browser, application restore, restore guest timeout and corrupt/truncated/revoked markers passed. |
| `thirteenth-runtime/independent-cleanup.json` | Owned outer container and volume absent. Existing container inventory and full volume profiles preserved. Health-log and mount-order differences are explicitly reconciled. |
| `integration/handoff.json`, `sourceproof.json` and `retention.json` | Accepted source correspondence, 157 focused named passes with zero skips, vet, API generation, frontend and product builds, native/Linux binaries and embedded artifact checks passed. Physical and PostgreSQL results retain their original revisions. |
| `integration/main-promotion.json` | Fast-forward to accepted `56cce329e620472e3c82e9ed7ef0ae0618f124e4` succeeded. The existing user UI process and its database were not restarted or migrated. |

Earlier failed attempts, including `qualification-handoff.json`, remain failed
historical evidence. They do not replace the final thirteenth pass. Selected
measured durations are 56.94 seconds for the main runtime test, 41.05 seconds
for its separate failure group, and 22.543 seconds for the integrated focused
race checks. These overlap or cover different scopes and must not be summed
as total delivery time. Receipts do not establish total elapsed effort, a total
review-round count, token use or monetary cost.

The [machine-readable checklist](parity-checklist.json) records ten later
bounded outcome assessments under `supplemental_assessments`. Nine are validated.
R46-C06 remains partial: shared backup/restore timeout handling is implemented,
but the physical fixture proves only `pg_restore` timeout. A forced `pg_dump`
backup timeout remains unverified. Its historical
`09a432407c41671517fa342d28c07e9c79978aea` audit and 933 eligible outcomes remain
unchanged. The later assessments add no historical score or full requirement,
acceptance or matrix gate. Scheduled PostgreSQL backup, S3 and retention remain
open, as do in-place restore, cross-destination recovery and other engines.
