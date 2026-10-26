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

## Accepted qualification and evidence

Leader acceptance is recorded at `52f1187a8c42dafd72b68bf7e015c226cc29ec1f`.
The physical source is `da0678eb7a8fdd67865bc328f0773334a47920d2`, tree
`3d09268416f8c5fb9d3715f6f7233886a3e584e2`. The final gate passed in 155.120 seconds.
It passed 18 PostgreSQL groups without skips, the local archive group in 23.27
seconds, and the manual deletion/recovery group in 55.68 seconds.

The live browser used the Go-served candidate UI and real API without request
interception. It selected a local copy, reviewed the exact plan, and retried the
identical request after committed-response loss. The fixture verified physical
local absence, retained remote availability and history, then restored the remote
copy into a new volume. The deployed application read restored bytes and made new
writes while the source remained intact. The browser then selected the remaining
local and remote copies, explicitly confirmed final-copy deletion, and retried the
same committed request. The fixture verified local absence, one conditional delete
of the recorded remote object, and retained history. Viewer history and a real 403
mutation denial also passed.

Recorded-target substitution, partial outcomes, restart after intent, final-copy
rechecking, stale authority, protected copies, revocation, disabled retention policy,
and audit rollback are source PostgreSQL test evidence. They are separate from the
physical workflow; the physical fixture does not inject every failure.

The physical evidence retains its original source SHA. Comparing that source
with accepted main changes only `README.md`, `docs/plan.md`, `docs/parity-audit.md`,
`docs/parity-checklist.json`, and the earlier retention guide. Product sources,
SQL/migrations, module dependencies, frontend sources and embedded assets, and
acceptance fixtures are unchanged. This documentation closure does not rerun
runtime tests or replace the original receipts.

The durable receipt directory is
`/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-storage-deletion-20261026/da0678eb7a8fdd67865bc328f0773334a47920d2.bldNO0`.
Its `attempt-verification-summary.json` has SHA-256
`0673463e3ea485196bfd3a91e052592bb5e92772ccef523df51f9804f6b1131c`.
`runtime/deletion-markers.json`, `browser-safe.json`, `store-deletion-tests.log`,
`api-tests.log`, `preservation-safe.json`, `source-blobs`, and `safe-receipt.tar.gz`
retain the corresponding evidence. All 38 baseline container profiles, all 214
volume profiles, and the existing user UI identity/content/listener/health were
preserved. Task-owned resources and processes were absent after cleanup.

Four physical attempts took 691.150 seconds in total. The first attempt failed
without a precise manual-browser cause. The second exposed stale archive-list
selection. A refresh fix then failed code hygiene because a click did not await
fresh data; the repaired fixture waits for the exact response and rendered copy
inventory. The third physical attempt passed the functional workflow but failed
marker collection and preservation accounting for concurrent unrelated additions.
The final repair collects markers before validation, preserves every baseline
profile, rejects task-owned leftovers, and reports unowned additions separately.
Earlier receipts remain failed. Four repair rounds produced five exact review
candidates. All three required lenses passed the final candidate. Combined checks
passed in 14.706 and 0.611 seconds; main promotion took 0.925 seconds. These timings
are not speedup measurements. Per-feature monetary and token totals are unavailable.

The optional broader runtime race run reached its 600-second package timeout and
remains a failed diagnostic. This acceptance is not a full CI pass. Qualification
covers trusted native Linux arm64, rootful classic-store Docker/overlay2, one
same-destination runner, pinned Silo at explicit loopback HTTP, and the existing
local private-CA profile. Production S3 HTTPS/provider breadth, arbitrary mounts,
resource-volume deletion, other engines, disk loss, cross-host/in-place recovery,
delete-all, destructive schedule removal, history erasure, and full parity remain
open. The user instance remains unchanged at `09a` and schema 74.
