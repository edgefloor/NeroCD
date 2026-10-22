# Secret backends

NeroCD stores project-managed secret values as encrypted, immutable versions.
The managed-secret API supports metadata, creation, rotation, owner reveal, and
irreversible revocation. Runner execution still supports only local
`runner_file` and development-only `env` providers. Managed secrets do not yet
provide runner resolution or transport for the `database` provider.

## Binding Contract

`RunSpec.secrets` entries are references, never inline values:

- `name`: display/audit label.
- `provider`: `runner_file`, `env`, `database`, or `vault`.
- `reference`: provider-specific lookup key.
- `target`: either `env:NAME` for a process or `file:NAME` for a Compose
  top-level secret. Compose services must reference `NAME` in their checked-in
  `secrets:` configuration; the runner supplies only its generated `file:`
  descriptor at deployment time.

Secret values never appear in normal metadata responses, run specs, audit
metadata, log messages, artifact records, or runner primitive plans. Only the
explicit owner reveal operation returns a plaintext value.

## Providers

- `runner_file`: resolved from the runner's owner-only secret root. For
  Compose `file:NAME` targets, the runner authorizes the lease first and
  writes a generated mode-0600 descriptor pointing directly at the validated,
  operator-managed source file. It removes this override metadata on every
  exit path but never copies or deletes the persistent source. The descriptor's
  real path is retained for execution but replaced with a
  stable `file:NAME`-derived placeholder before provenance hashing, so retry,
  recovery, and rollback are deterministic without recording a secret path.
  The private file must be reachable by the same Compose daemon that consumes
  the descriptor. A containerized runner using a host Docker socket therefore
  needs an owner-only host bind directory mounted at the identical absolute
  path inside the runner; named or container-private volumes are unsupported.
  Production should prefer a host-process runner on a dedicated runner VM.
  After Compose merges the generated override, NeroCD accepts only the exact
  authorized top-level `file:NAME` bindings: every descriptor is exactly its
  validated source file and every service reference names one of those
  bindings. Repository-defined `external`, additional, or alternate-file
  descriptors are rejected.
- `env`: resolved by the runner from its own host environment and allowed only
  with the `development` classification. It is not a production Compose
  secret transport.
- `database`: runner resolution remains planned. Project-managed encrypted
  storage and owner reveal are available through the API below. A managed
  secret ID cannot yet be used as a runner binding.
- `vault`: planned external-provider lookup. Required before use: provider
  config, runner-side auth, per-read audit, timeout/retry policy, and masking.

## Project-managed secrets

Migration `0041_project_managed_secrets.sql` adds project-owned metadata and
immutable encrypted version rows. Each project retains a unique secret name,
even after revocation. Metadata includes the current version, active or revoked
status, creator, and timestamps. Version metadata excludes encryption details.

Values contain 1 to 65536 UTF-8 bytes. Whitespace is preserved exactly. Each
version uses AES-256-GCM authenticated encryption from `internal/secretcrypto`,
bound to its project, secret ID, and version. The database stores ciphertext,
its envelope format version, and its key ID. Normal responses and audits contain
no value, value hash, value length, key ID, or ciphertext.

The API requires persisted project membership or a project-scoped service
account role. The existing system administrator override also applies. Every
operation requires an existing, active project.

| Method and path | Required role | Result |
| --- | --- | --- |
| `GET /api/v1/managed-secrets?project_id=...` | viewer | Paginated project metadata |
| `POST /api/v1/managed-secrets` | maintainer | Create version 1 from `project_id`, `name`, and `value` |
| `GET /api/v1/managed-secrets/{id}` | viewer | Secret metadata |
| `GET /api/v1/managed-secrets/{id}/versions` | viewer | Paginated immutable version metadata |
| `POST /api/v1/managed-secrets/{id}/versions` | maintainer | Append `value` at `expected_version` and return updated secret metadata |
| `POST /api/v1/managed-secrets/{id}/revoke` | owner | Revoke at `expected_version` |
| `POST /api/v1/managed-secrets/{id}/reveal` | owner | Return `{secret_id, version, value}` for explicit `version` |

List operations accept the standard `limit` and `offset` parameters. Creation
and rotation return HTTP 201. Other successful operations return HTTP 200.
Stale writes and operations on revoked values return HTTP 409. Metadata and
version history remain readable after revocation. Every managed-secret response,
including errors, has `Cache-Control: no-store`.

Creation, rotation, and revocation commit their audit with the state change.
Each successful reveal requires a new `managed_secret.revealed` audit. Reveal
decrypts internally, then locks the secret row, rechecks active state and the
selected version, and commits the audit before returning plaintext. Audit or
commit failure returns no plaintext. Reveal and revoke use the same row lock.
Historical versions can be revealed only while the secret remains active.

## Encryption configuration

`NEROCD_SECRET_KEYRING_FILE` optionally names an absolute path to a keyring JSON
file. An absent setting permits startup. Create, rotate, and reveal then return
HTTP 503. Metadata and revocation remain available. An invalid configured file
fails startup with a safe error that excludes its path and contents.

The file must be a regular file owned by the server's effective user, with mode
0400 or 0600. Its final path component cannot be a symlink. Ancestor directories
must be trusted. Unsupported platforms fail closed. The file is at most 8192
bytes and has exactly these fields:

```json
{
  "active_key_id": "key-2026-10",
  "keys": {
    "key-2026-10": "<base64-encoded cryptographically random 32-byte key>"
  }
}
```

A keyring contains at most 32 unique key IDs. The server loads an immutable copy
at startup. New versions use `active_key_id`. Historical versions require their
original keys. To change the active key, retain required historical keys in the
file and restart the server. Replacing the active key does not re-encrypt stored
versions. Losing a historical key prevents its values from being revealed.

Operators supply cryptographically random keys and rotate each key before
2^32 total encryptions across all server processes. Key files and database
backups require separate protection. Go does not guarantee erasure of plaintext
or key material from process memory.

## Operational Rules

- A runner may resolve a secret only for the currently leased run.
- Managed-secret reveal audit must commit before returning a value. Runner
  secret access retains its separate lease-bound authorization audit.
- Logs must record that a binding was prepared without printing provider
  reference, target name, or value.
- Required secret failures fail the current step and complete the lease as
  failed.
- Repository credentials are confined to provenance transport and must retain
  an `env:` target; they cannot be repurposed as application Compose secrets.
- Provenance migrations retain historical bare `sha256:` image values for
  reading. A legacy resolved revision is replay-compatible only for a
  single-service image whose new untagged `repository@sha256:` suffix exactly
  matches; all new receipts persist complete immutable references.
