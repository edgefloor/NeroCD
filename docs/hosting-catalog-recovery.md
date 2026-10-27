# Back up and recover a catalog application

The Linkding installation uses ordinary image deployment with platform-owned
storage and managed runtime secret references. Its storage archive follows the
same owner-authorized backup and new-target restore routes as upload archives.
This extension accepts the explicit
`public-image.owned-storage.runtime-env.release-ports.v1` family alongside
`public-image.owned-storage.release-ports.v1`. It does not admit generic Compose,
local builds, database resources, or application/database bindings.

A backup pins the settled healthy deployment, including its runtime generation
and document hash. The runner inspects every controller-derived writer, records
its original running state, and durably acknowledges stop intent before stopping
it. Archive execution stops and starts the exact existing container. It does not
fetch runtime secret values, recreate the application, or generate credentials.
Retained writers that were already stopped remain stopped.

Restore creates a separate resource, binding, and volume in the same project and
destination. The transaction loads the source runtime document at the generation
and hash in the backup's healthy snapshot. It validates the document and current
managed-secret version authority, then copies public literals and exact secret
references into version one of the target's runtime configuration. Changes to the
source's latest desired configuration do not change that copy. No plaintext
credential or new password enters the restore plan, receipt, audit, or journal.
Same-project reference sharing follows the existing managed-secret contract.

The resource, storage, runtime version, normalized references, receipts, audits,
restore hold, and archive job commit atomically. A missing document, mismatched
hash, unavailable secret version, revoked secret, or denied owner/destination
access prevents a partial target from committing. An identical request returns
the original restore identity after current source and target authorization and
secret-reference checks. It does not create another resource or volume.

The target is an ordinary image application with pinned runtime and storage.
Restore does not copy catalog installation identity, routes, triggers, or an
application readiness result. The restore runner extracts and validates bytes.
After file validation clears the target hold, ordinary deployment supplies runtime
values through protected delivery and establishes application readiness. A fresh
Linkding login and bookmark read/write check are separate application checks.

A failed resume remains an unresolved writer obligation. Existing fenced journal
and owner recovery rules retain the mutation hold until current authority proves
that the original running writers resumed. Recovery does not rerun archive
creation or extraction and cannot start an originally stopped writer. A runner
must advertise the existing combined storage/runtime capabilities as well as the
archive family before it can receive a combined archive plan.

<a id="catalog-backup-and-recovery-contract"></a>
<a id="upstream-behavior-and-deliberate-difference"></a>

## Source and recovery rules

The [restore transaction](../internal/store/postgres_hosting_storage_archive_restore.go)
and [runtime document handling](../internal/store/postgres_hosting_storage_archive_runtime.go)
validate the pinned configuration and current secret references.
The archive runner uses exact writer identities and durable resume obligations.
If a writer fails to restart, follow owner recovery and preserve the source.
Recovery does not recreate the application or rerun extraction.

## Qualification boundary

`acceptance/runtime-hosting-catalog/gate.sh` reuses the existing owned storage
fixture and builds the exact archive helper for the candidate. The connected
browser drives catalog installation, deployment, backup, new-target restore,
and ordinary deployment of the restored resource through the existing UI.
The Go fixture then exercises Linkding's real login and bookmark API.

The extended fixture checks an archived bookmark, a source mutation after backup,
a distinct restored volume, the pinned runtime document despite changed desired
configuration, a fresh login with the original referenced credentials, and new
writes isolated from the source. It also checks UID/GID, modes, private metadata,
completion replay without duplicate helper execution or materialization, and an
exact-container start failure followed by owner recovery. Browser assertions
include desktop and 320, 390, and 430 pixel widths for the restore controls.

The [catalog runtime guide](../acceptance/runtime-hosting-catalog/README.md#final-physical-qualification)
describes the application checks and tested environment. The profile is Linkding
1.47.0 on native Linux arm64, trusted rootful classic-store Docker/overlay2, the
same destination, and private fixture-CA HTTPS. Production ACME, generic Compose,
catalog version upgrades, cross-host recovery, in-place restore, and destination
disk loss are unqualified. Local archives depend on the destination's retained disk.
