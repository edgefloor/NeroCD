# Configure the owned-upload archive controller

Controller, startup configuration, public API and default-off runner code are
accepted through `88887e92c0cf7b93b828d3696ec3a5af16547ea7`, with migration 0068.
Helper packaging is accepted at `b796ce483e99d0dec29bb3e769982dc30da64668`.
The [API tests](../internal/api/hosting_storage_archive_handlers_test.go) and
[runner tests](../internal/runnerruntime/hosting_storage_archive_controller_test.go)
provide contract/lifecycle evidence. Actual PostgreSQL/River/controller tests use
a fake engine; later bounded physical uploaded-file recovery is accepted below.
Archive UI code/browser acceptance is recorded below. Bounded native Linux arm64,
classic-store owned-upload application backup/new-target restore is physically
accepted; broader recovery and full A26/A37 remain unfinished.

The source-bound runner handoff is
`/tmp/nerocd-archive-runner-integrated-handoff.json`, with promotion recorded in
`/tmp/nerocd-main-archive-runner-promotion.json`. It records focused race, vet,
introduced-code lint and Go build passes, not a whole-repository lint-clean or
physical A26/A37 result. Previous database physical qualification remains at
`c731f708e62e2376d174041957b8654fe83d5bcf`; R42 application binding remains open.

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

Archive UI source `d98e88c64f84924bd36fb2d4c7c29ad723e80ced` is accepted at
`a99a75517c018caa9aa9965cf451f9f826e7b3d6`. Retained evidence records 1,426
frontend tests, TypeScript, generated API, Go/frontend builds, a 75-file embedded
manifest and 74 matching served assets. Browser responses are intercepted and
qualify UI behavior, not physical archive backup/restore. Exact evidence is in
`/tmp/nerocd-archive-ui-integrated-handoff.json` and
`/tmp/nerocd-main-archive-ui-promotion.json`. The new built artifact remains in the
integration checkout; root ignored assets were untouched.

The current helper identity path uses a Docker image configuration ID. A Docker
29 containerd image-store attempt could not inspect the BuildKit image by that
configuration ID; the failed attempt remains at
`/tmp/nerocd-storage-runtime-20261024.2LlLIL`. Current qualification targets an
explicit classic Docker image store. No runtime fallback is implemented, no
universal Docker 29 support is established, and no future physical test pass is
claimed. The bounded physical uploaded-file slice is accepted below; full A26/A37 remains incomplete.

Bounded physical uploaded-file recovery is accepted at
`45298ea4bbd85c91ee4b0d4d64cd72d9346e1faa`, with exact source/tree equality to
`2ba8041359576a32d616d7f83e0c7eafe4ee4d91`. The 25.38-second pass is retained in
`/tmp/nerocd-storage-runtime-20261024.iWwXss/gate.log`, with
`/tmp/nerocd-archive-physical-integrated-handoff.json`,
`/tmp/nerocd-archive-physical-integrated-physical-retention.json` and
`/tmp/nerocd-archive-physical-integrated-main-promotion.json`. The application
retains 65,537 uploaded bytes through redeploy. Committed backup and restore ACK
loss each recover with one helper start. A new resource/volume reads restored
application data and accepts a new write; the source remains unchanged with
UID 12345/GID 12346, files 0640 and directories 0750. Oversized backup failure
resumes only originally running workloads; failed-readiness rollback and
missing-volume hold also pass. Owned objects/processes are absent after cleanup
and host inventories are unchanged. The tested helper image was removed and is
not an installed reusable pin. Earlier failures remain historical.

This is native Linux arm64, rootful same-host owned uploads on the classic Docker
image store/overlay2 with trusted fixture CA and local CAS. It does not qualify
database backup, S3, host disk loss, in-place restore, containerd image store,
production TLS, browser-to-real-archive flow, full A26/A37 or full parity. R42
PostgreSQL/Redis application binding is under implementation and unaccepted.

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

Archives use the versioned uncompressed `nerocd-storage-v1` format and remain in
the destination-local private artifact store. This controller concerns owned
upload volumes and restore into a new volume. It does not provide database
backups, S3 storage, in-place restore, or application database bindings. Public
API and runner code are accepted, but application-verified A26 and A37 require
separate physical end-to-end evidence.
