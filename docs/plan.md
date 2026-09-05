# Product plan

NeroCD has a PostgreSQL control plane with a web UI, local and OIDC login,
project authorization, audit events, runner leases, sequential shell/Compose
execution, and production deployment tooling. This is the remaining work.

## Identity and authorization

- Add a complete service-account lifecycle, password policy, user disablement,
  richer roles, and stronger failure-path coverage.
- Keep OIDC explicit until a separate policy defines any claim-based mapping.
- Do not advertise SAML, LDAP, SCIM, group mapping, or automatic signup before
  their lifecycle, authorization, audit, and recovery semantics exist.

## Runner operations

- Expand lifecycle reporting, enrollment rotation, failure recovery, and
  operator controls around the existing fenced-lease protocol.
- Make inventory and access-key metadata useful to supported execution paths.
- Define durable artifact storage and retention before claiming delivery.

The runner contract must continue to make lease ownership, cancellation,
completion, and replay outcomes observable. A convenience feature must not
weaken the fence that prevents an older runner attempt from changing newer
work.

## Workflow capability

- Finish step controls, retries, approvals, and diagnostics.
- Add parallelism only with an explicit dependency model, cancellation, locks,
  and audit semantics. Current execution is sequential.
- Decide whether scheduling belongs in the control plane after its missed-run,
  timezone, ownership, and outage behavior is designed.

Workflow controls must have a clear operator surface in both API and WebUI.
Each state change needs an audit record and a recovery path that works when a
runner or browser disconnects.

## Secrets and resilience

- Keep `runner_file` as the current production-safe path and development-only
  environment injection for local work.
- Design encryption, rotation, write-only APIs, runner delivery, read audit,
  revocation, and redaction before database or Vault providers.
- Broaden PostgreSQL, browser, migration, restore, and runner-failure coverage
  while preserving backup and release verification safeguards.

Operational work should prefer small, reversible steps: test backups before a
restore is needed, exercise migrations on representative data, and document the
observed recovery process alongside any new capability.

## Completion rule

A feature is complete only when its schema, API contract, authorization, audit,
operator path, and tests agree. A route or visible control is not a guarantee.

Release work follows the same rule. A local artifact can support review, but
only timestamped CI evidence, immutable publication, and trust verification
establish a release record.

Documented limits remain part of that release record until implementation changes.
