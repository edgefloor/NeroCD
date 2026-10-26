# Recurring upload backups

Use **Project hosting**, select the project and environment, then open the image
application's upload panel. Create owned local upload storage and deploy the
application before scheduling its archives. The runner needs the qualified
storage archive capability and pinned archive helper configuration.

In **Recurring upload backups**, enter a five-field cron expression or a supported
alias and an explicit IANA timezone. Select the remote target above the form to
pin its validated revision. Leave that selector empty for a local archive.
Create the schedule, then use **View schedule** to edit or disable it and inspect
its occurrence history. **View scheduled archive** selects the ordinary archive
history and its existing retry, recovery, and restore controls.

A delivery error preserves the form and its original request identity. Use
**Retry identical schedule request** to recover the accepted result. Discarding a
saved request does not cancel an accepted schedule. Read current history before
you make a separate submission. Disable stops future discovery. It does not
cancel an admitted archive or clear its ownership hold.

## API contract

Owners create schedules with `POST /api/v1/resources/{id}/storage-backup-schedules`
and update them with `PUT /api/v1/resources/{id}/storage-backup-schedules/{schedule_id}`.
Viewers can read the collection and `/{schedule_id}/occurrences`. Every request
requires current project membership and destination access. Historical request
replay also requires current authorization.

The closed input requires `request_id`, `expected_resource_generation`,
`expected_version`, `frequency`, `timezone`, and `enabled`. Request IDs contain
32 lowercase hexadecimal characters. Create uses version zero. An update uses
the exact current revision. Optional `remote_target` contains `target_id` and
`revision`. Changed input under the same request ID conflicts.

Each revision retains its actor, source storage reference, binding, destination,
frequency, timezone, and selected remote revision. Public history contains safe
IDs, the UTC minute, admitted revision, state, finite reason, and held ownership.
It does not expose paths, credential references, lease fences, or arbitrary output.

## Timing and execution

The serial hosting recovery loop discovers at most 100 rotating enabled schedules
per pass. It uses the database clock and the existing newest-due timing helper.
The inclusive ten-minute window admits only the newest due minute. Supported
aliases and DST duplicate handling match database schedules without changing
database execution. Storage scheduling does not require database runner capability.

The cursor, immutable occurrence, archive plan, hold, audit, and River job commit
in one transaction. Concurrent passes serialize on the same schedule. A busy
source produces a skipped occurrence. Revoked authority produces a denied
occurrence. Neither result is a backup failure notification.

Execution uses the existing Go storage archive runner. Queue recovery republishes
only the original queued operation with no run. It does not rerun uncertain work,
replace runner authority, or import database schedule retirement rules.

Archive failure and upload failure enter the existing project SMTP and webhook
channels through `backup_failed` and `upload_failed`. Source kinds are
`storage_backup` and `storage_transfer`. Events retain exact accepted receipts.
An uncertain result remains uncertain, and its hold remains held. Upload failure
preserves the successful local archive. Restore, download, validation, and
discovery-only skips do not emit these alerts. Recovery links select authenticated
history and grant no authority.

## Reference mapping and qualification

The comparison reference is Coolify commit
`b12d1386f3d458618b4c579babcd1e339c6c40b3`.

| Reference behavior | NeroCD implementation |
| --- | --- |
| `app/Jobs/ScheduledJobManager.php::processScheduledVolumeBackup`, line 530 | Bounded storage schedule discovery and source-busy skip |
| `ScheduledJobDeliveryService::recordAndPublish`, lines 75–110, newest due with ten-minute catch-up | Shared cron timing helper and immutable UTC occurrence |
| `app/Models/ScheduledVolumeBackup.php` | Resource-specific schedule with immutable revisions and exact storage reference |
| `app/Http/Controllers/Api/VolumeBackupsController.php::upsert` and `run` | Owner schedule API plus existing immediate archive API and upload panel |
| `app/Jobs/VolumeBackupJob.php::handle` and `archiveWithStoppedContainers` | Existing fenced Go archive runner and durable writer resumption evidence |
| `VolumeBackupJob::uploadToS3` | Existing pinned storage transfer plan, credential delivery, and accepted receipts |
| `app/Notifications/VolumeBackup/BackupFailed.php` | Storage-specific messages through existing SMTP and webhook delivery policy |

NeroCD supports owned upload volumes, preserves local archives, and restores into
a new volume and resource. It uses agent authority and Go archive/S3 execution.
It does not execute the reference SSH or `mc` shell commands. Retention, arbitrary
host directories, S3-only streaming, and generalized task scheduling are outside
this workflow. Discovery-only skipped occurrences do not become failure alerts.

Run `bash acceptance/runtime-hosting-storage/gate.sh --remote` from the committed
candidate after authorization for the exact disposable target described in
[the runtime guide](../acceptance/runtime-hosting-storage/README.md). That gate
checks scheduled UI admission, receipt replay, real archive and upload failures,
SMTP/webhook delivery, and application reads and writes after new-volume restore.
Local tests and fixture compilation alone do not prove those physical outcomes.

## Accepted qualification and evidence

The Leader accepted production `733cde407427ef745d57fbb726e001bdb1555d47`, tree
`5be5cb2d7c937aaea00e76a8a8e128bd6c6fd77a`, after independent code hygiene,
specification, and critical security reviews at that exact candidate. The physical
`--remote` gate passed in 123.399 seconds. Ten PostgreSQL groups passed without
skips, including four target/key/actor/grant revocation cases. The local archive
group passed in 23.43 seconds and the scheduled remote group in 37.07 seconds.
These are observed run intervals, not feature labor or a performance comparison.

The authenticated owner browser creates a schedule, recovers its lost response
with the identical request, edits and disables it, and reads occurrence history.
The viewer reads that history without mutation controls. Concurrent due passes
admit one archive, and the controller stops, joins, and restarts before admission.
The fixture produces both an actual archive failure and an actual upload failure.
Each accepted source event reaches SMTP and the HTTPS webhook with stable event
and delivery IDs. The fixture extracts the recovery URL from each accepted
transport receipt, validates its exact origin and project/environment/resource/
backup/source association, and opens that delivered URL in the authenticated
browser. Both openings select the expected history and start no mutation.

The owner explicitly retries the successful local archive's upload, then restores
the remote archive into a new owned volume and resource on the same destination.
A deployed application reads archived uploads, writes and reads new data, and
leaves the source bytes and permissions unchanged. The first successful scheduled
archive and its upload retry use one archive helper and one successful PUT.
Those counts exclude the later intentional archive-failure schedule. Corrupt and
truncated downloads, permission metadata, resumption before upload, receipt replay,
and cleanup assertions remain part of the passing gate.

Qualification is native Linux arm64, trusted rootful Docker with the classic
image store and overlay2, one same-destination runner, and pinned Silo through
explicitly trusted numeric loopback HTTP. SMTP and webhook receivers use a private
CA and explicit fixture TLS origins. This does not qualify production S3 HTTPS,
public-CA deployment, arbitrary host mounts, storage retention, in-place or
cross-host/disk-loss recovery, full upstream cron grammar, general command jobs,
or broader events and channels. R46, R63, A25, A26, A31, A37, and matrix gates stay
open in aggregate. Hosting remains the primary journey; workspaces are deferred.

Durable evidence is retained in `/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-storage-schedules-20261026/733cde407427ef745d57fbb726e001bdb1555d47.pF9Yxx`.

| Receipt | SHA-256 |
| --- | --- |
| [attempt-verification-summary.json](/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-storage-schedules-20261026/733cde407427ef745d57fbb726e001bdb1555d47.pF9Yxx/attempt-verification-summary.json) | `bf8b98d62d07c821eaf2cc10a43139cda39ee7ac19004245bfca891c062eac11` |
| [api-tests.log](/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-storage-schedules-20261026/733cde407427ef745d57fbb726e001bdb1555d47.pF9Yxx/api-tests.log) | `e523f7b54940dee5f97a0510000ef7e0fac583a7f4d464149fb3cb9ddc7b7ea1` |
| [store-tests.log](/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-storage-schedules-20261026/733cde407427ef745d57fbb726e001bdb1555d47.pF9Yxx/store-tests.log) | `b3a0b49c7c1d3b74dc66a7f29722043cfc53a073d869a3e55baa78d2778b0876` |
| [remote-markers.json](/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-storage-schedules-20261026/733cde407427ef745d57fbb726e001bdb1555d47.pF9Yxx/runtime/remote-markers.json) | `93d5ed52ba0c937c1238677dabaad034045ab1c3bf377be60dbed143e35ef60f` |

The browser verifies the actual served candidate assets against its manifest at
320, 390, 430, and 1280 pixels without request interception. Preservation receipts
retain the original 35 containers and 210 volumes. The existing user UI identity
and binary remain unchanged; this run does not upgrade that UI. Task-owned
processes and resources are absent after joined cleanup.

## Failed attempts and repairs

Earlier attempts remain failed and retain their original receipts under the same
evidence parent.

| Candidate and directory suffix | Seconds | Failure and subsequent repair |
| --- | ---: | --- |
| `1efe830db674f6a5f7866ad5662c2a2ce707bc0d.BHohfQ` | 52.858 | Migrations exhausted the 256 MiB PGDATA tmpfs with SQLSTATE `53100`. `9f8596def3aa6f6364b20f77d4a1b3ce5d69a80b` gives the isolated PG service a 1 GiB tmpfs and 1536 MiB memory cap, WAL checkpoint targets, and space diagnostics within the unchanged 6 GiB outer guest. |
| `9f8596def3aa6f6364b20f77d4a1b3ce5d69a80b.pMYk6E` | 89.464 | Schedule ACK setup rejected remote schedule creation after the PG and local archive groups passed. Local reproduction found target validation forcing deferred constraints before the matching immutable schedule version existed. `6526a0db3d717d9b58b30a144216e1565e6ac0c9` fixes the transaction order and adds remote create/update/replay/rollback coverage and finite status/code evidence. `733cde407427ef745d57fbb726e001bdb1555d47` adds error context while preserving error matching. |

The second failed run did not record its actual HTTP status. The reproduced
conflict explains the repaired path; no historical response body or status is
invented. The final passing run rebuilds the candidate binaries and embedded UI.
Local PG race evidence stays labeled at the ordering-fix revision. The final
context-only change has focused compile/API checks and no query or control-flow
change. Earlier product, frontend, and generator evidence retains its original
revision and dependency justification. The existing thirteen-file SQL-placement
baseline failure remains separate; no full CI pass is claimed. Token usage, dollar
cost, and total feature labor are unavailable.
