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
