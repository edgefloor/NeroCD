# Owned upload-backup copy deletion

An owner can select local or remote physical copies of one owned upload backup from its archive details. The review identifies the exact selection, digest and bytes before execution. If the selection removes the final recoverable copy, explicit confirmation is required. The controller rechecks recovery availability at execution admission and rejects a changed final-copy classification, requiring a fresh review.

This operation works without a retention policy, including when retention is disabled. It does not change retention limits, remove a schedule, erase backup history, or delete every backup. Viewers can read scoped deletion history but cannot preview or execute a deletion.

## Public contract

All routes are scoped to an authorized resource and one backup:

- `POST /api/v1/resources/{resource_id}/storage-backups/{backup_id}/deletion-previews` accepts `request_id`, `expected_resource_generation` and nonempty unique `copy_ids`.
- `POST /api/v1/resources/{resource_id}/storage-backups/{backup_id}/deletions` accepts `request_id`, `preview_id`, `expected_plan_hash` and optional `confirm_final_copy`.
- `GET` on the same `deletions` route reads history. Appending `/{execution_id}` reads one execution.

The server resolves physical identities. Clients cannot supply archive paths, object keys, buckets, endpoints, ownership proofs or runner identities. Foreign, unavailable, shared, reserved, unproven, active and recovery-protected copies fail the entire selection. A preview never silently drops a selected copy.

The public version is `owned-upload.backup-deletion.v1`. Preview and execution documents carry `origin: manual_selection`, `backup_id`, `resource_generation` and `policy_revision: 0`. `final_copy` is true when the reviewed selection has no available surviving copy, and is omitted when false. Policy selection never creates these documents.

Request identities are scoped to the resource and operation kind and bind the actor, backup and complete input. Identical requests replay their saved response, even after settlement. Changed inputs under an existing identity conflict. The UI keeps the original request for response-loss retry. Discarding a local saved request does not cancel accepted server work.

A separate explicit resume request references the same immutable preview and plan hash. It can obtain a fresh fenced attempt for held work. It cannot select more copies or replace a recorded remote target. Nonversioned remote deletion uncertainty retains its hold for reconciliation.

## Shared destructive execution

Manual selection uses the existing per-copy reservation, admission locks, journal, receipts, credential delivery and replay machinery. The internal task remains `storage_backup_retention`; the plan carries the explicit manual origin and deletion version. This avoids a second physical deletion engine.

A runner must advertise `hosting_storage_backup_deletion_v1` in addition to the existing storage retention task and retention capability. New agents advertise it only with the existing archive configuration `backup_retention: true`. That opt-in authorizes both retention-selected and manually selected destructive copy operations. No new automatic scheduler is introduced. Older agents cannot claim manual plans because admission and lease creation require the new capability, and their plan validator rejects the deletion version.

Every plan fetch, intent and physical mutation remains subject to current owner, destination grant, runner, binding, storage reference and lease authority. Manual operations also fence the resource generation. Local deletion verifies the recorded CAS ownership proof; remote deletion uses the successful upload's recorded target snapshot and conditional object proof. Backup success, transfer rows and audit history remain intact. Local and remote outcomes are reported independently, including a partial failure.

## Upstream mapping and deliberate differences

The reference is Coolify commit `b12d1386f3d458618b4c579babcd1e339c6c40b3`.

- `app/Livewire/Project/Shared/Storages/VolumeBackups.php:338` authorizes resource update and password confirmation, scopes the execution to its backup, and rejects running or recovery-pending work. It deletes the local archive first and optionally deletes the remote archive recorded on that execution.
- `resources/views/livewire/project/shared/storages/volume-backups/executions.blade.php:52` builds the optional S3 selection, and line 138 provides destructive confirmation.
- `bootstrap/helpers/databases.php:212` performs local deletion. Line 267 uses the recorded S3 storage. `ScheduledVolumeBackupExecution.php:45` exposes pending recovery. `tests/Feature/VolumeBackupTest.php:813` and line 839 cover individual local deletion and the recorded remote target.

NeroCD uses its current owner authentication and CSRF contract, exact copy selection, plan review and final-copy confirmation. It does not add a second password exchange. NeroCD deliberately preserves the logical backup and execution history for R47 instead of deleting the Coolify execution row. It also retains CAS sharing protections, conditional remote deletion, durable intent, stale-authority rejection and uncertainty holds from the accepted retention implementation.

## Verification boundaries

The focused PostgreSQL tests cover admission, request replay, audit rollback, revocation, restart after intent, final-copy rechecking, retained remote availability and per-location partial outcomes. Runner journal and runtime replay tests use explicit manual-origin plans. UI tests cover selection, confirmation, response-loss retry, history and denied access.

`acceptance/runtime-hosting-storage/gate.sh --deletion` extends the existing disposable retention fixture. It deletes one selected local archive, checks physical absence and retained remote/history evidence, restores that remote archive to a new volume, deploys the restored application, and verifies real reads and new writes with the source preserved. The same live browser then selects both remaining copies, confirms final-copy deletion, and retries the identical committed request. The fixture checks local absence, one conditional delete of the recorded remote object, retained history, and unaffected deployed data. Viewer controls and direct viewer mutation denial use the real API. The gate also requires the manual PostgreSQL test matrix without skips and exports `deletion-markers.json`. Launching this physical fixture requires separate approval for the exact reviewed candidate and disposable Docker/S3 target. Source compilation or mocked browser tests do not prove physical deletion or recovery.
