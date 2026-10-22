# Managed proxy control

The backend persists authorized route generations and dispatches exact-target proxy
tasks through River. An explicitly configured Linux agent executes these tasks through
the managed Traefik file-provider engine. The runner must have persisted `proxy_apply`
authority; existing agents without that capability cannot claim these tasks. Application
traffic and automatic ACME renewal continue when the controlplane and agent are absent.

## Configure authority

### Enroll and start the Linux agent

Create a runner enrollment as an authenticated human administrator. The enrollment
must persist `compose-deploy` for server placement and `proxy_apply` for proxy tasks.
Add `image_deploy` and `image_release_ports_v1` when this agent also deploys image
applications. A local `--capabilities` value cannot alter an existing enrollment.
The agent reads its authenticated current persisted capabilities before opening or
replaying the journal and refuses a missing or incompatible proxy capability.

The enrollment request is `POST /api/v1/runner-enrollments` with a human session
bearer token and this JSON body for an agent that also deploys image applications:

```json
{
  "runner_id": "runner_web_01",
  "runner_name": "Linux web host",
  "capabilities": ["compose-deploy", "image_deploy", "image_release_ports_v1", "proxy_apply"]
}
```

Use the returned one-time `token` as the enrollment-file contents. Do not use the
human session token or a bootstrap API token as an agent credential. Enrollment
consumption binds a generated dedicated credential to the persisted runner identity.

Save the one-time enrollment token to an owner-only mode-0600 file, and create the
owner-only parent directories for the durable credential, journal and proxy root.
For example, after replacing the server and operator paths:

```sh
nerocd runner --server https://nerocd.example.org \
  --enrollment-file /var/lib/nerocd/enrollment-token \
  --credential-file /var/lib/nerocd/runner-credential \
  --work-dir /var/lib/nerocd/work \
  --journal-dir /var/lib/nerocd/journal \
  --proxy-state-root /var/lib/nerocd/proxies \
  --proxy-docker-binary /usr/bin/docker \
  --proxy-docker-socket /var/run/docker.sock \
  --proxy-protocol managed-traefik.file.v1
```

Subsequent starts use the same command without `--enrollment-file`. Keep the
credential, journal and proxy root across agent restarts. The state root must be
mode 0700 and its Docker endpoint must be an actual local Unix socket. Proxy
configuration requires Linux and execution enabled. There is no production flag
for a custom ACME endpoint, trust root, Docker argument, or project-selected path.

An ordinary agent without proxy configuration keeps its existing capabilities and
cannot execute proxy tasks. A configured agent with only placement and proxy
capabilities cannot claim image or shell tasks. An already enrolled `proxy_apply`
agent refuses to start without the explicit local proxy configuration.

Ordinary runner IDs keep the existing nonempty, trim-stable identity contract;
punctuation and Unicode are not rewritten. Capability names remain opaque, including
custom names and duplicates. Only exact lowercase `proxy_apply` presence selects the
proxy guard, and duplicates add no authority. An older identity response without
capabilities remains compatible with an ordinary agent. Identity responses must contain
one complete JSON document within the existing 1 MiB bound; malformed typed fields
or an unauthenticated response fail before journal startup or heartbeat.

Engine placement and route identities have an 80-byte limit. Normal server-generated
identities fit this limit. Longer imported identities fail preparation before engine
effects, even though the public backend accepts identities up to 128 bytes.

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

The agent appends a typed mode-0600 journal preparation before sending it, an intent
before requesting admission, and a result before sending completion. Journal v4
preserves older image cleanup records. Older binaries that do not support v4 must not
open this journal. Startup replays proxy evidence before heartbeat and claim. It never
retries an admitted engine effect or clears uncertain or restored ownership.

If a committed completion response is lost and the lease watcher cancels execution,
a separate bounded status probe must prove the exact receipt, plan, result and attempt
before replaying that receipt. A missing or changed receipt leaves the intent and result
journaled. Local committed engine state and the acknowledged server result must agree
before journal removal. Local settings or Docker identity changes fail this comparison.

API readiness is the last committed generation's observation. It is not a continuous
DNS or TLS monitor. Traefik can renew a certificate autonomously; a later applied
generation refreshes the API fingerprint and expiry. Callers should check the reported
expiry when presenting an old observation. ACME failure on a new hostname can leave
that route HTTP-ready and TLS-pending while existing trusted routes continue serving.

Database restore inserts a permanent restore hold for every configured proxy, including
idle and disabled proxies without operations, in the same transaction that revokes
sessions and expires leases. There is no automatic recovery proof or hold-clearing API.

## Validation boundary

The backend tests use isolated PostgreSQL schemas, a real River consumer, direct API
authentication checks and race-enabled concurrent requests. They cover authorization,
whole-generation preservation, exact lease identity, committed replay, uncertain holds,
grant revocation versus admission, control-project archive serialization, and actual
backup/restore transaction failure. The standalone engine's runtime and backup/restore
evidence is documented in `hosting-proxy-engine.md`.

Run the combined fixture from the repository root:

```sh
bash acceptance/runtime-proxy-control/gate.sh
```

It compiles the Go agent test with the race detector and the explicit
`nerocd_proxy_fixture` build tag, then starts a uniquely labeled disposable Linux
Docker daemon with no host ports published. PostgreSQL, River, real API human sessions,
runner enrollment, two scratch HTTP backend images, the pinned Traefik image and a
local Pebble CA run inside that namespace. Fixture DNS resolves only its test domains.
The CA/root injection constructor is absent from ordinary production builds.

The fixture runs the Go agent runtime rather than a mock executor. It proves concurrent
whole-generation routing across projects, Host/path segment selection, trusted HTTP-01,
response-loss receipt recovery, actual rejected-file rollback, initial and disabled
empty generations, ACME rejection continuity, controlplane outage, exact persisted
certificate reuse with the CA unavailable, and renewal without the controlplane.
Separate exact targets retain admission holds after lease expiry and human cancellation;
startup performs no engine effect. A no-proxy image agent and configured proxy-only
agents exercise queued work filtering and preserve unrelated image execution. Focused
CLI tests independently verify invalid proxy flags fail before registration or credential use.

The fixture never uses production DNS, a public ACME CA, user workloads, or a global
Docker prune. Its trap verifies the exact owned outer container label before removal.

DNS-01, wildcard certificates, custom certificates and Caddy are outside this backend.
