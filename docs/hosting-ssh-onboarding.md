# Prepared-host SSH onboarding

SSH onboarding is disabled unless the operator sets `NEROCD_SERVER_BOOTSTRAP_CONFIG_FILE`.
The controller requires PostgreSQL, a managed-secret keyring, and an HTTPS public origin matching
the policy's `controller_origin`. The selected SSH target must already run Debian 12 on native
arm64, systemd, Docker API 1.54, and Compose 5.1.1, with at least 1 GiB available disk space.
The controller does not download binaries or install system packages.

The policy file contains an HTTPS origin and at most sixteen qualified local artifacts:

```json
{
  "controller_origin": "https://controller.example",
  "artifacts": [{
    "artifact_id": "runner-arm64-v1",
    "path": "/opt/nerocd/artifacts/nerocd",
    "size": 123456,
    "sha256": "<exact lowercase SHA-256 digest>",
    "version": "v1.2.3",
    "architecture": "arm64",
    "profile": "prepared_debian12_v1"
  }]
}
```

The operator supplies the actual qualified binary and its measured metadata. Startup checks
size and digest. The installer independently opens the artifact without following symlinks,
validates the bytes, and transfers from that same descriptor. Artifact paths and controller
configuration never appear in the public options response. The API accepts only opaque artifact
IDs. It never accepts private keys, enrollment tokens, local paths, or controller URLs.

An administrator who owns the project creates an operation using a request UUID, a root SSH
endpoint with literal IP and pinned SHA-256 host fingerprint, and an existing managed-secret
ID and version. The durable secret grant binds that actor, target, secret version, and original
one-hour deadline. Retrying commands or observing enrollment does not extend the delegation.

The public workflow is:

1. `GET /api/v1/server-bootstrap-options?project_id=...` explains feature availability.
2. `POST /api/v1/server-bootstraps` reserves stable operation, installation, runner, enrollment,
   server, and destination IDs. Exact request retries return those same identities.
3. `POST /api/v1/server-bootstraps/{id}/probe` queues a fixed read-only prerequisite observation.
4. `POST /api/v1/server-bootstraps/{id}/review` names only a committed probe receipt ID. The
   database assigns its observation and thirty-second expiry. Review derives the exact canonical
   installer manifest from trusted persisted metadata.
5. `POST /api/v1/server-bootstraps/{id}/install` requires the reviewed manifest hash,
   `acknowledge_install_and_start: true`, and an explicit `grant_project` boolean. An expired
   pre-effect review can be refreshed with another probe and review.
6. The runner consumes enrollment independently. Registration checks the consumed enrollment's
   exact runner and credential binding. `POST /{id}/register` resumes registration explicitly.
7. A reviewed `grant_project: true` permits the processor to grant the resulting destination to
   that project after registration. False retains the registered server without project access.
   `POST /{id}/grant` supplies a later explicit grant decision.
8. The existing server readiness endpoint reports readiness independently of installation and
   registration. An installed service alone does not prove readiness.

All command requests use a UUID `command_id` and `expected_version`. List and detail responses
are closed metadata projections with bounded progress, retained holds, and available actions.
Lists require a project and return `items`, `limit`, `offset`, and `has_more`. All responses use
`Cache-Control: no-store`.

The existing River hosting queue carries only the opaque bootstrap operation ID. Business
commands survive finite job retries and job-history cleanup. The existing hosting recovery loop
finds due bootstrap work in bounded batches. Remote calls run outside transactions, renew their
thirty-second lease every ten seconds, and cancel and join on authority loss.

Install intent and enrollment are committed before remote submission. A failed connection,
lease expiry, cancellation, or lost response retains remote ownership. It never permits a
conflicting installation. `POST /{id}/inspect` invokes the fixed owned-state helper. Private
reconciliation requires the original durable install intent and an exact matching installation,
manifest hash, remote operation ID, installed artifact, active service, and no pending service
job. It closes the original intent and advances to enrollment waiting while retaining the hold.
An administrator cannot submit these facts through the API. A refused or noninstalled automatic
inspection retains uncertainty and the hold. The operator can request a new inspection with a
new command UUID, up to the existing eight-inspection limit. Automatic recovery does not
repeat a completed inspection. Restored operations remain held and
never resume remote activity automatically.

The backend tests cover transactional command replay, typed receipt binding, response-loss
reconciliation, retained holds, queue recovery without a build policy, metadata isolation, and
cancellation with join. The disposable runtime fixture remains the separate gate for actual SSH,
controller TLS, enrollment, and the complete API and UI journey.
