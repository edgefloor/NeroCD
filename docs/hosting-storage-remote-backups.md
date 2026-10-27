# Back up and recover owned uploads remotely

Owned-upload backups can copy a completed local archive to a project-owned
S3-compatible backup target. A failed upload retains the local archive. Retrying
that upload uses the same archive without stopping writers or running the archive
helper again. A remote restore downloads and verifies the object before restoring
it into a new resource and owned volume on the same destination.

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

## Restore and verify a new resource

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

[Recurring upload backups](hosting-storage-backup-schedules.md) uses the same
archive and transfer path. [Retention](hosting-storage-backup-retention.md) and
[individual copy deletion](hosting-storage-backup-deletion.md) have separate
policies and destructive-operation opt-ins.
