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

## Implementation

The [transfer executor](../internal/runnerruntime/hosting_database_backup_remote_execution.go),
[S3 client](../internal/runnerruntime/hosting_database_backup_remote_s3.go),
[credential service](../internal/app/hosting_database_backup_remote_credentials.go),
and [transactional admission](../internal/store/postgres_hosting_database_backup_remote_admission.go)
implement transfer, credential renewal, and replay. Failure alerts use the separate
[notification workflow](hosting-backup-notifications.md).

<a id="accepted-qualification-and-durable-evidence"></a>
<a id="upstream-behavior-and-go-adaptation"></a>

## Tests and supported environments

The [runtime scenario](../internal/api/hosting_database_backup_remote_runtime_integration_test.go)
and [browser scenario](../web/app/tests/browser/database-remote-backups-live.spec.ts)
cover denied upload with local success, restart and reload history, same-archive
retry without another dump, lost completion acknowledgement, actual GET after
the local copy is withheld, and independent restored application reads and writes.
They do not establish exactly one PUT. Target creation and validation use API
setup. Browser history checks cover 320, 390, 430, and 1440 pixels.

The tested profile is native Linux arm64, PostgreSQL 17.11, trusted rootful
classic-store Docker, and pinned Silo on the same destination through explicit
loopback HTTP. Production S3 HTTPS, certificate installation, other providers,
cross-host recovery, and destination disk loss are unqualified.
