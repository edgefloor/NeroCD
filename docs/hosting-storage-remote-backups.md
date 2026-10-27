# Back up and recover owned uploads remotely

Owned-upload backups can copy a completed local archive to a project-owned
S3-compatible backup target. A failed upload retains the local archive. Retrying
that upload uses the same archive without stopping writers or running the archive
helper again. For image resources, remote restore downloads and verifies the
object before restoring it into a new resource and owned volume on the same
destination. Compose remote download and restore are unsupported.

## Enable storage remote transfers

Configure the [archive controller and qualified helper](hosting-storage-archive.md)
and the [operator secret keyring](secrets.md). PostgreSQL persistence and hosting
job consumers must be enabled. Apply migration
`0081_hosting_storage_backup_remote.sql` with the normal migration process.

On the runner, use `--execute` and `--storage-archive-config-file` with the existing
absolute, owned mode-0600 archive configuration. Keep its version, helper,
`artifact_root`, `temporary_root`, and `docker_binary` fields. Set
`remote_backups` to `true` to enable storage transfers. The runner adds
`storage_backup_transfer` and `hosting_storage_remote_backup_v1`, independently
of database execution. No database resource or database backup capability is
required.

For a private HTTPS target, set `remote_endpoints` to the exact approved origins
and `remote_cidrs` to their permitted private address ranges in that file. These
use the [shared remote network policy](hosting-database-remote-backups.md#network-policy).
Certificate validation remains required. Redirects, environment proxies, changed
DNS answers, and unapproved private/metadata addresses fail closed.
`remote_fixture_origin` permits one exact numeric loopback HTTP origin for
isolated fixtures only. It is not a production HTTPS qualification.

## Request a backup in the UI

1. Open **Projects**, select the hosting project and environment, and open the
   application's **Upload backups and restores** section. The direct resource
   anchor is `/projects#storage-{resource_id}`.
2. As a project owner, create a target in **Backup targets**. Enter its name,
   endpoint, bucket, region, and write-only access and secret keys. An uploads-only
   application can configure a target here without creating a database.
3. Validate the saved target revision from the selected runner. Refresh the
   destinations and select that revision in **Remote backup target**.
4. Confirm that writers may stop, then choose **Request upload backup**.
5. Select the backup to inspect local results and **Remote archive transfers**.
   Wait for upload success before treating the remote copy as available.

Target validation checks reachability for the exact revision, destination,
runner, and daemon. It does not establish permission to upload an object.
Writer resume must be durably recorded before the independent upload is admitted.
The upload does not hold a live source mutation reservation.

If upload fails, inspect its failure code and choose **Retry remote upload of the
same archive**. The UI retains an ambiguous request for explicit identical replay.
Do not substitute another transfer's retry body after a lost response. Local
archive success and remote transfer failure remain separate recorded outcomes.

## Copy an immediate Compose volume backup remotely

For a managed Compose application, select an eligible named volume and a validated
project target revision in **Compose volume backups**. Review the exact volume,
resource generation, deployment, and remote target. Acknowledge interruption and
confirm the backup. The runner creates the local archive and resumes the recorded
writers before the independent transfer uploads it.

Inspect local completion and remote transfer history separately. A failed upload
preserves the successful local archive. **Retry remote upload of the same archive**
reuses its exact blob and performs no new stop or archive operation. An unconfirmed
request retains its identity for explicit identical replay. Recovery of stopped
writers does not require upload enablement. If transfer dispatch is unavailable,
local completion still releases its hold and leaves the same transfer queued for
the existing recovery loop.

The existing `POST /api/v1/resources/{id}/storage-backups` request accepts
`remote_target: {target_id, revision}` alongside the Compose volume, generation,
deployment, interruption acknowledgement, and request identity. There is no
separate export endpoint. Remote Compose plans use
`managed-compose.remote-volume-archive.v1`. The capability
`hosting_compose_storage_remote_v1` identifies plan understanding and can remain
advertised with uploads disabled so recovery can resume writers. New uploads
still require the existing transfer capabilities, configuration, target validation,
and current authority.

Compose schedules remain local-only. This workflow provides no Compose remote
download, archive restore, or coordinated database and upload recovery point.
The pinned behavioral reference is
`app/Jobs/VolumeBackupJob.php` at Coolify revision
`b12d1386f3d458618b4c579babcd1e339c6c40b3`, which archives the selected volume,
resumes stopped users, and uploads the archive to S3. NeroCD retains its existing
fenced archive, transfer, and recovery contracts.

<a id="restore-and-verify-a-new-resource"></a>

## Restore and verify a new image resource

Select a backup with a successful upload transfer. In **Restore backup into a
new resource**, select **Download and verify the remote archive before restoring**,
enter the new resource name, and choose **Restore into new resource**. Read the
result using **Restore operation ID** after a reload. Deploy the new application
and verify its reads and writes before switching traffic.

Every remote restore performs a fresh GET even when the local content-addressed
archive already exists. It verifies size, SHA-256, and the `nerocd-storage-v1`
archive structure before extraction. The source volume is never replaced.
A corrupt or truncated download fails before an extraction run is admitted.
The failure and exact new-resource ownership hold remain visible. An authorized
**Retry remote download** retries that same restore only while
no extraction run exists. It creates a new transfer attempt and performs a fresh
GET. Earlier failure history remains recorded. An uncertain extraction or writer
resume retains its hold for the existing archive recovery workflow.

Viewers can inspect backup and transfer history. They cannot create targets,
validate, request a backup, retry a transfer, or restore.

## API contract

The versioned contract is in [OpenAPI](../openapi.yaml). All bodies are closed
JSON documents. Backup and restore `request_id` values contain exactly 32
lowercase hexadecimal characters. Repeat the same body and identity to replay
an ambiguous request. A changed body is a separate request, not a replay.

| Request | Body or result |
| --- | --- |
| `POST /api/v1/resources/{id}/storage-backups` | `request_id`, `expected_resource_generation`, optional `remote_target: {target_id, revision}` |
| `GET /api/v1/storage-backups/{id}/transfers` | Storage-specific upload/download metadata and history |
| `GET /api/v1/storage-backup-transfers/{id}` | One transfer, including `state`, `failure_code`, `backup_id`, `restore_operation_id`, and immutable `blob` metadata |
| `POST /api/v1/storage-backups/{id}/transfers/retry` | `request_id`, `expected_transfer_id`; replay does not archive or quiesce again |
| `POST /api/v1/storage-backups/{id}/restores` | `request_id`, `name`, optional `remote_source: {transfer_id, expected_target_revision}` |
| `GET /api/v1/storage-restores/{id}` | New resource/storage and restore operation |
| `GET/POST /api/v1/projects/{project_id}/database-backup-targets` | Existing project target registry adapter, shared with PostgreSQL |
| `GET/POST /api/v1/projects/{project_id}/database-backup-targets/{target_id}/storage-validations` | Storage-specific validation history or request with `request_id`, `expected_revision`, `destination_id` |

The legacy `database-backup-targets` path names the shared registry adapter.
Storage transfers have their own plans, capabilities, blob validation, and
credential purpose. They do not use PostgreSQL backup plans. Target keys remain
write-only managed secrets. Current enablement, target revision, credential
revocation, short-lived grants, and full attempt fences govern new effects.
Exact committed runner receipts remain replayable after revocation; replay cannot
authorize another mutation. The immutable object key is
`nerocd/storage/{backup_id}/{sha256-without-prefix}.tar`. Conditional PUT and
full-body response-loss reconciliation prevent an ACK replay from overwriting or
uploading the archive again.

<a id="accepted-qualification-and-evidence"></a>
<a id="later-scheduled-workflow-qualification"></a>
<a id="upstream-behavior-and-native-implementation"></a>

## Tests and supported environments

The [storage runtime gate](../acceptance/runtime-hosting-storage/gate.sh) in
`--remote` mode checks target creation and validation, local backup, failed-upload
history after reload, same-archive retry, remote restore, and history reads.
Viewer history and absent mutation controls are checked separately. Owner layouts
use widths 320, 390, 430, and 1280 pixels.

The runtime verifies upload after writer resumption, retry without a new archive
helper, committed receipt replay, and corrupt/truncated download rejection before
extraction. A restored application reads archived uploads and makes new writes in
a distinct volume while source bytes and metadata stay unchanged. Numeric owners
and ordinary permission modes survive restore.

The tested profile is native Linux arm64, trusted rootful classic-store
Docker/overlay2, one destination, pinned Silo through explicit loopback HTTP, and
private fixture-CA HTTPS. Production S3 HTTPS, other providers, disk loss,
cross-host recovery, in-place restore, and arbitrary mounts are unqualified.

[Recurring image upload backups](hosting-storage-backup-schedules.md) use the same
archive and transfer path. Compose schedules remain local-only. [Retention](hosting-storage-backup-retention.md) and
[individual copy deletion](hosting-storage-backup-deletion.md) have separate
policies and destructive-operation opt-ins.

<a id="compose-remote-copy-qualification-pending"></a>

### Accepted Compose remote copy

The bounded immediate Compose remote-copy workflow passed qualification on
2026-10-27 at `f927e59d7470b2b6e0b86074f4ac411200aece02`:

- [x] Selected a validated S3-compatible target revision with the immediate volume
  backup, reviewed the exact request, and inspected failed-upload history after
  reload. The denied upload retained successful local backup state and its blob.
- [x] Retried the same archive and verified all 6,144 bytes and checksum against
  the remote object. One archive helper ran. Retry caused zero component events,
  and two transfer guard checks confirmed writers had resumed before upload.
- [x] Application records and uploaded files survived redeployment and Stop/Start.
  Local volume schedules and FIFO failure compensation, PostgreSQL schedules and
  separate-target import, and failed replacement with its mutation hold passed.
- [x] All 41 baseline container profiles and 236 volume profiles remained exact,
  with identical inventories, no additions or leaks, and complete owned cleanup.
  The user's instance retained its identity, content, and health.

The aggregate gate passed in 279 seconds, from 11:59:15 to 12:03:54 UTC.
`TestHostingComposeVikunjaCompiledRunnerHTTPS` passed in 160.01 seconds.
Safe receipts and hashes are indexed at
`$HOME/.local/share/nerocd/acceptance/runtime-hosting-compose-20261027/f927e59d7470b2b6e0b86074f4ac411200aece02/evidence-index.json`.
This qualifies the managed Compose profile with the pinned Silo fixture through
explicit loopback HTTP. It does not qualify production S3 HTTPS or other
providers, Compose remote restore, or coordinated database and upload recovery.

The three earlier `--managed-compose` runs stopped before remote-copy phases.
All three passed the same exact inventory, cleanup, and user-instance checks. The
[accepted local schedule evidence](hosting-compose.md#accepted-local-named-volume-schedules)
remains separate, including its earlier unresolved failed attempt.

A focused check using the actual Project Hosting component reproduced a fixture
flaw. A principal refresh retires the child after a 200 response header arrives,
cancels that response's body, and remounts the UI with a working fetch. The
fixture had selected the canceled response. At `f927e59`, its existing response
predicates wait for a complete body. They emit no requests or retries, and still
reject completed 503 responses and invalid JSON. The completed-schedule-response
proof is included in the safe evidence.
This reproduction does not conclusively attribute any historical failure.
The first saved-schedule selection timeout remains unresolved.

Source checks passed at `1b56db8cc1631ee87c40d69af5d43ab3bf0be7f4`:
`make verify`, the full frontend suite with 1,928 tests in 129 files, the WebUI
and embedded application build, and `make check-generated`. Subsequent changes
affect only acceptance fixtures. Focused real-decoder, shared browser-phase, and
response-completion checks passed. Product, schema, UI, and runner bytes are unchanged, so their source evidence is retained at `1b56db8`. Independent hygiene,
specification, and security reviews reported no findings at `f927e59`.
Full `make check` was not run.

Recorded delivery times overlap and are not an additive elapsed-time partition.
All times below are UTC on 2026-10-27. Receipt directories use the prefix
`/tmp/nerocd-storage-runtime-20261024.`.

| Category | Revision or receipt | Recorded time and limitation |
| --- | --- | --- |
| Implementation | Backend and UI | Backend has no separate measured total. UI work was approximately 10:58:51–11:09, including focused checks. |
| Product-defect repair | Admission ordering | Corrected before runtime qualification. Repair time was not separately measured. |
| Fixture repair | Invalid-target decoder case | Approximately 62 seconds, 11:16:02–11:17:04, including checks. |
| Fixture repair | `f927e59` completed-body selection | Actual React reproduction passed from 11:58:16.560 to 11:58:18.531. Total repair and diagnosis time was not separately measured. |
| Test execution | `1b56db8` source checks | `make verify` 218 seconds, frontend tests and build 45 seconds, generated checks 48 seconds. Commands started at 11:14:14. Final decoder and phase checks took four seconds. |
| Failed qualification | `c12f079`, `ruIO8X` | 217 seconds, 11:20:23–11:24:00. Timed out at `volume_schedule_run` / `select_saved_compose_volume_schedule`, after disabled schedule save, redeployment, and Stop/Start passed. |
| Failed qualification | `923952d`, `wDx8af` | 117 seconds, 11:39:47–11:41:44. Failed at `volume_schedule` / `read_initial_compose_volume_eligibility`. |
| Failed qualification | `c5fe588`, `QMsshP` | 128 seconds, 11:48:03–11:50:11. Failed at the same initial eligibility step. |
| Passed qualification | `f927e59`, `1kUdIW` | 279 seconds, 11:59:15–12:03:54. Physical runtime test 160.01 seconds. |
| Review and waiting | Independent reviews | No separate measured total is available. |

The evidence index retains source-check logs and command timestamps at their
original revisions. Only acceptance fixtures changed after `1b56db8`.
