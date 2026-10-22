# Private Git credential delivery component

This component authorizes and decrypts private Git source credentials for a live
build's sourcing phase. It does not register an API endpoint, controller,
runner executor, or public Git build workflow.

## Authorization contract

`HostingSourceCredentialRequest` contains a full `HostingBuildAuthority` and an
opaque `DeliveryID`. IDs are canonical lowercase hexadecimal encodings of 128
or 256 random bits. Future clients must generate them with a cryptographic
random source. The request cannot choose a secret or credential kind.

`AuthorizeHostingSourceCredential` takes a separately authenticated runner ID.
It uses the existing build transaction order: task and lease, destination and
resource, creator authorization, source and target, credentials, then build.
It requires a live complete lease tuple, current enrolled dedicated runner,
active target grant, current creator role, and a sourcing build without a
committed source receipt. Current resource generation and the complete current
source body must equal the immutable snapshot. Public sources are refused.

The snapshot's exact active source grant selects the project-owned secret and
immutable version. Rotation can leave that old selected version usable while
secret metadata remains active. Secret or grant revocation denies new delivery,
including replays. No issuer, tag, project, or secret-reference fallback exists.

## Audit-backed windows

The existing append-only audit ledger stores an authorization record whose ID
uses a fixed namespace and `DeliveryID` alone. An advisory transaction lock is
acquired after the build lock; no other build's locks are acquired afterward.
Concurrent identical requests share one audit and window. Reusing the ID for
a changed runner, build, lease, fence, or request conflicts.

The closed audit metadata contains purpose `git_fetch`, runner and full attempt
tuple, source/input/project/grant/secret references, request hash, and `UseUntil`.
It contains no credential values, ciphertext, key IDs, or transport diagnostics.
The first window ends at the earlier of database time plus 60 seconds and lease
expiry. Replay reauthorizes current live sourcing state and returns the original
window. Lease renewal cannot extend it; a fresh ID may open a new live window.

Authority and the original window are checked with the database clock after the
audit insert and all blocking work, immediately before commit. Failure returns
no envelope and rolls back a newly inserted audit. This delivery path never
uses historical source-receipt replay as credential authority.

## Application component and byte ownership

`NewHostingBuildCredentialDelivery` is constructed separately from `Service`
with authentication, a persisted `CurrentRunnerIdentity` verifier, the repository,
an immutable keyring, and a trusted clock. `Deliver` derives runner identity
from a dedicated runner principal and verifies the current persisted identity.
It decrypts only after repository authorization has committed, using the existing
project/secret/version encryption binding.

Missing keys, incorrect keys, malformed ciphertext or credential payloads, and
repository failures produce a fixed unavailable error without diagnostics.
The component checks its window again before returning any bytes. The audit
means authorized access, not confirmed delivery; it may remain when decryption
or payload validation fails. Revocation after authorization cannot recall bytes
already delivered, and this window is not a revocation mechanism for a Git PAT.

HTTPS basic credentials use a UTF-8 JSON object containing exactly `username`
and `password`. Both must be nonempty; username is limited to 256 bytes and the
whole encrypted-secret plaintext to 65,536 bytes. Duplicate or unknown fields,
trailing JSON, and control characters are refused. SSH credentials use a raw
UTF-8 private key accepted by the existing `x/crypto/ssh` parser without an
interactive passphrase.

The returned delivery, material, and repository authorization redact JSON and
formatted output. `Take` transfers ownership once; `Close` clears untransferred
or transferred byte storage. Callers must close the material and must not log,
serialize, or retain field copies. Go cannot guarantee erasure of strings or
runtime copies. Future client/runtime wiring maps the application material to
the existing checkout adapter's credential shape; the application layer does
not depend on the runtime package.
