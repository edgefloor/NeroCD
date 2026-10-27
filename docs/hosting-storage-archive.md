# Configure the owned-upload archive controller

Owned-upload backups stop the exact running writers, archive an owned local volume,
and resume those writers. Image-resource restore extracts into a separate resource
and volume. Managed Compose supports immediate and scheduled local named-volume
backups.
Configure the controller and matching runner helper before admitting this work.

The server's archive controller is disabled by default. Set
`NEROCD_HOSTING_STORAGE_ARCHIVE_POLICY_FILE` to an operator-provisioned JSON file
to enable admission with an explicit helper descriptor and limits. PostgreSQL
must be configured through the normal server configuration. Development memory
mode cannot enable this controller.

The server reads this file once at startup and calls
`PostgresStore.ConfigureStorageArchive` before starting hosting job consumers.
Invalid configuration aborts startup. There is no reload, default helper,
daemon discovery, image pull, or dependency on the hosting build policy.
Leaving the variable unset disables new archive admission; it does not delete
existing operations or retained archives.

## UI acceptance and image-store boundary

The helper is pinned by its local Docker image configuration ID. Qualification
uses a classic Docker image store with overlay2. A Docker 29 containerd image-store
attempt could not inspect the BuildKit helper by that ID; no fallback is implemented.
Do not infer support from the Docker major version alone.

The [remote backup evidence](hosting-storage-remote-backups.md#accepted-qualification-and-evidence)
includes a rerun of local archive backup and new-volume restore. The profile is
native Linux arm64, trusted rootful Docker, one destination, and private fixture-CA
HTTPS. It verifies uploaded bytes and numeric owners/modes after restore, writer
recovery, lost completion replay, and restored application reads and writes.
Production ACME, containerd image stores, arbitrary mounts, disk loss, in-place
restore, and cross-host recovery are unqualified. Earlier temporary local receipts
are no longer available; use the retained remote-workflow evidence.

## Provision an exact helper identity

The first supported helper profile is native Linux arm64 with a rootful,
same-host Docker daemon and destination-local owned upload volumes. Configure
only a profile installed and verified on the intended runner. A policy file
does not install a helper, advertise a runner capability, or qualify a workflow.

These are separate operator steps:

1. Prepare a clean, offline build context using the
   [helper packaging instructions](../internal/storagearchive/README.md). Its
   provenance records a source commit and executable SHA-256. A prepared context
   is not an image.
2. Build and verify the fixed scratch recipe through an authorized installation
   process. Record the actual local image configuration ID, executable hash,
   source commit, labels and daemon identity. A local image ID is not a registry
   manifest digest. Do not use a deleted qualification image as an installed pin.
3. Install the matching trusted runner profile and provision the controller
   descriptor with those observed identities. The runner must independently
   verify the descriptor, daemon, image and owned storage at execution time.
4. Qualify the complete backup and restore workflow for that installation before
   relying on it. This startup configuration change does not establish that
   physical workflow qualification.

The file has exactly two required top-level objects, `helper` and `limits`.
The following template deliberately contains placeholders and cannot load until
every identity and its descriptor hash has been supplied:

```json
{
  "helper": {
    "mode": "local_image",
    "recipe_version": "owned-upload.local-archive.v1",
    "image_id": "<actual local sha256 image configuration ID>",
    "executable_sha256": "<sha256: followed by 64 lowercase hex digits>",
    "source_commit": "<40 lowercase hex digits>",
    "os": "linux",
    "architecture": "arm64",
    "daemon_id": "<verified intended runner daemon ID>",
    "hash": "<computed complete helper descriptor hash>"
  },
  "limits": {
    "archive_bytes": 1073741824,
    "data_bytes": 536870912,
    "file_bytes": 268435456,
    "members": 100000,
    "depth": 64,
    "path_bytes": 255
  }
}
```

Compute `helper.hash` with the repository's
`domain.HostingDocumentHash` over a `domain.HostingStorageArchiveHelper` value
whose `Hash` field is the empty string. Supply that result as `hash`; changing
any identity requires recomputing it. JSON field ordering in the policy file
does not affect validation. `variant` may be omitted or empty; nonempty variants
are rejected. Image and executable hashes both require the `sha256:` prefix.

All six limits must be explicit positive integers. There are no zero defaults.
`archive_bytes` must be between 1,536 and 1,073,741,824 inclusive.
`data_bytes` cannot exceed `archive_bytes`; `file_bytes` cannot exceed
`data_bytes`. `members` is at most 1,000,000, `depth` at most 128, and
`path_bytes` at most 255. Set limits appropriate to the actual upload data and
runner resources rather than copying the template without review.

Use a regular file of at most 65,536 bytes at a path without leading or trailing
whitespace. Symlinks, directories, FIFOs, duplicate keys, unknown fields, case
aliases, trailing JSON, null objects and invalid descriptor hashes are rejected.
Keep the file and its parent directories under trusted operator control.
Startup errors omit the file path, contents and decoder diagnostics.

```sh
export NEROCD_HOSTING_STORAGE_ARCHIVE_POLICY_FILE=/etc/nerocd/storage-archive.json
nerocd server
```

Archives use the versioned uncompressed `nerocd-storage-v1` format. The local
controller concerns owned upload volumes and restore into a new volume.
[Remote backups](hosting-storage-remote-backups.md) add independent S3 transfer
and verified download before new-volume extraction. [Database backups](hosting-database-backups.md) and
[application database connections](hosting-database-connections.md) have separate contracts.

## Managed Compose volume selection

The same controller policy and helper limits apply to
[managed Compose named-volume backups](hosting-compose.md#local-named-volume-backups).
The helper recipe remains `owned-upload.local-archive.v1`; Compose plans use the
separate `managed-compose.local-volume-archive.v1` family. A configured archive
runner with managed Compose support advertises
`hosting_compose_storage_archive_v1`. Archive configuration alone does not enable
Compose execution.

`GET /api/v1/resources/{id}/storage-backup-volumes` returns the current resource
generation, settled operation, availability reason, and named volumes with
component mount paths and read-only flags. Discovery requires current viewer
access. This response records controller evidence; execution rechecks actual
Docker identities and mounts.

`POST /api/v1/resources/{id}/storage-backups` requires a 32-character lowercase
hexadecimal `request_id`, `expected_resource_generation`, `volume`,
`expected_deployed_operation_id`, and `acknowledge_interruption: true` for Compose.
The three Compose selector fields must appear together. An immediate backup
accepts optional `remote_target`; see the workflow below. Existing image
backup requests keep their original contract.

Backup list and detail records include `volume` and
`source_deployed_operation_id`. Current authorization applies before identical
request replay. Replay precedes new runtime eligibility checks, so an identical
accepted request can recover its receipt when the runtime is unavailable.
Authorized history remains readable independently of new backup availability.

The existing
`POST /api/v1/hosting-operations/{id}/storage-archive-recovery` endpoint handles
unresolved writer resumption with an exact plan hash and a fresh request identity.
[Local Compose named-volume schedules](hosting-storage-backup-schedules.md#schedule-a-compose-named-volume)
reuse these archive plans and limits. A schedule fixes the selected volume's
origin and resolves the current settled deployment at each due admission.
Compose restore remains rejected. A successful local archive does not establish
a coordinated database and upload recovery point.

The [immediate Compose remote-copy workflow](hosting-storage-remote-backups.md#copy-an-immediate-compose-volume-backup-remotely)
adds an optional validated `remote_target` to the existing backup request and uses
`managed-compose.remote-volume-archive.v1` plans. It retains the same helper and
limits. Writer recovery and local completion can finish while upload dispatch is
unavailable, leaving the original transfer queued. Compose schedules stay local.

## Download a local archive

Enable `--hosting-storage-backup-download` on both the server and runner. Keep
`NEROCD_HOSTING_STORAGE_ARCHIVE_POLICY_FILE` configured on the server and the
matching `--storage-archive-config-file` on the runner. Both download flags are
disabled by default. No S3 target is required. The runner advertises
`hosting_storage_backup_download_v1` with the existing storage transfer capability.

As an authorized project owner or administrator, select a successful local backup
and click **Download archive**. **Refresh download availability** checks the
current blob and authority. The control supports ordinary owned-storage backups
and Compose named-volume backups without requiring a remote copy.
`GET /api/v1/storage-backups/{id}/archive-options` reports availability.
`GET /api/v1/storage-backups/{id}/archive` returns an `application/x-tar`
attachment with the exact recorded length, `no-store`, and `nosniff`.
The bytes are the uncompressed USTAR archive, not a newly generated backup.
Downloading does not stop writers or create another archive.

The broker supports one controller, with two concurrent controller streams and
one per actor. Initial metadata work has a five-second budget. Runner admission
waits at most 30 seconds, and streaming is bounded to 300 seconds. Missing or
tampered local archives return 404 before an attachment is committed. A truncated
or mismatched stream fails rather than completing a valid download. Current
authorization and exact fenced runner authority remain required.

### Local download qualification

Accepted source `ff6b200cb198d621bd9bd6a429ee4a81fda45478` passed the existing
managed Compose gate on 2026-10-27, 19:00:09–19:04:21 UTC, in 252 seconds.
The API runtime passed in 163.33 seconds. Native Chromium downloaded the exact
5,120-byte selected blob. History and writer events stayed unchanged, and the
runner then completed remote validation, scheduled transfer and same-archive
retry. Records, uploads, routes, failed-replacement recovery, and exact cleanup
of the 41-container/236-volume baseline passed.

The physical profile is native arm64, rootful Docker, fixture-CA HTTPS, and one
controller. Ordinary image support has real HTTP/PostgreSQL and runner proofs,
but no new ordinary-image physical qualification. Compose archive restore,
cross-host recovery, and broader architectures remain unqualified.

The audited [evidence index](/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-archive-download-20261027/ff6b200cb198d621bd9bd6a429ee4a81fda45478/evidence-index.json)
has SHA-256 `30ad793f14a2890d8a2f2c1bc9cd15df8d12adab3c0008fd4b31e5cb3fcbc50a`
and 28 safe files. It retains exact final Go tests, race/static/build checks,
75 embedded UI assets, dependency-attributed WebUI 2,025-test evidence, and links
to four separate failed receipts. Raw diagnostics and archive payloads are not
shareable evidence.

| Delivery category | Evidence and limits |
| --- | --- |
| Implementation | Worker and integration checks retain their source revisions. Exclusive implementation time was not measured. |
| Product repair | Metadata deadlines, UI actor identity, pre-existing Compose completion cancellation, and composed download completion were repaired with focused proofs. Command windows overlap implementation and validation. |
| Fixture repair | The paginated history decoder was corrected after the first failed gate. The real encoder/decoder regression is retained. |
| Test execution | Failed gates took 321, 239, 215, and 408 seconds. The accepted gate took 252 seconds. Their total is 1,435 seconds; API runtimes are subsets, not additional time. Final standard Go tests passed 18:56:23–18:59:38 UTC; PostgreSQL evidence is separate. |
| Review and waiting | Three exact final reviews passed. Earlier repair reviews and validation overlapped; exclusive waiting durations are unavailable. |
