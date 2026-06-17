# Secret Backend Design

NeroCD currently executes only the local `env` secret provider. `database` and
`vault` bindings are accepted as references in templates and runs, but the
runner does not resolve their values. This is intentional until encrypted value
storage and secret-read audit are implemented.

## Binding Contract

`RunSpec.secrets` entries are references, never inline values:

- `name`: display/audit label.
- `provider`: `env`, `database`, or `vault`.
- `reference`: provider-specific lookup key.
- `target`: process injection target, currently only `env:NAME`.

Secret values must never appear in API responses, run specs, audit metadata, log
messages, artifact records, or runner primitive plans.

## Providers

- `env`: resolved by the runner from its own host environment. This is the only
  executable provider today.
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
