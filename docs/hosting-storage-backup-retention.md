# Owned-upload backup retention

Status: Leader accepted at `c9c90429d5633fb6e64656001b21df3bfb6dfa97`, tree `892713a0b19c21a2afd973f5e80414fa87925bb9`, after exact code hygiene, specification, and critical security reviews, physical qualification, and combined integration checks. Acceptance is limited to the owned-upload profile below.

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

Selection follows `VolumeBackupJob::removeExpiredBackups` and `executionsOutsideRetention` in Coolify reference commit `b12d1386f3d458618b4c579babcd1e339c6c40b3`, lines 459 and 533. The reference operates per schedule. NeroCD deliberately applies these rules to the resource's same saved storage identity across its backup schedules and manual backups.

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

## Accepted qualification and evidence

The accepted source is `c9c90429d5633fb6e64656001b21df3bfb6dfa97`, tree `892713a0b19c21a2afd973f5e80414fa87925bb9`. The physical command was `acceptance/runtime-hosting-storage/gate.sh --retention`. The exact-candidate run exited zero in 126.110 seconds; its local archive group took 23.75 seconds and its retention group took 48.47 seconds. These are observed durations, not a throughput or delivery-speed comparison. Main fast-forwarded to the identical tree, and combined contracts passed in 5.034 seconds with a clean diff check.

The qualified profile is native Linux arm64, trusted rootful classic-store Docker/overlay2, one same-destination runner, the pinned archive helper and Silo at explicitly trusted loopback HTTP. The existing private-CA notification fixture remains part of the shared workflow. This does not qualify production S3 HTTPS or other providers, arbitrary mounts, disk loss, other engines, in-place/cross-host recovery, or full hosting parity.

The browser served the exact candidate assets without route interception. Owners saved local count one and remote count two, reviewed the exact immutable selection, executed it, and retried the identical request after the production handler committed but its response was lost. History and copy availability showed the result. A viewer could read history, had no mutation controls, and received a real forbidden preview response. The run checked responsive widths 320, 390, 430, and 1280 pixels.

The runtime physically unlinked two local copies and conditionally deleted one remote copy. Exact preview/execution replay did not select another batch. Restoring the middle retained remote backup republished an owned local generation into a distinct application volume; application reads and new writes passed while both source applications remained unchanged. The whole baseline of 38 containers, 214 volumes, and the existing user UI identity and health was preserved. Owned processes and disposable resources were removed.

Count/age/size union semantics and the age boundary have focused domain evidence. Automatic publication deduplication and joined recovery have focused PostgreSQL evidence; the physical browser path used manual review and execution. Audit atomicity is specifically covered by `TestPostgresStorageRetentionCurrentAuthorityAndAuditRollback/audit`: rejecting the audit write leaves no retention receipt or changed item, and retry succeeds after removing the rejection. Its retained real-PostgreSQL race receipt is `/tmp/nerocd-storage-retention-repair-storage-pg-race.log` at `5f000fff1f3701e31b9107a4ab5da2f633b7f36b` (79.54 seconds wall, 74.941 seconds package). The invocation selected `TestPostgres(StorageRetention|StorageRemote|StorageArchive)`. The relevant store, application, domain, queue, SQL, migration, and owning test sources are unchanged through the accepted candidate. The ten PostgreSQL groups inside the physical fixture cover earlier schedule/remote/notification contracts; they are not a physical audit-failure injection.

Durable evidence is retained under:

`/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-storage-retention-20261026/c9c90429d5633fb6e64656001b21df3bfb6dfa97.Nh3DOw`

| Artifact | SHA-256 |
| --- | --- |
| `attempt-verification-summary.json` | `5ebbd81fbe6b9ce12bfa197b61e18e90f60d8585d81201410bfcc16e01e11e6b` |
| `safe-receipt.tar.gz` | `53aa89f1c12fc30ab6cea919cfcc85f5496ebfee82e7a945f1773f1b36e8aa85` |
| `runtime/retention-markers.json` | `c71e26501e2be4edaa0c24a8ca675b1f89a0609c988a81c0f20fb153f50fcdb9` |
| `browser-safe.json` | `a206fe80982d5ae54f505eafd6d34fe3ab8f4f809b00de8cf515596321d51a63` |

The directory also contains API/PostgreSQL logs, preservation proof, source blobs, binary hashes, and the UI manifest. Source correspondence is retained separately from the historical parity audit.

## Upstream mapping and deliberate differences

The reference is Coolify `b12d1386f3d458618b4c579babcd1e339c6c40b3`:

| Reference | NeroCD replacement or limit |
| --- | --- |
| `app/Livewire/Project/Shared/Storages/VolumeBackups.php:135,424` and `resources/views/livewire/project/shared/storages/volume-backups/retention.blade.php` | Existing resource UI/API saves independent local and remote limits. Defaults are visible but never enable cleanup retroactively. |
| `app/Http/Controllers/Api/VolumeBackupsController.php:193,271` | Resource-scoped policy CAS and current owner authority replace schedule field updates; mutation/viewer authorization remains explicit. |
| `app/Jobs/VolumeBackupJob.php:459,533` | Typed Go selection preserves successful/nondeleted inventories, recorded remote storage, count/strict-age/size union, and zeros as unlimited. A persisted database instant and ID tie-breaker make preview and replay deterministic. |
| `bootstrap/helpers/databases.php:212,267` | Go runner ownership checks, exact inode removal, conditional S3 proof, lease/fence checks, and durable intent/result journals replace SSH `rm`/`mc` execution. |
| `app/Models/ScheduledVolumeBackup.php` and the job's execution cleanup | Policy binds the resource's same storage/binding/destination across schedules and manual backups. Logical backup and transfer history is immutable; copy availability is separate. Disabling a schedule does not delete archives. |

NeroCD uses byte limits rather than upstream floating-point GiB fields and bounds days at 36,500. Legacy receipts remain restorable but deletion-unproven. Shared, active, recovery, and uncertain effects remain protected. There is no blanket newest-copy protection: age may select all eligible old copies. These restrictions do not establish arbitrary-host inventory or upstream history-erasure parity.

## Prior attempts and remaining scope

The 270.530-second attempt at `5f000fff1f3701e31b9107a4ab5da2f633b7f36b` failed before retention deletion in the shared remote restore path. Durable `helper_identified` evidence existed while the server retained only `helper_intent`; the underlying request error was not retained. Its cause remains unconfirmed. The later diagnostic change added safe stage/class/status output, and the focused PostgreSQL/typed-client reproduction passed; no restore fix is claimed.

The 126.285-second attempt at `8a464c036bebfe9c5b138f8b7f6e9a2688a58fed` passed the API retention and existing archive browser assertions but failed overall preservation after unrelated `plex-patched` restarted. The actor is unknown. Both failed attempts remain failed with their original evidence; the final pass does not rewrite them. Implementation, review repairs, diagnosis, and browser-fixture work occurred in separate rounds. Feature-specific token usage, dollar cost, total labor, and speedup are unavailable.

The SQL-placement check retains its 13 preexisting database-file failures. New storage retention queries live in SQLC sources. There is no full-CI pass or all-engine/full-parity claim. The supplemental ledger adds no historical score and leaves all aggregate gates unchanged. Derived `.codegraph` daemon cache activity was observed and preserved; it is not runtime execution authority or acceptance evidence.

The next proposed bounded hosting gap is owner-directed deletion of one selected owned-upload backup's local and optional remote copies. Coolify's `VolumeBackups::deleteBackup` at line 338 performs this separately from automatic retention. NeroCD still needs an explicit reviewed selection, exact replay, current authority, shared/active/recovery protections, and physical/browser qualification for that manual action. This is a proposal only. Manual delete-all, destructive schedule removal (`DeleteScheduledVolumeBackup` and API deletion at line 314), and history erasure remain separate backlog; this accepted workflow preserves immutable history.
