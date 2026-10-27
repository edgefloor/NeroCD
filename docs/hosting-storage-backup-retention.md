# Owned-upload backup retention

## Policy and operator workflow

Storage backup retention applies to one resource's saved storage binding. It does not inventory arbitrary host files or buckets. An owner saves independent local and remote limits, reviews a persisted deletion preview, and executes that exact preview. Viewers can read policies, execution history, and copy availability.

The archive panel displays configuration defaults of seven local copies and seven remote copies. Age and size default to zero. All zero limits mean unlimited retention. Existing resources have no enabled retention policy until an owner saves one. Disabling a backup schedule does not delete archives.

The policy pins the storage reference, binding, and destination. An ordinary application generation change preserves that policy when these identities stay the same. A new policy write still requires the current resource generation. A changed storage reference or placement invalidates admission.

The resource endpoints are:

| Method | Path suffix under `/api/v1/resources/{id}/storage-backup-retention` | Result |
| --- | --- | --- |
| GET | empty | Current policy or null |
| PUT | empty | Save with request identity, expected policy revision, and current resource generation |
| POST | `/previews` | Immutable preview at a persisted database evaluation time |
| GET | `/executions` | Current batch history |
| POST | `/executions` | Execute or continue the preview identified by its revision and hash |
| GET | `/executions/{execution_id}` | Current per-copy results and reservations |

A replayed request returns its original response. Read the execution endpoint for current progress. A replay never chooses another batch. Resume uses a fresh lease and fence for the same immutable plan and preserves completed items.

## Selection semantics

The [selection algorithm](../internal/domain/hosting_storage_backup_retention.go) applies to the resource's saved storage identity across scheduled and manual backups.

Each location inventories successful, nondeleted copies. Remote copies also require a successful upload and retain their recorded destination configuration. Descending backup creation time and a deterministic copy ID tie-breaker determine order.

Count, age, and size selections form a union:

- A positive count selects copies after the retained prefix.
- A positive age selects copies strictly before the preview's database time minus the configured days. Equality survives. Age can select every old copy, including the newest.
- A positive size includes the newest copy in the running sum. Once an older copy crosses the threshold, that copy and its older suffix are selected. Size alone never selects index zero.

For sizes 128, 64, and 64 bytes, a 160-byte limit selects both older copies. A 192-byte limit selects only the oldest. There is no blanket newest-copy protection. Selection uses the persisted preview instant again at admission, so a delayed execution does not advance the age boundary.

A batch contains at most 100 selected copies. The response exposes at most 1,000 inventory items and reports truncation. These bounds apply after complete inventory selection.

## Ownership and protected copies

Deletion requires runner-only physical ownership evidence. Public policy, preview, history, and availability responses contain no object keys, host paths, or credential references.

Local publication records the artifact pool, device, inode, byte count, and whether `PutOwned` created the file. Local deletion verifies this exact inode and digest under the pool lock. A replaced inode fails closed.

Remote publication records conditional creation, ETag, and version evidence. Identical bytes adopted from an existing object do not create deletion rights. Before any selected remote deletion, the executor qualifies conditional deletion on a fresh owned probe at that recorded destination. A provider that ignores a mismatched condition cannot qualify. The actual deletion verifies the saved content and uses its exact ETag and version.

Explicit protections override selection:

| Protection | Reason |
| --- | --- |
| Shared | Another live copy or successful legacy reference uses the same physical location |
| Active | Archive publication or transfer can still use the runner's artifact pool |
| Recovery | The selected backup has unresolved archive or transfer recovery |
| Unproven ownership | A legacy receipt or adopted object lacks creation evidence |
| Reserved or uncertain | An existing batch owns unresolved effects |
| Unavailable | The copy cannot currently support a new destructive effect |

Publication, upload, download, restore, and retention share short serial admission transactions. A held local effect conservatively excludes producers on the same runner or server because new archive plans do not know their artifact pool. Independent runners on other servers remain usable. A held remote effect excludes its recorded canonical endpoint, bucket, and object; different objects and destinations can proceed. Remote target validation does not touch a local artifact pool. Download completion can reuse an exact current local generation or publish a distinct owned generation. Supersession retains the old proof and receipt. It does not rewrite that generation as physically deleted.

## Automatic cleanup and recovery

The protected storage archive runner configuration has a separate `backup_retention` boolean. The runner advertises `storage_backup_retention` and `hosting_storage_backup_retention_v1` only with this explicit opt-in. Local retention does not require database execution or remote backup execution.

Automatic cleanup is optional in the resource policy. Successful local publication, successful remote upload, and completed archive recovery produce deduplicated publication records. The existing joined hosting recovery loop admits their previews and batches. Retention failure does not rewrite archive or transfer success and does not emit a backup-failure notification.

Intent and result journal records survive restart. A historical receipt acknowledges only its exact request. New effects require current actor, project, destination, credential, runner, and full lease authority. Unknown destructive effects retain their reservations. A batch denied before deletion can retain reservations for recovery after current authorization is restored; there is no separate cancellation subsystem. In the UI, definitive invalid or stale requests permit correction and a new review. An ambiguous saved request retries the identical body, or can be discarded locally with an explicit warning that discarding does not cancel accepted server work. An uncertain nonversioned remote delete cannot be retried as a fresh deletion merely because its response was lost.

Restore uses retained availability and the recorded remote configuration. A remote restore verifies downloaded bytes, republishes local ownership evidence, and restores into a new owned application volume. The source application and immutable backup history remain separate from copy availability.

<a id="accepted-qualification-and-evidence"></a>
<a id="upstream-mapping-and-deliberate-differences"></a>
<a id="prior-attempts-and-remaining-scope"></a>

## Tests and supported environments

The [storage runtime gate](../acceptance/runtime-hosting-storage/gate.sh) in
`--retention` mode checks manual preview and execution, identical response-loss
replay, two local deletions and one conditional remote deletion, remote restore
into a separate volume, and application reads and new writes. The real browser
checks owner actions, viewer history and mutation denial, and widths 320, 390,
430, and 1280 pixels. Automatic publication deduplication, audit rollback, and
age/size boundaries have focused domain and PostgreSQL tests; the physical test
does not inject every failure.

The tested profile is native Linux arm64, trusted rootful classic-store
Docker/overlay2, one same-destination runner, the pinned archive helper, and Silo
through explicit loopback HTTP. The shared notification fixture uses a private CA.
Production S3 HTTPS, other providers, arbitrary mounts, disk loss, other engines,
and in-place or cross-host recovery are unqualified.

To delete selected copies without a retention policy, use
[individual backup deletion](hosting-storage-backup-deletion.md).
