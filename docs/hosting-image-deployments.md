# Deploy an image application

Use **Projects → Project hosting** to select a shared environment and saved image
application. Save its desired destination before requesting deployment. The
destination identifies one enrolled runner and an immutable runtime namespace.
The runner must be active, advertise `image_deploy`, and have no `shell`
capability. Server admission also requires `compose-deploy`; enroll the intended
runner with both capabilities. Use the file-based onboarding instructions in
[Hosting destinations](hosting-destinations.md).

The runner host needs NeroCD, Docker Engine, and Docker Compose. Preserve its
credential, work, and journal files, and enable execution with `--execute`.
Server registration alone does not verify that these prerequisites are ready.
Image execution uses the local Docker Unix socket, defaulting to
`unix:///var/run/docker.sock`. If the host uses a different local socket, pass
its explicit path with `--image-docker-endpoint unix:///absolute/path/to/docker.sock`.
For subsequent runner starts, omit `--enrollment-file` after its one-time file
has been consumed. Continue using the same `--credential-file`, work directory,
and journal directory. Keep the permanent credential out of browser inputs and
command arguments.

## Request deployment

1. Review the saved image reference, configuration generation, and binding shown
   in **Image deployments**. A deployment captures this configuration and target.
2. Enter the container port and runner-local host port. Container ports range
   from 1 through 65535; host ports range from 1024 through 65535.
3. Enter an origin-relative HTTP readiness path, such as `/health`, and a timeout
   from 1 through 120 seconds. The check does not require revision-specific
   response headers.
4. Acknowledge that replacing a workload on the fixed host port interrupts
   service while the old container stops and the new container is verified.
5. Select **Request image deployment**. A project maintainer or owner can submit
   the request. The server also permits a system administrator.

The host port listens on `127.0.0.1` at the runner. It is not a public application
URL. This workflow does not provision DNS, HTTPS, a reverse proxy, or SSH access.

## Recover a request

An interrupted connection can leave delivery uncertain. The form preserves the
submitted settings, generation, and request key. Select **Retry identical
deployment request** to recover the same operation. Do not create a new request
just because the first response was lost.

If refreshed history reveals a mutation hold, an authorized operator can still
retry the retained exact request to recover its existing operation. The settings
and request key remain locked. This retrieves the earlier receipt; it does not
admit a new workload change or clear the hold. New requests remain paused, and
permission, availability, conflict, and restored-ownership restrictions still
apply to recovery.

A conflict can indicate changed configuration or placement, another operation,
or a reused request key with different settings. The UI refreshes configuration
and history without resubmitting. Review both before choosing a separate intent.
To use changed settings, explicitly acknowledge that the earlier operation may
already exist, select **Use reviewed configuration for a new request**, edit the
settings, and acknowledge interruption again. The next submission receives a
new request key.

If deployment information cannot be refreshed, the form preserves its settings
and pauses actions. Retry the refresh before submitting. Reloading the page
retrieves persisted operation history; it does not resubmit a deployment.

## Read operation history

Project viewers can read bounded pages of deployment history and select an
operation. Details show its exact captured image, generation, binding,
destination, runner, namespace, local port, readiness request, resolved image
evidence when available, and recorded state.

Queued, dispatched, preparing, prepared, applying, and verifying describe
progress through one operation. Succeeded records a completed operation. A
recorded readiness result describes that completion; it is not continuous
monitoring and does not prove the current workload remains healthy.

## Cancel and reconcile

A project maintainer can request cancellation. Before apply admission, the
operation may settle as canceled without a workload change. After admission,
`cancel_requested` or `reconciliation_required` means apply may have started.
Cancellation does not prove the workload stopped or that rollback completed.

A retained mutation hold blocks another workload change until the server settles
the operation. Review the recorded result for uncertainty or restoration of the
previous workload.

A project owner, or the server's system-administrator exception, can request
reconciliation. It schedules a separate observation against the recorded target;
it does not authorize replaying a deployment. The UI shows the observation run
separately from the original deployment run and retains the distinction between
the recorded deployment result and observation requests.

## Restored ownership remains blocked

After restoring the platform database, a binding can retain a separate restored
ownership hold, including when it had no operation before the backup. Deployment
admission returns `hosting_restore_held` until trusted local recovery proof can
be supported. The UI preserves the submitted settings and shows the blocked
recovery state. This is not a configuration-generation conflict.

`restore_ownership_held` is independent of `mutation_hold`. A read-only
reconciliation can observe the target, but cannot clear restored ownership or
resume deployment admission. This image deployment workflow has no administrator
unlock action. Do not repeatedly resubmit or replace the request key to bypass
the hold.
