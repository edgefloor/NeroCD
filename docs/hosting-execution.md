# Public image deployment

Image deployment requires PostgreSQL, the native schema through migration 0045,
the River schema, and the server's live hosting worker. Memory mode returns
`503 hosting_unavailable` before accepting a deployment. River dispatch creates
one typed `image_deploy` task for the resource's bound runner. A River job finishing
does not mean the deployment succeeded; read the hosting operation for its outcome.

The runner needs the `image_deploy` capability and the authoritative identity
endpoint. Existing Compose capability remains `compose-deploy`. Image execution
uses a local Unix Docker endpoint, defaulting to `unix:///var/run/docker.sock`.
Set `--image-docker-endpoint` to another local Unix socket when required. The
same explicit endpoint is used for daemon probing, pulling, inspection and apply.
Each engine instance has an empty, owner-only registry configuration; ambient
registry credentials and Docker contexts are not inherited. Only anonymous
public image pulls are supported in this slice.

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
returns `409`. The host port is a stable reservation for the binding, between
1024 and 65535, bound only on `127.0.0.1`. Container ports are between 1 and 65535.
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
Durable local apply intent and result survive process restart. A successful
completion settles the lease, operation, healthy pointer, receipt and audit record
in one transaction before clearing its owned hold.

**Restore recovery remains blocked in this slice.** A restored binding reports
`restore_ownership_held: true`. A database backup can predate an admission while
an old Docker command still runs. An absent admission row, an empty new journal,
or a point-in-time empty Docker listing cannot prove that command has stopped.
Read-only reconciliation therefore cannot clear restored ownership. Trusted local
journal and process recovery proof is not yet implemented; there is no admin
bypass. Preserve the runner's journal and keep the binding blocked pending that
recovery workflow. This limitation prevents claiming restore recovery or the full
hosting milestone is complete.

The current slice does not provide TLS, public routing, private registry
credentials, variables or zero-downtime replacement.

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
