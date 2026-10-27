# Public image deployment

Image deployment requires PostgreSQL, the current native and River migrations,
and the server's live hosting worker. Run `nerocd migrate` before server startup. Memory mode returns
`503 hosting_unavailable` before accepting a deployment. River dispatch creates
one typed `image_deploy` task for the resource's bound runner. A River job finishing
does not mean the deployment succeeded; read the hosting operation for its outcome.

The runner needs the `image_deploy` capability and the authoritative identity
endpoint. Existing Compose capability remains `compose-deploy`. Image execution
uses a local Unix Docker endpoint, defaulting to `unix:///var/run/docker.sock`.
Set `--image-docker-endpoint` to another local Unix socket when required. The
same explicit endpoint is used for daemon probing, pulling, inspection and apply.
Each engine instance has an empty, owner-only registry configuration; ambient
registry credentials and Docker contexts are not inherited. Only anonymous public image pulls are supported.

Container inspection checks the default bridge network mode, loopback port mapping,
and host privileges before accepting ownership or readiness. Privileged mode,
added capabilities, host binds, shared host namespaces, devices, volume inheritance,
security overrides, and extra published ports fail closed. Stateless plans reject
mounts. Managed-storage plans allow only their exact local named volume and mount
path. Checks cover predecessors, retained releases, newly created candidates,
readiness, observation, and targeted cleanup. Readiness rechecks container identity
and host policy after a successful HTTP response. The local Docker daemon remains
trusted; these checks do not provide an OS sandbox for hostile workloads.

## Request and inspect a deployment

`POST /api/v1/resources/{id}/deployments` requires project maintainer access:

```json
{
  "expected_generation": 1,
  "idempotency_key": "release-20261022-1",
  "container_port": 8080,
  "host_port": 18080,
  "readiness": {"path": "/ready", "timeout_seconds": 30}
}
```

The response is `202` with a `HostingOperation`. Repeating the same request key
returns the original operation and snapshot. Changing the request under that key
returns `409`. An explicit host port must be available, between 1024 and 65535,
and binds only on `127.0.0.1`. Omit `host_port` or use zero to request allocation
from the server operator's range. The defaults are 20000–60999; operators can set
`NEROCD_HOSTING_AUTO_PORT_MIN` and `NEROCD_HOSTING_AUTO_PORT_MAX` before startup.
Allocation preserves the original request's idempotency key and resolved port.
Before automatic allocation, destination admission reclaims slots whose claims
have conclusively settled without live effects. Historical claims, snapshots and
receipts remain available for replay. Legacy binding reservations, retained
releases, active claims, unresolved effects, restore holds and protected route
references keep their slots.
Container ports are between 1 and 65535.
Readiness accepts an origin-relative path and a bounded timeout of 1–120 seconds.
The runner uses ordinary HTTP GET, accepts 200–399 and does not follow redirects.

Viewers can read `GET /api/v1/hosting-operations/{id}` and list resource operations
with `GET /api/v1/resources/{id}/deployments?limit=20&offset=0`. Lists use
`{items,count,limit,offset,total}`, newest first by creation time and ID. The
operation includes its immutable snapshot, resolved release and apply plan when
available, receipts, result, state and independent hold flags. Public reads do not
include lease or fence authority.

Maintainers can send `{}` to `POST /api/v1/hosting-operations/{id}/cancel`.
Owners can request read-only target reconciliation with `{}` at
`POST /api/v1/hosting-operations/{id}/reconcile`. Reconciliation receives a
separate exact-target observation lease. That lease cannot prepare, apply or
publish a deployment success.

## Failure and recovery boundaries

Preparation pulls and inspects the image without changing containers. It records
the repository digest, local image ID, platform and Docker daemon ID. Apply uses
this committed release even if its original tag moves. A failed pull leaves the
previous workload running.

Applying on a fixed loopback port permits an interruption. A known HTTP readiness
failure happens after apply; the runner preserves the previous container and
attempts bounded restoration while it still has live authority. Unknown command
effects, lease loss and failed restoration retain the independent mutation hold.
Cancellation after admission requests cancellation but does not erase uncertainty.
Durable local apply intent and result survive process restart. Every first
completion requires a live, uncanceled lease. After authority ends, the server
only replays an exact previously committed receipt; it cannot accept a fresh
result or change terminal lease/task state. Rejected results and mutation intent
remain in the local journal, and startup stops before heartbeat or new claims.
Read-only observation cannot publish an uncommitted local result. A separate
explicitly authorized recovery contract is still needed for that case.

A successful completion under live authority settles the lease, operation,
healthy pointer, receipt and audit record
in one transaction before clearing its owned hold.

## Retained releases and targeted cleanup

Auto allocation, different-port replacements, and cleanup require the runner's
explicit `image_release_ports_v1` capability. Their frozen compiler is
`image-hosting.release-ports.v1`; the server checks the capability again at
dispatch, claim, prepare, admission, and first result. Legacy agents can receive
only legacy plans without retained-release fields. A retained-release intent
advances the local journal to version 3, which older binaries reject.

A replacement on a different port keeps the previous healthy container running.
A failed readiness check removes only the failed candidate and verifies the old
release is still healthy. Changing the healthy pointer does not itself switch any
proxy route: a route generation must explicitly select and verify the new release.
The previous release and port remain recorded until targeted cleanup succeeds.

Viewers can list resource-owned releases with
`GET /api/v1/resources/{id}/retained-releases`. Maintainers request cleanup with
`POST /api/v1/resources/{id}/retained-releases/{operation_id}/cleanup` and
`{"expected_generation":1,"idempotency_key":"cleanup-old-release"}`. The server
selects the exact container, image, daemon and port from persisted evidence.
Current healthy releases and releases referenced by desired, queued, loaded,
admitted or unresolved proxy generations cannot be cleaned up. Cleanup stops and
removes only that exact owned container. It never prunes a daemon or accepts a
caller-supplied container identity. The ledger remains protected until the exact
successful receipt commits; lost responses, stale authority, restore holds and
uncertain effects keep ownership held. The original legacy binding port remains
reserved for compatibility even after its old container is removed.
If cleanup is canceled before admission, its exact canceled server status, absent
mutation and restore holds, and absence of conflicting result evidence allow the
runner to acknowledge its durable cleanup intent on restart. Admitted or uncertain
cleanup still blocks startup until a verified completion settles ownership.

Lease expiry before cleanup admission may requeue the same immutable target with
new fenced authority. After admission, expiry marks the operation
`reconciliation_required` and its task failed while retaining the mutation hold,
cleanup target and release ownership. This applies to registry-image and local-build
cleanup. The server cannot dispatch that destructive cleanup again or admit a
conflicting deployment merely because the lease expired. A previously committed
exact completion receipt remains replayable; expiry does not recreate cleanup work.


Restore recovery is blocked. A restored binding reports
`restore_ownership_held: true`. A database backup can predate an admission while
an old Docker command still runs. An absent admission row, an empty new journal,
or a point-in-time empty Docker listing cannot prove that command has stopped.
Read-only reconciliation therefore cannot clear restored ownership. Trusted local
journal and process recovery proof is not yet implemented; there is no admin
bypass. Preserve the runner's journal and keep the binding blocked pending that
recovery workflow.

Configure public routing and TLS through the [managed proxy](hosting-proxy-ui.md).
Runtime variables are saved separately in the application's **Runtime configuration**
section and require a new deployment. Private registry credentials and
zero-downtime same-port replacement are unsupported.

## Disposable Docker verification

Set `NEROCD_TEST_DATABASE_URL` to a disposable PostgreSQL test database, then run
`make image-hosting-gate` from the repository. The gate runs under the Go race
detector. It builds two
small HTTP image versions, publishes to a run-owned loopback registry, moves the
tag after preparation and verifies the prepared version over ordinary HTTP. It
also checks the next version, failed pull preservation and post-apply HTTP failure
restoration. The combined PostgreSQL, River, API and agent fixture loses a
preparation response, restarts the control plane while HTTP remains live, and
loses a lease after Docker starts the container. Cleanup only removes captured
fixture resources.
