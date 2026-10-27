# Configure the owned-upload archive controller

Owned-upload backups stop the exact running writers, archive an owned local volume,
and resume those writers. Restore extracts into a separate resource and volume.
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
