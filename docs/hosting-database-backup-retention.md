# Hosted PostgreSQL backup retention

Database owners can configure local and remote retention independently on the database backup panel. Viewers can read the policy, saved previews and execution history. Retention concerns hosted PostgreSQL recovery copies; it does not remove databases, application uploads, platform backups or historical operation receipts.

Each location has three limits: `keep_count`, `keep_days` and `max_bytes`. Zero means unlimited for that limit. Limits are combined as a union, so a copy matching any limit can be selected. Local and remote references are computed independently from the newest successful copy recorded at that location.

The rules apply in newest-to-oldest order. Equal timestamps use descending copy
ID as a deterministic tie-breaker.

| Limit | Selection |
| --- | --- |
| `keep_count` | Keep the requested newest prefix and select older copies. |
| `keep_days` | Select copies strictly older than the newest successful copy's timestamp minus the configured days. The current wall clock does not set this boundary. |
| `max_bytes` | Exclude the newest copy from the sum. Add older copies until one crosses the limit, keep that threshold copy, and select only the copies older than it. |

`max_bytes` is a selection threshold. It does not guarantee that the retained byte
sum falls below the limit. Counts cannot exceed 10,000, days cannot exceed 36,500,
and bytes cannot exceed 1 PiB.

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

A reserved copy that was never dispatched is distinct from an actual DELETE with unknown outcome. An unknown nonversioned remote DELETE remains uncertain and reserved: runner exit or lease expiry alone cannot prove an accepted provider request will not finish later. That key cannot be reused or automatically deleted again. Untouched copies can continue in the same batch after old-runner quiescence. If the only remaining work is an unknown nonversioned deletion, a new execution admission returns conflict and retains the hold. Immutable object version identity can permit stronger exact reconciliation. Unknown probe effects retain their evidence until reconciliation proves the outcome. A missing local receipt does not establish success.

Original saved actor authority must remain current for new effects and resume admission. Permanently revoked/orphaned actors are not automatically replaced by another owner. Current permissions, destination grants and saved target references remain necessary; restoring authority is a separate administrative decision.

## Runner and operator requirements

The runner opt-in is `--hosting-database-backup-retention`, alongside the existing logical-backup and remote-backup modes. It advertises `hosting_postgresql_backup_retention_v1` and uses the `postgresql.backup-retention.v1` protocol. Older agents cannot claim retention work. Protected journal version 20 adds retention entries while preserving earlier versions. River carries only the operation ID; secrets remain in the protected, purpose-bound credential transport and are cleared after use. Existing network restrictions, TLS verification, operator endpoint allowlists and credential deadlines continue to apply.

The [selection algorithm](../internal/domain/hosting_database_backup_retention.go)
defines count, age, and size behavior. Resource removal and redeployment do not
implicitly run retention. Use an isolated fixture to qualify conditional deletion
for a provider before relying on it.

A verified remote download can publish a new inode at the original local CAS
location when the recorded file was moved or is missing. The controller records
an immutable supersession link to that exact successful download receipt. It
preserves the prior ownership document and availability history; it does not
claim that the prior inode was deleted. Only the new generation participates in
current availability and retention selection. Reserved or uncertain generations
and locations with held retention reservations cannot be superseded. Adopting an
existing file does not prove replacement and cannot supersede another inode.

<a id="accepted-qualification-and-durable-evidence"></a>

## Tests and supported environments

The [physical fixture](../internal/api/hosting_database_backup_retention_runtime_integration_test.go)
checks local and remote policy execution, restored application reads and writes
after local-copy deletion, new local copy generations, receipt replay, explicit
same-batch continuation, protected copies, revocation, unsupported conditional
deletion, and unknown-delete holds. The desktop browser checks owner policy,
preview, confirmation, and recovery from history. It does not establish viewer
denial or responsive viewport coverage. The [domain tests](../internal/domain/hosting_database_backup_retention_test.go)
cover age and size boundaries separately.

The tested profile is native Linux arm64, PostgreSQL 17.11, trusted rootful
classic-store Docker, and pinned Silo through explicit loopback HTTP on the same
destination. Production S3 HTTPS, other providers, engines, architectures,
cross-host recovery, and destination disk loss are unqualified.
