# Hosted PostgreSQL backup retention

Database owners can configure local and remote retention independently on the database backup panel. Viewers can read the policy, saved previews and execution history. Retention concerns hosted PostgreSQL recovery copies; it does not remove databases, application uploads, platform backups or historical operation receipts.

Each location has three limits: `keep_count`, `keep_days` and `max_bytes`. Zero means unlimited for that limit. Limits are combined as a union, so a copy matching any limit can be selected. Local and remote references are computed independently from the newest successful copy recorded at that location.

The count rule keeps the newest requested number of copies. The age rule selects copies strictly older than the newest successful copy's timestamp minus the configured number of days; it does not use the current wall clock. The size rule preserves Coolify's threshold behavior: it excludes the newest copy, accumulates the others from newest to oldest, finds the first copy that makes the sum exceed the byte limit, preserves that threshold copy, and selects the copies older than it. Equal timestamps use descending copy ID as a deterministic tie-breaker. The threshold copy and every copy preceding it in that order survive the size rule; only the suffix after it is size-selected. Consequently `max_bytes` is a selection threshold, not a guarantee that the retained byte sum will be below it. Counts are bounded at 10,000, days at 36,500 and bytes at 1 PiB.

## Review and execute

Saving a policy uses its revision and the database resource generation. A preview saves the exact eligible copy identities and policy revision. The execution request names that preview and its SHA-256 plan hash. It cannot supply filesystem paths, object keys, credentials or a caller-selected list of deletions.

`selected: true` means the copy is eligible and included in this immutable batch. Policy-matched copies that are protected stay visible with `selected: false` and a reason. At most 100 copies are reserved in a batch and 1,000 inventory rows are returned. Selection considers the complete relevant inventory before bounding the response, so old eligible copies are not starved by protected rows. If more eligible copies remain, `selection_truncated` is true; a later explicit preview can select a later batch. Replaying the same request never advances selection. Inventory exceeding the supported 100,000-row scan fails closed instead of treating unseen references as absent.

The following copies are protected:

- The newest recovery copy at each location.
- Copies shared by another backup or already reserved by a retention operation.
- Copies involved in active backup, restore or transfer work.
- A local copy whose selected remote upload is pending, failed or uncertain.
- External, adopted or legacy copies without an exact platform creation proof.
- Missing, changed or uncertain physical identities.

The local physical key includes the stable server identity, verified private store pool and digest. Exact inode/device/size evidence is checked before unlink. Replacement runner processes on the same server can retain the pool identity; identical filesystem numbers on another server cannot inherit its deletion proof. A matching preexisting copy acquires no creation authority merely because its digest matches. Known exact platform-owned reuse retains the original proof and is protected while shared references exist.

## Remote deletion

Remote execution uses the saved endpoint, bucket, key, credential references and creation evidence. It does not follow later target edits to another location. Current owner, destination, runner, target and credential authority are checked again before execution. Credential replacement revokes the old references; a retention operation cannot silently substitute new keys.

Before deleting any selected remote copy, each execution creates a fresh unpredictable probe inside `nerocd/postgresql/retention-probes/`. The protected runner must observe a wrong `If-Match` DELETE rejected with precondition failure and the probe intact, then delete that exact probe with the correct condition and confirm absence. The target's scoped policy must permit these owned probe keys as well as its selected backup keys. A provider that ignores conditions or denies deletion cannot authorize selected-copy deletion. Probe failure remains visible as `conditional_unsupported` or an uncertain result; an ambiguous probe cleanup remains held with its original journal evidence.

A selected object is checked by saved ETag, version when present, byte count and downloaded SHA-256 before its exact conditional DELETE. ETags are not used as content digests. There is no prefix sweep, list-and-delete operation, or unconditional fallback. Qualified `If-Match` deletion supports owned nonversioned copies during ordinary execution. Provider-specific conditional behavior still requires runtime qualification; an S3-compatible label alone is insufficient.

## Replay and interrupted batches

A successful remote restore download can recreate a local CAS copy after its original local copy was deleted. Its protected completion records the new exact creation proof under a stable download-derived copy ID. The deleted original inventory row stays deleted; a later local restore uses the new available copy. Adopting a matching preexisting file does not invent creation authority, and exact completion replay cannot add another generation.

History and original backup/upload success receipts remain immutable. Per-copy deletion outcomes, copy availability, audit evidence and acknowledgments commit together. A failed retention pass never relabels a successful dump or upload as failed. Automatic retention is admitted after local or remote publication with deterministic identities. A selected upload is persisted and queued before the local-publication retention trigger can select its source copy.

After browser reload, an owner can submit a new execution request ID with the original `preview_id`, `expected_policy_revision` and `expected_plan_hash`. This resolves the same execution ID and copy set. A live attempt is never duplicated. A terminal successful execution is returned without more effects. Otherwise a permitted continuation receives a fresh fenced attempt, separate audited resume admission, and the existing reservations. Completed copy outcomes are preserved.

The original same-root journal must survive runner restart. It uses the existing process-lifetime single-writer lock. Startup replays exact saved completion receipts without repeating deletion. An audited supersession read permits old intent evidence to be carried into the fresh attempt, but gives the old tuple no new mutation or credential authority. A new mutation from that stale tuple is rejected; an exact already committed receipt can still be acknowledged.

A reserved copy that was never dispatched is distinct from an actual DELETE with unknown outcome. An unknown nonversioned remote DELETE remains uncertain and reserved: runner exit or lease expiry alone cannot prove an accepted provider request will not finish later. That key cannot be reused or automatically deleted again. Untouched copies can continue in the same batch after old-runner quiescence. If the only remaining work is an unknown nonversioned deletion, a new execution admission returns conflict and retains the hold. Immutable object version identity can permit stronger exact reconciliation. Unknown probe effects likewise retain their evidence and require deliberate resolution; this slice does not invent success from a missing local receipt.

Original saved actor authority must remain current for new effects and resume admission. Permanently revoked/orphaned actors are not automatically replaced by another owner. Current permissions, destination grants and saved target references remain necessary; restoring authority is a separate administrative decision.

## Runner and operator requirements

The runner opt-in is `--hosting-database-backup-retention`, alongside the existing logical-backup and remote-backup modes. It advertises `hosting_postgresql_backup_retention_v1` and uses the `postgresql.backup-retention.v1` protocol. Older agents cannot claim retention work. Protected journal version 20 adds retention entries while preserving earlier versions. River carries only the operation ID; secrets remain in the protected, purpose-bound credential transport and are cleared after use. Existing network restrictions, TLS verification, operator endpoint allowlists and credential deadlines continue to apply.

This implementation reuses Coolify's local/S3 count, age and size selection behavior from `bootstrap/helpers/databases.php` and `DatabaseBackupJob::removeExpiredBackups`. Go agent leases, durable receipts, explicit immutable batches, exact physical ownership and conditional deletion replace SSH commands and helper-side deletion. Newest/shared/active/unproven-copy protection and retained history are deliberate safety differences. Resource removal or redeployment does not implicitly run retention.

UI, controller transaction tests, portable filesystem/S3 tests and source reviews are separate evidence. Compiled-runner retention, provider conditional DELETE and restored application reads/writes must pass the isolated runtime gate before workflow acceptance. No production user's backup should be used as a test fixture.
