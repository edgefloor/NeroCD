# Secret Backend Design

NeroCD currently executes only local `runner_file` and development-only `env`
providers. `database` and `vault` remain planned until encrypted value storage
and secret-read audit are implemented.

## Binding Contract

`RunSpec.secrets` entries are references, never inline values:

- `name`: display/audit label.
- `provider`: `runner_file`, `env`, `database`, or `vault`.
- `reference`: provider-specific lookup key.
- `target`: either `env:NAME` for a process or `file:NAME` for a Compose
  top-level secret. Compose services must reference `NAME` in their checked-in
  `secrets:` configuration; the runner supplies only its generated `file:`
  descriptor at deployment time.

Secret values must never appear in API responses, run specs, audit metadata, log
messages, artifact records, or runner primitive plans.

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
- `database`: planned encrypted-at-rest values scoped to a project or variable
  group. Required before use: envelope encryption, key rotation story, secret
  read audit, masked display metadata, and RBAC checks.
- `vault`: planned external-provider lookup. Required before use: provider
  config, runner-side auth, per-read audit, timeout/retry policy, and masking.

## Required Database Provider Shape

Before encrypted database values are accepted, add:

- `secret_values(id, project_id, name, provider, ciphertext, key_id, version,
  created_by, created_at, rotated_at, revoked_at)`.
- `secret_access_events(id, secret_id, run_id, runner_id, actor_id, action,
  created_at, request_id)`.
- Write-only create/rotate APIs. No API may return plaintext.
- Runner resolution through a short-lived lease-bound endpoint or encrypted
  runner payload, never through stored run JSON.

## Operational Rules

- A runner may resolve a secret only for the currently leased run.
- Secret-read audit must be best-effort before returning a value and mandatory
  for successful reads.
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
