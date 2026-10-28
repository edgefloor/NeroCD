# Connect an image application to a private database

Connect one public-image application to a healthy PostgreSQL, MySQL, or Redis database
in the same project and destination. First Connect deploys without application
storage or routes. After it settles, you can attach one owned upload volume or
continue reviewed HTTPS routes during later replacements.

## Prepare the resources

Use a project owner account for connection changes, connected deployments and
database maintenance. Current project viewers can read eligible options and
recorded connection progress. The server rechecks current authority for changes.

Create an undeployed application from a public container image. Deploy a
PostgreSQL, MySQL, or Redis database first and wait for its healthy settled deployment.
Place both resources in the same project and destination. The connection uses
the retained private database network and a deterministic database alias.
Cross-server networking is unsupported.

This workflow supports one application per database. Source builds and an already
running unbound application are unsupported. Initial Connect rejects attached
application storage. After the connected application settles, the composed storage
policy below can redeploy it with one owned volume and an explicit interruption. An explicit
route continuity option supports replacement of an already connected application
with existing HTTPS routes on the same destination. The workflow does not create
a public application route or database endpoint.
The application must listen on its configured container port and provide a local
HTTP readiness path.

The runner needs the existing image, database, owned-storage and runtime-environment
capabilities plus explicit application/database and member-maintenance support.
The corresponding opt-ins are `--hosting-application-database` and
`--hosting-database-membership-maintenance`. Both require `--hosting-database`,
`--hosting-database-network-capture`, `--hosting-database-network-upgrade` and
`--hosting-runtime-env`, together with the corresponding persisted capabilities.
The connected profile forbids the `shell` capability. A waiting capture does not
prove that the runner enabled automatic capture. Preserve the runner's credential,
journal and protected state roots across restart. See the
[runner configuration checks](../cmd/nerocd/runner_application_database_config.go).

## Save and deploy a native MySQL database

In the project environment, open **Add database configuration**. Enter its name,
select **MySQL**, and choose a project-granted **Database destination**. The
fixed recipe is `mysql-8.4.13-oraclelinux9.v1`. Saving creates the resource,
owned storage reference, and encrypted `root` and `ncd_app` credential versions.
It does not start MySQL or expose a database port on the host.

Open **Database deployments**, review the saved generation, and request its
deployment. The runner requires `hosting_mysql_v1` alongside the existing database,
private-network, and owned-storage capabilities. Wait for successful engine
readiness before selecting the database for an application connection. The
application database is `app`, with internal port 3306 and an owned volume mounted
at `/var/lib/mysql`.

Later same-version replacements retain that volume. These operations do not
authorize an engine-version upgrade. Keep the runner's protected state and owned storage
across restarts. Use [logical backup and new-database restore](hosting-database-backups.md#mysql-local-backup-and-restore)
for a separate recovery copy.

## Connect through the UI

1. Open the image application in its project environment and find **Database connection**.
2. Select an available PostgreSQL, MySQL, or Redis database in **Database to connect**.
   If no database appears, check the database deployment, placement and application prerequisites.
3. Review **Application container port**, **Application readiness path** and
   **Readiness timeout in seconds**. Use the port and path served by your image.
4. Select the approval checkbox, then choose **Connect database and deploy application**.
5. Use **Refresh database connection** to inspect recorded progress. The controller
   saves the binding, obtains network observation, updates the database network
   and requests application deployment. A submitted request is not runtime success.

The server adds exact credential references under these application environment names:

| Engine | Names |
| --- | --- |
| PostgreSQL | `PGDATABASE`, `PGHOST`, `PGPASSWORD`, `PGPORT`, `PGUSER` |
| MySQL | `MYSQL_DATABASE`, `MYSQL_HOST`, `MYSQL_PASSWORD`, `MYSQL_PORT`, `MYSQL_USER` |
| Redis | `REDIS_HOST`, `REDIS_PASSWORD`, `REDIS_PORT`, `REDIS_USERNAME` |

The connection UI does not display credential values. Protected delivery resolves
the referenced managed-secret versions for authorized execution. Configure your
application to consume these names. MySQL does not automatically map them to
`WORDPRESS_DB_*` or another image-specific convention. Use an application image
that consumes or explicitly maps the supplied names. Do not paste passwords into
public literals.

**Connection workflow completed** records a settled operation. It is not continuous
health monitoring. Verify application behavior separately and use runtime status
and logs within their documented freshness and retention limits.

## Use the API

Authenticate through the existing [API contract](../openapi.yaml). Read fresh
resource, runtime-configuration and database generations before submitting a new
request. All routes below are relative to the control API origin.

| Action | Method and path | Required role |
| --- | --- | --- |
| Eligible options | `GET /api/v1/resources/{application}/database-connections/options` | Project viewer |
| Connection progress | `GET /api/v1/resources/{application}/database-connection` | Project viewer |
| Connect and deploy | `POST /api/v1/resources/{application}/database-connections` | Project owner |
| Redeploy connected application | `POST /api/v1/resources/{application}/database-connection/deployments` | Project owner |
| Redeploy connected database | `POST /api/v1/resources/{database}/database/maintenance` | Project owner |
| Request recovery or observation | `POST /api/v1/resources/{application}/database-connection/recovery` | Project owner |

The initial connection body contains `binding` and `deployment`.
`binding` selects `database_resource_id`, `expected_resource_generation`,
`expected_database_generation`, `expected_runtime_generation` and a `request_id`.
`deployment` supplies `expected_generation`,
`expected_runtime_configuration_generation`, `idempotency_key`, `container_port`
and `readiness`, containing `path` and `timeout_seconds`. Use fresh metadata and
new valid request identifiers for a separately reviewed request. Networking,
credential bytes and proof documents are server-owned inputs.

For example, the initial connection request has this shape. Replace the resource
ID and generation values with the fresh values you read. The hexadecimal request
identifiers below are illustrative; generate new identifiers for a new operation
and retain them for an identical retry. The body contains no credential values.

```json
{
  "binding": {
    "database_resource_id": "res_database",
    "expected_resource_generation": 1,
    "expected_runtime_generation": 0,
    "expected_database_generation": 1,
    "request_id": "0123456789abcdef0123456789abcdef"
  },
  "deployment": {
    "expected_generation": 1,
    "expected_runtime_configuration_generation": 0,
    "idempotency_key": "abcdef0123456789abcdef0123456789",
    "container_port": 8080,
    "readiness": { "path": "/health", "timeout_seconds": 30 }
  }
}
```

The binding `request_id` is exactly 32 lowercase hexadecimal characters. Keep the
request body unchanged when retrying an ambiguous response. Do not send query
parameters or extra JSON fields. The connection request limit is 32 KiB.

Connected application redeployment uses the deployment body alone. Database
maintenance requires `expected_generation`, `expected_membership_revision` and
`idempotency_key`. Use the current database generation and the recorded connection
membership revision. See the [typed client](../web/app/src/api/hosting-database-connections.ts)
and [domain inputs](../internal/domain/hosting_database_connection.go) for the
closed request contracts. Unknown or conflicting fields are rejected.

## Preserve existing HTTPS routes during connected redeployment

`application_database_route_continuity` is an optional field on the connected
application deployment request. Initial connection requests reject this option.
The application must already have one active PostgreSQL or Redis connection on
the same destination. Source builds remain unsupported. An attached application volume
requires the separate interruption review below.

In the UI, review **Existing public routes**, select **Continue all reviewed public
routes when redeploying the connected application.**, then approve and select
**Redeploy connected application**. Changed route choices require a new review.
The route continuation runner flag is disabled by default.

For API requests, read `GET /api/v1/resources/{id}/route-switches` before review. An available
response includes `loaded_generation`, `loaded_proxy_operation_id`, every enabled
resource route, and the current healthy release. These values are explanatory
state. Admission checks the loaded proxy, all route versions, and the exact
selected predecessor again.

The additional request field has this shape:

```json
{
  "application_database_route_continuity": {
    "expected_loaded_generation": 4,
    "expected_loaded_proxy_operation_id": "pop_loaded",
    "routes": [
      {
        "route_id": "route_application",
        "expected_version": 2,
        "expected_selected_release_operation_id": "hop_previous"
      }
    ]
  }
}
```

Every enabled route owned by the application must be selected. The controller
freezes the complete loaded proxy snapshot, including unrelated routes. The new
application uses a different loopback port while the predecessor continues to
serve. A ready candidate is not a published healthy release.

The runner requires explicit `--hosting-application-database-route-continuity`
with the existing connected database options, a configured proxy, and
`--proxy-verified-switch`. The capability is
`hosting_application_database_route_continuity_v1`. The versioned continuation is
`application-database.route-continuity.v1`.

The synchronous runner executes three serial tasks:

1. Prepare creates, configures, starts, and checks the candidate. Journal version
   16 records a yield receipt before transmission. The controller completes only
   this task and lease. The original operation, healthy pointer, membership
   revision, predecessor, and application and database holds remain pending.
2. A separate proxy operation performs the existing strict HTTPS switch. Its
   durable result records applied, refused, rolled back, or uncertain behavior.
3. `finalize_{operation_id}` obtains a fresh lease for the original operation.
   Finalize rechecks the exact yield, stage, candidate, applied proxy proof,
   permissions, grants, secrets, and placement. It reinspects the candidate and
   retires the exact predecessor. It never pulls an image, creates a container,
   delivers secrets, or attaches the candidate again.

Final completion atomically publishes healthy and membership evidence and
releases both holds. `run_id` always identifies the original prepare task.
`application_database_route_continuity.finalize_run_id` identifies the separate
finalize task. The operation stays `applying` or `verifying` after yield.
`application_database_yield` exposes receipt IDs and correlation hashes, without
lease or fence authority.

A lost yield ACK replays the original immutable request before the runner claims
the proxy task. A lost final ACK replays its saved receipt without repeating
retirement. Expired authority, revoked grants, partial retirement, failed
readiness, or a failed or uncertain switch retain holds and original evidence.
Observation recovery grants no new effects. An interrupted retirement intent
without a detached receipt cannot automatically repeat retirement.

Existing requests without this option keep their no-route admission rules.
The [physical gate](../acceptance/runtime-hosting-database-runner/README.md) covers both engines, existing HTTPS routes, retained
database reads/writes, failed readiness, verified cutover rollback and exact
acknowledgment replay. An uncertain or held operation still needs explicit review.

## Handle an unavailable or conflicting request

| Response | Action |
| --- | --- |
| `400` | Correct the rejected settings and review a new request. The UI preserves the draft. |
| `401` or `403` | Refresh project access. Permission loss clears retained UI requests and drafts. |
| `404` | Refresh the selected connection or resource; it may be unavailable. |
| `409` | Refresh and explicitly review changed configuration or membership before approving a separate request. |
| `413` | Reduce the request to the bounded documented fields. |
| `503` or an unconfirmed response | Preserve the exact submitted request for an identical retry; inspect recorded progress before making a new request. |

Temporary access revalidation pauses changes. Unknown connection states also
pause actions. The UI polls known pending states a bounded number of times;
use **Refresh database connection** for a fresh recorded result after polling stops.

## Redeploy and recover a response

After the connection workflow completes, review settings and approve
**Redeploy connected application** or **Redeploy connected database**. Database
maintenance preserves current membership evidence and can interrupt database
access. It does not promise uninterrupted service or perform an engine-version upgrade.

If a response is lost, retain the exact request and identifiers. The UI freezes
that attempt and offers an identical retry. A configuration conflict requires
**Refresh for conflict review**, followed by **Use refreshed configuration for a new request**
and a new approval. Do not turn an ambiguous result into a different request.

Runtime journal replay recovers acknowledgments for the same retained attempt.
It does not grant a new writer or bypass current authority. A held operation stays
held. For a deployment without the connected storage policy, recovery admits
observation only through an exact original
operation, stage and candidate hashes, membership revision and idempotency key.
The recovery body uses `action: "observe"`, `operation_id`, `stage_hash`,
`candidate_hash`, `expected_membership_revision` and `idempotency_key`.
That observation action does not unlock effects. Connected storage can offer the
separate reviewed recovery actions described below. Empty hashes are
allowed only together for the explicitly unknown observation case. Observation
does not reapply effects or relabel an unresolved candidate as successful.

Native database backup/restore, credential rotation, host-disk loss recovery and
arbitrary takeover are outside this workflow. Uploaded-file archive recovery is
a separate capability. Retaining a Docker volume is not a database-native backup.

## Implementation and upstream mapping

The [connection controller](../internal/store/postgres_hosting_database_connection.go),
[owner service](../internal/app/hosting_database_connection_service.go), and
[connection UI](../web/app/src/pages/ResourceDatabaseConnections.tsx) implement
the workflow. Public input selects resources and reviewed settings. The controller
owns private network proof, credential references, and queued operations; the
runner executes under current leases and records durable receipts.

## Redeploy with one connected upload volume

The connected upload-volume protocol is `application-database.storage.v1`.

First connect and deploy the application without storage. Attach one
platform-managed local volume to that resource after its initial deployment
settles. Initial Connect with an attached volume is unsupported. The runner must
explicitly enable `--hosting-application-database-storage` and advertise
`hosting_application_database_storage_v1`, in addition to the connected database,
owned storage and protected runtime environment capabilities. Existing HTTPS
routes also require the route continuity and strict proxy options above.

Read `GET /api/v1/resources/{application}/storage-deployment-options`. An available
connected storage review includes `storage_ref` and
`application_database_storage`, with strategy `replacement_with_interruption`,
the current storage generation/hash and `acknowledge_interruption_required:true`.
A response with `storage_ref:null` and reason `storage_unconfigured` describes
absence; a missing descriptor never grants storage deployment authority.

Include this additional field on the existing connected deployment request:

```json
{
  "application_database_storage": {
    "expected_storage_generation": 1,
    "expected_storage_hash": "sha256:<fresh storage hash>",
    "acknowledge_interruption": true
  }
}
```

The server loads the resource-owned volume ID, name, mount path and creation
provenance. This field cannot supply or replace those values. When the application
has existing routes, also send the complete fresh
`application_database_route_continuity` review. Ordinary storage route continuity
is not the connected storage protocol.

The runner materializes the owned volume under the existing creation protocol,
records a stop intent, and stops the exact predecessor before creating or starting
its replacement. It uses the existing protected secret delivery. A ready candidate
keeps both application and database holds until strict HTTPS verification and
fresh finalization succeed. Requests are interrupted while the predecessor is
stopped. The specialized proxy switch checks the exact stopped-predecessor and
sole-candidate writer proof. Its failure path never restores a route to a stopped
application. The storage-less strict switch still requires a healthy predecessor.

Operation fields `application_database_storage` and
`application_database_storage_state` expose the selected policy and durable writer
correlations. The state includes `phase`, `receipt_id`, `state_hash`,
`previous_state_hash`, `stage_hash`, `candidate_hash`, `recovery_actions`,
`recovery_action` and `recovery_state`. Empty stage/candidate hashes mean no stage
has been recorded. Offered actions are read snapshots. An owner request must
reauthorize the exact current operation, membership, storage and route state.

In the UI, refresh the failed operation and review its offered recovery action,
current route choices and storage interruption acknowledgement. Approve the
selected action and submit it once. A submitted request is not settlement. Wait
for the separate recovery state to succeed, then verify application reads and
writes. Viewers can inspect progress but cannot submit recovery.

Use the existing recovery endpoint only with an offered action. Preserve the
operation ID, current stage/candidate hashes and membership revision, and include
`storage_state_hash`, a new `idempotency_key`, and the same explicit
`application_database_storage` acknowledgment:

- `resume_predecessor` follows a failed readiness attempt whose candidate has been
  proven stopped before any proxy yield. A dedicated fresh recovery lease checks
  the exact volume/network ownership and candidate quiescence. It removes only
  that failed stopped candidate, without deleting its volume, proves its absence,
  and obtains current authorization again before starting the original container.
  This keeps the next application deployment from encountering the failed volume
  user. An ambiguous removal or restart retains holds and cannot replay effects
  from its open resume intent. The original deployment remains failed. A dedicated
  receipt records Docker's fresh private endpoint and advances membership by one; the
  healthy application identity and its reserved public host port stay unchanged.
- `complete_replacement` follows a closed refused proxy switch with the exact
  known prior configuration and restored route metadata. It creates one fresh P2
  operation for the same original deployment and ready candidate. The failed P
  and original continuation stay immutable. Exact P2 HTTPS success permits the
  existing fresh finalizer to retire the stopped predecessor and settle holds.

After successful `resume_predecessor`, refresh membership and route/storage
reviews before requesting a new application deployment. The physical gate proves
a fresh C can stop the recovered A, retire it exactly once, retain the original
volume and candidate-written data, and accept new database records and uploads
through trusted HTTPS. Resuming A does not reverse B's database writes or uploads.

`recovery_state` is independent of the original deployment status. `queued` and
`running` report the dedicated accepted task. `succeeded` requires durable effect
and settlement proof. `uncertain` retains holds and offers no automatic retry.
Unknown proxy state, changed route membership, expired recovery authority, foreign
writers, missing or replaced storage and uncertain finalization fail closed.
Observation remains read-only and cannot release these holds. Recovery retains
both the original deployment owner's current authority and the requesting
owner's current authority. A different owner cannot override revocation of the
original actor. Offers are suppressed after original-owner revocation. Changing the original actor's authority is a separate deliberate administrative
decision. Recovery never restores it automatically. Recovery from a permanently
orphaned original owner is unsupported.

This policy covers one owned local named volume, one public-image application and
one PostgreSQL or Redis database on the same authorized destination. It does not
add host mounts, multiple volumes, source builds, cross-host storage, backups or
archives. An admitted missing volume is never recreated. As in the existing
owned-volume protocol, a trusted daemon administrator who replaces a volume and
copies all ownership metadata cannot be distinguished from the original volume.
Coolify's named-volume metadata and deployment review inform this workflow; the
exclusive writer interruption and fresh recovery receipts are Go runner behavior.

## Native MySQL qualification

At `f2b4e471` on 2026-10-28, the compiled-runner workflow created native MySQL
through `POST /api/v1/databases`, connected an image application through the
private `MYSQL_*` profile, and retained records and uploaded files across
replacement. A direct Docker stop/start of the source database checked restore
isolation and retained source data. Managed HTTPS routing, failed-readiness recovery,
and failed-cutover recovery passed with the prior database and volume identities
preserved. The existing connection and backup browser scenarios also passed.

The engine picker has source tests. This physical fixture created MySQL through
the real API, so it does not establish browser operation of that picker. The
profile is MySQL 8.4.13 on native Linux arm64 with trusted rootful classic-store
Docker, one destination, and fixture-CA HTTPS. Credential rotation, engine
upgrades, cross-host recovery, and automatic image-specific variable mapping
remain open. [Local backup and separate-target recovery](hosting-database-backups.md#mysql-qualification)
have their own assertions within the same run.

<a id="historical-storage-less-qualification"></a>

## Tests and supported environments

The [runtime guide](../acceptance/runtime-hosting-database-runner/README.md#current-connected-storage-qualification)
describes the earlier PostgreSQL and Redis fixture. It covers application and database
replacement, one-writer upload continuity, strict HTTPS cutover, response-loss
replay, and owner recovery. PostgreSQL resume and Redis forward completion also
use the live owner recovery form with viewer denial and keyboard review.

The profile is native Linux arm64, PostgreSQL 17.11, Redis 8.2.10, trusted rootful
classic-store Docker, one destination, and private fixture-CA HTTPS. The storage
fixture runs as root to initialize its volume. It does not qualify arbitrary
nonroot image initialization, production ACME, source builds, credential rotation,
in-flight request draining, control-plane restart, cross-host storage, or disk loss.
Backup and archive recovery have separate contracts. Resuming a predecessor keeps
candidate-written data and cannot establish compatibility with the older app.
