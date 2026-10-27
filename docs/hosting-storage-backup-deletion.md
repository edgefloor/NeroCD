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

<a id="verification-boundaries"></a>
<a id="accepted-qualification-and-evidence"></a>
<a id="upstream-mapping-and-deliberate-differences"></a>

## Tests and supported environments

The [storage runtime gate](../acceptance/runtime-hosting-storage/gate.sh) in
`--deletion` mode checks selected local deletion, retained remote availability,
new-volume restore with application reads and writes, final-copy confirmation,
conditional remote deletion, response-loss replay, retained history, and viewer
denial. Focused PostgreSQL tests separately cover target substitution, partial
outcomes, restart after intent, final-copy rechecking, stale authority, protected
copies, revocation, disabled retention, and audit rollback.

The tested profile is native Linux arm64, trusted rootful classic-store
Docker/overlay2, one destination, pinned Silo through explicit loopback HTTP, and
private fixture-CA HTTPS. Production S3 HTTPS, other providers, arbitrary mounts,
resource-volume deletion, disk loss, cross-host or in-place recovery, delete-all,
destructive schedule removal, and history erasure are outside this workflow.
The optional broader runtime race run timed out; the focused workflow does not
establish a full CI pass.
