# Connect an image application to PostgreSQL or Redis

The bounded workflow is physically qualified at
`9a69228f9a2a8324e25b1c366ee4eba913b8dba2`, tree
`3ccede1d9e44531f70993a40840274ced78b5969`, for the pinned native Linux arm64
private fixture with a verified test CA. The live browser scenarios passed in
28.2 seconds for PostgreSQL and 26.9 seconds for Redis. Owner connection,
application redeployment, database maintenance and same-attempt lost-ACK
recovery passed through the real API, River and compiled Go runner.

All four runtime groups passed without skips, connected workflow 74.64 seconds,
capture 23.19 seconds, normal database recovery 23.19 seconds and empty-network
upgrade 24.65 seconds. The complete gate took 174.487 seconds. Cleanup and outer
inventories were reconciled. Exact source, binaries, UI artifact and cleanup are
recorded in `/tmp/nerocd-application-database-final-c-integrated-physical-handoff.json`;
the runtime output is in `/tmp/nerocd-database-runner-20261024.wy_azrsp/runtime-test`.

Earlier preflight-only failure and the failed Redis third replacement after
`network_configured`, before `attached`, remain historical failures at their
original revisions. The failed 273.666-second gate is not relabeled. This later
pass qualifies only the adapter limits below, not full R42/A17 or hosting parity.

## Prepare the resources

Use a project owner account for connection changes, connected deployments and
database maintenance. Current project viewers can read eligible options and
recorded connection progress. The server rechecks current authority for changes.

Create an undeployed application from a public container image. Deploy a
PostgreSQL or Redis database first and wait for its healthy settled deployment.
Place both resources in the same project and destination. The connection uses
the retained private database network and a deterministic database alias.
Cross-server networking is unsupported.

This workflow supports one application per database. Source builds, application
storage, route continuity and an already running unbound application are
unsupported. It does not create a public application route or database endpoint.
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

## Connect through the UI

1. Open the image application in its project environment and find **Database connection**.
2. Select an available PostgreSQL or Redis database in **Database to connect**.
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
| Redis | `REDIS_HOST`, `REDIS_PASSWORD`, `REDIS_PORT`, `REDIS_USERNAME` |

The connection UI does not display credential values. Protected delivery resolves
the referenced managed-secret versions for authorized execution. Configure your
application to consume these names. Do not paste passwords into public literals.

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
| Request recovery observation | `POST /api/v1/resources/{application}/database-connection/recovery` | Project owner |

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
held. Recovery currently admits observation only through an exact original
operation, stage and candidate hashes, membership revision and idempotency key.
The recovery body uses `action: "observe"`, `operation_id`, `stage_hash`,
`candidate_hash`, `expected_membership_revision` and `idempotency_key`.
The UI does not offer an unlock or effectful recovery action. Empty hashes are
allowed only together for the explicitly unknown observation case. Observation
does not reapply effects or relabel an unresolved candidate as successful.

Native database backup/restore, credential rotation, host-disk loss recovery and
arbitrary takeover are outside this workflow. Uploaded-file archive recovery is
a separate capability. Retaining a Docker volume is not a database-native backup.

## Implementation and upstream mapping

The comparison target is Coolify
`b12d1386f3d458618b4c579babcd1e339c6c40b3`. These are behavior mappings, not
unchanged-client compatibility claims.

| Pinned Coolify implementation | NeroCD replacement |
| --- | --- |
| [`StartPostgresql::handle` and `generate_environment_variables`](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Actions/Database/StartPostgresql.php#L33), destination network and persistent storage | [Connection controller](../internal/store/postgres_hosting_database_connection.go), retained network-origin proof and native Go database engine |
| [`StartRedis::handle` and password configuration](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Actions/Database/StartRedis.php#L33) | Exact managed-secret references and protected runner credential delivery |
| [`ApplicationDeploymentJob`](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Jobs/ApplicationDeploymentJob.php#L1726), runtime environment and destination networking | [Owner service](../internal/app/hosting_database_connection_service.go), transactional River work, fenced Go-agent execution and durable journal |
| [`StandalonePostgresql`](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Models/StandalonePostgresql.php#L322) and [`StandaloneRedis`](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Models/StandaloneRedis.php#L308), internal connection details | Private deterministic alias, exact credential references and [connection UI](../web/app/src/pages/ResourceDatabaseConnections.tsx) without password disclosure |

NeroCD uses a native enrolled Go agent, current effect authorization, retained
membership evidence and same-attempt journal replay. Coolify's PHP/SSH execution
model is not copied as an API or process contract. The parity audit assesses each
full mandatory outcome separately; source presence alone earns no verified credit.
