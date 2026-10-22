# Managed proxy control

The backend checkpoint persists authorized route generations and dispatches exact-target
proxy tasks through River. It does not yet execute those tasks in the runner. The runner
must explicitly advertise `proxy_apply`; existing agents do not advertise it and cannot
claim these tasks. Runner journal integration and the combined Linux Traefik/Pebble
acceptance fixture remain required before runtime completion.

## Configure authority

A server administrator configures `/api/v1/servers/{id}/proxy` with an existing
`control_project_id`, ACME email, the closed production/staging CA choice, and a
loopback management port. Configuration also requires owner permission in that project;
the existing system administrator override applies. Placement and settings are immutable.
The project cannot be archived while the proxy remains configured, including while
disabled. Configuration and archive serialize on the same project lifecycle row.

Administrator hostname grants assign exact names or an explicit descendant namespace
to a project. Grants cannot overlap across projects. Wildcard authority does not request
wildcard certificates. Generated names require both an explicitly configured domain and
a matching active wildcard grant. Neither grants nor generated names prove DNS ownership.

Runner filesystem paths, Docker binary/socket selection, credentials and CA trust
overrides are never accepted from project route input or exposed in public responses.

## Select routes

Project viewers may read their resource routes. Maintainers create or update a route
using a hostname, canonical literal path, idempotency key, exact route version and
expected settled healthy deployment operation. The backend derives the destination,
runner, release identity and loopback port. A pending replacement cannot receive traffic
merely because it reuses a previous port. Serving release references protect retained
containers and port slots until a verified replacement generation commits.

Every request creates one immutable whole-proxy snapshot, preserving concurrent projects.
The lock order is destination, proxy, then resources sorted by identity. New generations
may supersede work that has not been admitted. An admitted or uncertain operation keeps
its independent durable proxy hold. Disabling the proxy or revoking a grant queues route
withdrawal; pending withdrawal remains visible while the prior configuration serves.

Project operation reads contain only authorized own-route statuses. Administrator detail
and internal runner plans include complete snapshots. Generic task cancellation, retry
and completion cannot mutate proxy tasks. Proxy cancellation requires server administration
and ownership of the immutable control project.

## Dispatch and settle

River jobs carry only an operation ID. Dispatch freezes a versioned typed plan and its
committed predecessor generation. Claim and runner endpoints require the explicit proxy
capability, exact server/destination/runner target, and full operation/run/lease/attempt/
fence authority. Local engine preparation pins nonsecret daemon and static configuration
hashes. Admission commits ownership before external effects. Completion commits the
receipt, loaded generation, task/lease result and audit event in one transaction.

First reports require live uncanceled authority. Exact previously committed receipts
may replay after expiry; stale first reports cannot settle ownership. Applied routing
with pending ACME remains distinct from trusted HTTPS readiness. Default, self-signed,
expired or untrusted certificates cannot establish HTTPS readiness.

Database restore inserts a permanent restore hold for every configured proxy, including
idle and disabled proxies without operations, in the same transaction that revokes
sessions and expires leases. There is no automatic recovery proof or hold-clearing API.

## Validation boundary

The checkpoint tests use isolated PostgreSQL schemas, a real River consumer, direct API
authentication checks and race-enabled concurrent requests. They cover authorization,
whole-generation preservation, exact lease identity, committed replay, uncertain holds,
grant revocation versus admission, control-project archive serialization, and actual
backup/restore transaction failure. Engine results in these backend tests are typed
fixture receipts, not evidence of Traefik execution. The standalone engine's accepted
runtime evidence remains documented in `hosting-proxy-engine.md`.

DNS-01, wildcard certificates, custom certificates and Caddy are outside this backend.
