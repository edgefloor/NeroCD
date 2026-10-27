# Hosting destinations

A server gives one enrolled runner a stable hosting identity. Each server has
one immutable `docker_compose` destination named `default`. To admit a server,
use an active, credentialed runner with a consumed, nonrevoked enrollment,
`compose-deploy` capability, and no `shell` capability. Admission also accepts
the historical `compose_deploy` spelling. Migration 0044 adds canonical
capability support to databases that already applied migration 0042.
Run `nerocd migrate` before starting NeroCD to apply the full migration set.
Startup and readiness reject the old admission function.

A runner can belong to one server. These records do not reserve capacity or
change legacy scheduling.


Global `runner_admin` and `system_admin` principals can list and admit servers,
inspect destinations, and manage explicit project grants. Project viewers can
list destinations granted to their project. Project maintainers can bind a
resource from that project to a granted destination. Service accounts use their
persisted project role and cannot administer global server infrastructure.

## Manual onboarding through the WebUI

1. As a runner administrator, open **Runners** and create a one-time enrollment.
   Choose the basic image hosting preset, or choose **Use image hosting with
   runtime configuration, observations and logs** for protected runtime values and
   independent status and demand-driven runtime log collection. Both include
   `compose-deploy`, `image_deploy`,
   and `image_release_ports_v1` without `shell`. The second also includes
   `hosting_runtime_env_v1`, `hosting_observation_v1` and `hosting_runtime_logs_v1`
   and requires HTTPS.
   Use a unique runner ID
   and suitable runner tags. The secret dialog shows the exact runner ID and
   expiry. Save the token once; closing or expiry clears the browser copy.
2. On the intended host, install NeroCD, Docker Engine, and Docker Compose.
   Create a dedicated runner user, owner-only persistent work and journal
   directories with mode 0700, and a private enrollment file with mode 0600.
   Transfer the downloaded enrollment token to that file securely.
3. Set the dialog's server URL to the actual origin reachable from that host.
   Production runners require HTTPS. Run the file-based command below as the
   runner user. Execution defaults to enabled in the CLI; `--execute` makes that
   intent explicit. The command consumes the enrollment file and stores the
   permanent credential in an owner-only file. Keep the credential, work, and
   journal paths persistent. The browser never receives that permanent credential.
4. Return to **Runners**, close the enrollment dialog, and choose **Refresh
   eligible runners** under **Hosting servers**. Register the enrolled runner
   using a server name. Admission verifies consumed enrollment and credentials.
5. Select the server destination and grant the project's exact ID. A project
   user can find that ID under **Projects → Project hosting**. A global runner
   administrator can enter an authorized project ID without project membership.
6. Under **Project hosting**, create a shared environment and save an image
   application. Select its granted **Desired destination** and save placement.
   Reloading retains the binding and its platform-generated runtime namespace.

```sh
nerocd runner --server https://nerocd.example.com \
  --enrollment-file /secure/nerocd-runner-enrollment.token \
  --credential-file /secure/nerocd-runner.credential \
  --work-dir /var/lib/nerocd/work \
  --journal-dir /var/lib/nerocd/journal \
  --capabilities 'compose-deploy,image_deploy,image_release_ports_v1' \
  --compose-image-policy pull --execute
```

Replace the example origin with the runner-reachable HTTPS origin. For local
HTTP testing only, explicitly set `NEROCD_MODE=development`; the WebUI command
includes this prefix for HTTP origins. Keep tokens out of command arguments,
URLs, shell history, and shared logs. If copying the token fails, the dialog
reports failure and offers a one-time file download instead.

The dialog derives the command from the returned enrollment's persisted
capabilities. Editing a draft does not alter those grants. The runtime configuration,
observations and logs profile adds
`--hosting-runtime-env --hosting-observation --hosting-runtime-logs` to the
command above and includes the corresponding versioned capabilities in
`--capabilities`. Use each flag only when its persisted capability is present.
These modes reject HTTP even with `NEROCD_MODE=development`. Runtime configuration
also requires `image_deploy` and `image_release_ports_v1`. None of these modes can
share
`shell`. Runtime log collection requires execution and forbids `--once` and
`--complete-status`. It reads container output only while an authorized viewer has
active log demand. Enrollment alone does not start a log session. See the
[runtime log workflow](hosting-runtime-logs.md).

The dialog withholds a complete command for advanced grants because the enrollment
does not contain the operator's host configuration:

| Persisted capability | Required operator configuration |
| --- | --- |
| `hosting_build_v1` | `--build-config-file` with an owned, nonsymlink regular mode-0600 JSON file. Configure the actual toolchain, target, frozen policy, delegated cgroup and disk limits. See [build setup](hosting-build-runtime.md). |
| `hosting_git_https_basic_v1` | `hosting_build_v1` and the build configuration's `private_git` opt-in with matching HTTPS source credential policy. See [private Git setup](hosting-build-runtime.md#enable-private-https-git). |
| `proxy_apply` | Linux, `--proxy-state-root`, `--proxy-docker-binary`, `--proxy-docker-socket`, and `--proxy-protocol managed-traefik.file.v1`. Supply the actual persistent mode-0700 root, Docker executable and local Unix socket. See [proxy setup](hosting-proxy-control.md). |
| `local_build_deploy_v1` | `--local-build-deploy` and the same runner and Docker daemon as the successful build. A runner that also builds needs its validated build configuration. See [local deployment setup](hosting-local-build-runtime.md). |

Keep credentials out of build configuration. The WebUI does not create these files
or choose cgroups, proxy paths or credentials. Invalid grant combinations and unknown
custom capabilities also withhold the command. The one-time token remains available
until dialog close or expiry so an administrator can finish the documented setup.

Legacy `compose-deploy`, historical `compose_deploy`, and shell enrollments retain
file-based commands with their exact persisted capabilities. Those commands do not
qualify a host for the image hosting profile or verify Docker readiness.

These steps register records and save desired placement. They do not automate
SSH, test the Docker daemon, configure HTTPS, reserve capacity, or deploy an
application. The runtime kind `docker_compose` is separate from the canonical
runner capability `compose-deploy`. Historical `compose_deploy` capability
records may exist; new enrollment instructions use the canonical spelling.

## Admit a server and grant a project

Create a server with `POST /api/v1/servers`:

```json
{"name":"Production host","runner_id":"runner_production"}
```

The response contains `server` and `destination`. NeroCD generates their IDs,
creator, timestamps, and the default destination name. Use
`GET /api/v1/servers/{id}` to read the same detail. `GET /api/v1/servers` returns
a bounded list of server records.

Grant a project with `POST /api/v1/destinations/{id}/project-grants`:

```json
{"project_id":"proj_production"}
```

List destinations with exactly one scope:

- `GET /api/v1/destinations?project_id=proj_production` requires project viewer
  access and returns only active grants.
- `GET /api/v1/destinations?server_id=server_example` requires global runner
  administration and returns the server's destination.

Lists use `items`, `limit`, `offset`, `count`, and `total`. Administrative
`GET /api/v1/destinations/{id}/project-grants` includes active and revoked grants.

## Bind a resource

Create desired placement with `POST /api/v1/resources/{id}/binding`:

```json
{"destination_id":"dest_example"}
```

NeroCD authorizes the resource's persisted project and generates the binding ID,
creator, timestamp, and runtime namespace. Requests cannot supply a project or
namespace. The binding is immutable; a duplicate or attempted replacement
returns HTTP 409. Project viewers can read it with
`GET /api/v1/resources/{id}/binding`.

A binding records desired placement. It does not deploy a resource, grant an
execution lease, or imply that a process is running. Future execution must check
current authorization and runner eligibility.

## Revoke a grant

Revoke permission with
`POST /api/v1/destinations/{id}/project-grants/revoke` and the same `project_id`
body. Revocation prevents new bindings and removes the destination from the
project's available list. Existing bindings remain readable and unchanged.
Revocation does not stop, delete, move, or replace workloads. Granting the same
project again reactivates permission for new bindings.

The API provides no server removal, target replacement, or binding update.
