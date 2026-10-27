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

## Schedule a Compose named volume

In **Compose volume backups**, select an eligible named volume. In **Recurring
Compose volume backups**, enter the frequency and timezone. Use **Remote scheduled
Compose backup target** to select a validated target revision, or leave it empty
for a local archive. This selection is separate from the immediate backup target.
Choose **Review Compose volume schedule**. Review the target revision and current
deployment, then acknowledge
interruption of the selected volume's running writers for every due backup.
Choose **Confirm Compose volume schedule**. Even an initially disabled schedule
requires this fresh review. Enabling or editing an enabled schedule requires a
new deployment review and acknowledgement.

The selected volume is fixed for the schedule. Create a separate schedule to
select another volume. Redeployment can change component containers while
preserving the selected volume's recorded creation origin. Each due occurrence
resolves the current settled deployment and records its exact operation identity.
It uses the existing [archive controller](hosting-storage-archive.md) and writer
recovery protocol. Other volumes are excluded. Each occurrence preserves a local
archive and can upload that same archive to the schedule's selected target after
writers resume. A failed upload retains local success. Use the existing remote
transfer history to retry the same blob without another archive or interruption.
The schedule does not provide coordinated database and upload snapshots.

Use **View schedule** and **View scheduled archive** to inspect occurrences and
archive results. Owners can disable an existing schedule without fresh runtime
evidence. Current authorization still applies to reads, disable, and identical
request replay when the archive controller or runtime is unavailable. Disabling
a schedule does not cancel an admitted backup or clear a mutation hold.

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

Compose uses the same endpoints and common input fields. Add `volume`,
`expected_deployed_operation_id`, and `acknowledge_interruption: true` when
creating a schedule, including a disabled one, or saving an enabled revision.
The deployment must match fresh eligible volume evidence. A disabled update
retains `volume` but can omit the deployment and acknowledgement fields.
Optional `remote_target: {target_id, revision}` pins the target for the saved
Compose revision. Image schedules keep their existing input contract. A disable
can retain that saved selection even while remote validation or runtime evidence
is unavailable. Current authorization still applies.

Compose schedule metadata uses
`managed-compose.local-volume-archive.schedule.v1` and exposes `volume` and retained
`acknowledge_interruption`. Occurrences expose the selected volume alongside
their schedule revision, linked backup, operation, and ownership state. The
controller verifies the stable volume origin and the schedule actor's current
authority at each due admission. The immutable occurrence uses that revision's
exact remote target and rechecks its validation, credential authority and required
capabilities before admitting new work. A changed volume origin or target cannot
silently retarget an existing occurrence.

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

## Runtime verification

Run `bash acceptance/runtime-hosting-storage/gate.sh --remote` only after approval
for the exact committed candidate and the disposable target in the
[runtime guide](../acceptance/runtime-hosting-storage/README.md).
The gate checks schedule admission and replay, controller restart, real archive
and upload failures, SMTP/webhook delivery, and restored application reads and
writes. Compilation alone does not prove those outcomes.

<a id="accepted-qualification-and-evidence"></a>
<a id="reference-mapping-and-qualification"></a>
<a id="failed-attempts-and-repairs"></a>

## Tests and supported environments

The [storage runtime gate](../acceptance/runtime-hosting-storage/gate.sh) in
`--remote` mode checks schedule create, identical response-loss retry, edit,
disable, and occurrence history. Viewers read history without mutation controls.
Concurrent due passes admit one archive, and the controller stops, joins, and
restarts before admission.

Real archive and upload failures reach SMTP and HTTPS webhook receivers. The
fixture opens delivered recovery URLs in the authenticated browser, checks source
association, and confirms that opening a link starts no mutation. The owner retries
the existing archive's upload and restores into a distinct volume. The deployed
application reads archived data and accepts new writes while source bytes and
permissions stay unchanged. Browser checks cover 320, 390, 430, and 1280 pixels.

The tested profile is native Linux arm64, trusted rootful classic-store
Docker/overlay2, one destination, and pinned Silo through numeric loopback HTTP.
Notification receivers use a private CA and explicit fixture TLS origins.
Production S3 HTTPS, public-CA deployment, arbitrary mounts, in-place or cross-host
recovery, disk loss, broader cron grammar, general command jobs, and additional
events and channels are unqualified.

## Accepted Compose qualification

Local schedules for a selected managed Compose named volume passed at
`1df3c90a1aef7b3f4235b4ba43eb239415b29e51`. The existing Compose gate saved a disabled
schedule, retained it across redeployment and Stop/Start, enabled it against the
current deployment, executed a due local archive, and disabled it through the UI.
Exact archive bytes and ownership, application persistence and new writes,
failure compensation, and cleanup passed. See the
[Compose checklist and evidence](hosting-compose.md#accepted-local-named-volume-schedules)
for source revisions, the earlier unresolved timeout, check limits, and timing.
That local qualification does not establish scheduled remote copies. The
[scheduled Compose remote-copy extension](hosting-storage-remote-backups.md#compose-remote-schedule-qualification)
records its separate qualification status. Compose download and archive restore
remain unsupported.

## Compose upstream reference

The behavioral reference is Coolify commit
`b12d1386f3d458618b4c579babcd1e339c6c40b3`, specifically
`app/Http/Controllers/Api/VolumeBackupsController.php::persistSchedule` and
`app/Jobs/ScheduledJobManager.php::processScheduledVolumeBackup`. Coolify stores a
schedule against selected persistent storage and dispatches each due volume
backup, withholding new work while stopped-container recovery is pending.
NeroCD follows that workflow through its existing scheduler, current authorization,
immutable occurrence transactions, fenced Go runner, and durable archive recovery.
The optional scheduled remote copy reuses the immediate local archive and S3
transfer sequence. Retention and broader storage-type behavior remain outside
this Compose slice.
