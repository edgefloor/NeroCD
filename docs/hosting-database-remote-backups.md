# Hosted PostgreSQL remote backups

Remote backups copy a completed hosted PostgreSQL custom-format archive to a
project-owned S3-compatible destination. Local snapshot creation and remote
transfer have separate outcomes. A failed upload does not erase the local dump
or change its successful backup receipt.

This workflow supports the pinned PostgreSQL recipe on the same authorized
Docker destination and runner. It does not implement storage-volume archives,
controller-database backups, arbitrary SQL uploads or object browsing.
[Independent retention](hosting-database-backup-retention.md#accepted-qualification-and-durable-evidence)
is qualified separately; uploading an archive does not itself remove the local copy.

## Enable the runner

Use the existing database execution roots and logical-backup configuration,
then add `--hosting-database-remote-backups`. The runner advertises both
`database_backup_transfer` and `hosting_postgresql_remote_backup_v1`. An older
runner cannot claim these jobs. Hosting execution must be running on the
controller so its existing River consumer can dispatch transfers.

The runner delivers credentials only over the protected controller transport.
Access and secret keys use the existing envelope encryption and operator key
recovery configuration. Ordinary project API responses, task arguments,
execution plans and journal records contain no plaintext S3 credentials.
Transfer plans contain opaque credential references and a saved target revision.
Each protected credential delivery expires within one minute. Signing and active
HTTP bodies remain bound to that grant. Longer transfers obtain fresh protected
deliveries before expiry using the same immutable references, plan and full
attempt authority; the returned key bytes must match. Only a server-issued
`use_until` can renew that use. Revocation, a late refresh or caller cancellation
cancels the active request and forbids later signing. The original operation
timeout remains the outer bound; renewal never extends it.
The protected delivery response binds the purpose, target revision, plan hash,
runner, full attempt authority and deadline. Buffers owned by NeroCD are cleared;
the standard Go JSON and SDK signing implementations also create temporary
strings which NeroCD cannot erase individually.

## Network policy

HTTPS with certificate validation is the default. The S3 client disables
environment proxies, redirects and automatic SDK retries. It validates DNS
answers, pins connections to the initial validated address set and rechecks DNS
before each request. A changed address set fails closed. Metadata, link-local,
loopback and private addresses are rejected by default.

For a private HTTPS S3 service, the operator must configure both an exact origin
and private CIDRs, for example:

```text
--hosting-backup-remote-endpoints=https://s3.internal.example:9000
--hosting-backup-remote-cidrs=10.20.30.0/24
```

Comma-separated origins and CIDRs are bounded to 32 entries each. Every private
answer must be covered by the protected configuration. The endpoint needs a
certificate trusted by the runner's system trust store. Project owners cannot
add network exceptions through the UI or target API.

`--hosting-backup-remote-trusted-loopback` accepts one exact numeric loopback HTTP
origin for isolated fixtures. It is disabled by default. HTTP fixture evidence
does not qualify production HTTPS endpoints or their certificate deployment.

## Configure and use a destination

An owner creates a destination with a name, endpoint, bucket, nonempty region and
write-only access/secret keys. Endpoints have no user information, query,
fragment or non-root path. Validated strings are saved without normalization.
Viewers can read nonsecret destination and transfer metadata, but cannot save,
validate, retry or restore.

Validate the saved revision against the database's selected destination. A
successful validation proves `ListObjectsV2` reachability from that exact
runner/destination scope. It does **not** prove permission to upload an object.
Immediate backups and schedules select `{target_id, revision}` explicitly.
Admission requires a successful validation for the exact revision, runner,
destination and daemon. A schedule keeps its selected revision until reviewed
and edited; a later target edit never silently redirects a scheduled occurrence.

On local completion, one transaction saves the existing local success receipt,
creates the immutable upload intent and enqueues its operation ID. Replaying the
same local completion cannot create a second upload. Upload execution has its
own task, lease, attempt and fence. It owns no live source-database mutation hold.
Source database maintenance can proceed while the immutable archive uploads.

The stable object key includes the backup ID and full SHA-256 digest. NeroCD
uses conditional creation and verifies the complete object body, byte count and
`PGDMP` header. An S3 ETag is not accepted as a content digest.

## Failed uploads and retry

History shows the local backup and remote transfer separately. A destination
which permits listing but denies `PutObject` produces a failed transfer while
the local backup remains successful and available for local restore.

After correcting permissions, retry the selected failed or uncertain upload.
The retry creates a new fenced transfer attempt using the same backup, local
blob, digest and saved object location. It does not invoke `pg_dump`. If an
earlier request may have written the object, the new attempt verifies the
existing object before deciding whether conditional creation is needed.

Credentials and current owner/project/destination/runner authority are checked
again at execution. Disabling a target blocks new effects. Configuration-only
edits preserve historical snapshots. Replacing credentials revokes the previous
encrypted references, so old snapshots cannot silently borrow the new keys.
Keep the local archive when replacing credentials needed by older backups.

`publication_failed` on the existing local backup means local content-addressed
publication failed. It is not an S3 error. Completed staging is retained for
local recovery; remote transfer failures do not delete it.

## Restore a recorded remote backup

Choose a successful upload and request a new private database. The restore
source is the saved transfer ID and its historical target revision. It is not
an arbitrary endpoint, object key, path or URL. Harmless target edits do not
change that saved location; current target enablement, permissions and exact
credential-reference revocation still apply.

The controller atomically creates the independent database, storage and new
encrypted database credentials under the existing restore hold. A separate
download task performs a real authenticated GET even when the local archive
exists. It checks the saved size, SHA-256 and custom-format header before local
publication. The normal fresh-target restore task cannot execute before that
exact download succeeds. Failed downloads retain the target hold and report an
uncertain restore; they never make an empty database available for applications.

The existing restore runs `pg_restore` with `--exit-on-error`,
`--single-transaction`, `--no-owner`, `--no-acl` and the target application role.
It imports no source roles or passwords. The source database and its credentials
are not required to remain current or running merely to restore an immutable
completed backup. The recovered database can then be bound to a distinct
application through the existing reviewed connection workflow.

## Replay and limits

Transfer intent and result requests are journaled before sending. A lost result
acknowledgment replays the identical historical receipt and does not repeat
network transfer or database import. A missing result never proves success.

For an upload-only intent whose original lease expired, the authenticated
original runner can classify uncertainty using the exact saved intent, tuple
and plan. This exception grants no credentials, remote effect or successful
completion. It records why expiry classification was accepted and retires only
acknowledged journal evidence. A user retry still needs current authority and a
new fence. Revoked agents cannot authenticate this classification; permanently
orphaned authority is not repaired automatically.

Download/import uncertainty remains held. The local restore crash window after
database commit but before durable result evidence is not inferred as success
or blindly imported again. Local archives remain the default recovery source.
Retention and local deletion have a [separate accepted workflow](hosting-database-backup-retention.md#accepted-qualification-and-durable-evidence).
Remote object browsing and additional engines remain outside this workflow.

## Upstream behavior and Go adaptation

The upstream comparison uses Coolify
`b12d1386f3d458618b4c579babcd1e339c6c40b3` in
`/Users/bubble/Documents/Dev/coolify-reference`. The relevant contracts are:

| Upstream source | Behavior carried into this bounded workflow |
| --- | --- |
| [S3Storage](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Models/S3Storage.php), [Create](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Livewire/Storage/Create.php), [Form](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Livewire/Storage/Form.php) and [S3StoragesController](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Http/Controllers/Api/S3StoragesController.php) | Scoped destination configuration, hidden encrypted keys and explicit connection validation. NeroCD saves immutable project target revisions and validates from the selected runner. |
| [DatabaseBackupJob](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Jobs/DatabaseBackupJob.php) and [BackupEdit](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Livewire/Project/Database/BackupEdit.php) | PostgreSQL custom dumps, explicit S3 selection and separate local/upload outcomes. Failed upload preserves useful local recovery. Retention and local deletion are accepted separately in the retention guide; notifications remain pending. |
| [ImportForm](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Livewire/Project/Database/ImportForm.php) and [StartDatabaseImport](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Actions/Database/StartDatabaseImport.php) | Select a saved S3 source and import database data. NeroCD restricts selection to a successful recorded transfer and restores into a fresh private database. Arbitrary keys, upload formats and replacement options are outside this workflow. |

Upstream transfer/import uses SSH-managed Docker helpers and `mc`. NeroCD uses
the Go AWS SDK through the protected network policy, purpose-bound credential
delivery and durable lease/attempt/fence receipts. The
[transfer executor](../internal/runnerruntime/hosting_database_backup_remote_execution.go),
[S3 client](../internal/runnerruntime/hosting_database_backup_remote_s3.go),
[credential service](../internal/app/hosting_database_backup_remote_credentials.go)
and [transactional admission](../internal/store/postgres_hosting_database_backup_remote_admission.go)
implement this adaptation. It does not claim Coolify API or migration compatibility.

## Accepted qualification and durable evidence

The bounded remote workflow is accepted on main at
`6621204964e96039558c0f267dbac50475a7ad98`, tree
`c2dba0c2a9ff32a7e6b433283a750da82836f9fe`. The reviewed source
`ba63eeefcb98eda32fb18184f10c794357bb114b` has that exact tree. Migration
0077 adds versioned targets, transfers and protected credential deliveries.
Acceptance covers native Linux arm64, PostgreSQL 17.11, trusted rootful Docker
with its classic image store, and the pinned Silo image on one declared
same-destination runner. Silo uses an explicitly trusted loopback HTTP origin.
This does not qualify production S3 HTTPS, certificate deployment, cross-host
recovery or loss of the destination's disk.

The [compiled-runner scenario](../internal/api/hosting_database_backup_remote_runtime_integration_test.go)
`TestHostingDatabaseBackupRemoteCompiledRunner` and
[live browser scenario](../web/app/tests/browser/database-remote-backups-live.spec.ts)
prove the following sequence:

1. The fixture creates a versioned encrypted target and validates real
   `ListObjectsV2` access through the API. Target creation and validation are API
   setup, not a live-browser configuration proof.
2. The owner selects that target and requests a backup through the embedded UI.
   Silo allows listing but denies `PutObject`. Local success and failed remote
   transfer remain distinct across controller restart, runner restart and browser
   reload. The browser displays the warning and preserved local snapshot.
3. The fixture grants upload permission without changing the target revision or
   credentials. The owner retries through the browser. The retry keeps the same
   archive, digest and object key, and the fixture proves no second dump was
   admitted or planned. A lost committed upload acknowledgment replays the same
   request and response after runner restart. This is not an exactly-one-PUT claim.
4. The fixture verifies and withholds only its exact local content-addressed
   archive. The browser selects the saved remote object and requests a new
   database. A real authenticated GET republishes the archive with the same
   SHA-256 before import. This isolates remote recovery from a local CAS cache hit.
5. The restored database has a distinct resource, binding, volume and encrypted
   credential identity. A distinct application reads the snapshot record and
   writes and reads new data. The original application retains its post-snapshot
   record and remains usable. The fixture checks completed history at 320, 390,
   430 and 1440 pixels.

Stable evidence is under
`/Users/bubble/.local/share/nerocd/acceptance/postgresql-remote-backups-20261025/accepted-runtime/`:

| Receipt | Observed result |
| --- | --- |
| `verification.json` and `result` | Candidate/tree above, gate exit 0, full workflow pass, four top-level groups with zero failures or skips, all 13 required markers exactly once. Verification receipt SHA-256 is `1fbb779075c7a2ef1ea4fab5911d42226c1d76db1ca693871f1f4b7854b66abc`. |
| `runtime-results-redacted.log` | Remote group 37.46 seconds, local group 45.52, schedule group 43.27 and failures group 31.49. Corrupt, truncated and revoked subtests pass. These are group durations, not total delivery time. |
| `backup_remote-browser-last-run.json`, `backup-browser-last-run.json`, `backup_schedule-browser-last-run.json` | All three browser profiles passed with no failed tests. The scheduled workflow is regression coverage; its earlier accepted R46-C02 credit is retained separately. |
| `backup-preservation-reconciled.json`, `process-absence` and `verification.json` | Owned resources removed, no canonical baseline changes to 35 containers and 210 volumes, and the selected existing user UI profile unchanged. |
| `main-integration-verification.json`, binary build-info/digest files and `evidence-sha256.json` | Fast-forward to accepted main, exact approved tree identity and durable evidence digests. Integration did not rerun the full tests because the exact accepted tree was already qualified. |

The original physical files remain at
`/tmp/nerocd-database-runner-20261024.35r17try/`. Earlier full attempts
`/tmp/nerocd-database-runner-20261024.x0c98tlo/` and
`/tmp/nerocd-database-runner-20261024.zkx52pev/` remain failed evidence, at
109.88 and 21.04 seconds respectively. They do not become passes after repair.
The reviewed repair compares credential grant deadlines with `time.Time.Equal`
in [credential revalidation](../internal/store/postgres_hosting_database_backup_remote_credentials.go).
Structural comparison distinguished equal instants with different UTC location
representations. The retained credential-repair checks include real PostgreSQL
and TLS/race coverage. Under the sibling `credential-repair/transaction-pg/`
directory, `credential-utc-green2.json` records UTC exit 0 in 9.716 seconds and
`credential-http-final.json` records UTC exit 0 in 7.318 seconds. Their matching
logs retain the named checks. The final physical pass above qualifies the
repaired tree.

The [checklist](parity-checklist.json) records eight bounded validated remote
assessments, R46-C03/C14/C16, R47-C01/C02 and R48-C01/C08/C09. The latter three
extend the earlier local-restore evidence to this remote source without adding
duplicate historical credit. R29-C12 remains partial for the broader managed
credential contract. The historical audit remains 30 verified outcomes out of
933 eligible outcomes. No complete R46/R47/R48, A25/A26/A37, database matrix,
migration or unchanged-client gate gains acceptance.

Target configuration edits preserve saved locations and historical references.
Explicit key replacement revokes old references, so an older remote-only backup
can become unrecoverable with those references. Local archives remain available
under their existing authority. Independent local/S3 retention and deletion are subsequently accepted at
`5003e16c0c0fc48d31c29f444f47a97b94e10339` within the
[retention profile](hosting-database-backup-retention.md#accepted-qualification-and-durable-evidence).
Notifications, other engines, in-place restore, broader cron grammar and
production HTTPS qualification remain next hosting gaps. The user's existing UI still runs its
older source; fixture qualification does not upgrade that process.
