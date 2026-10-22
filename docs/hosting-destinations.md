# Hosting destinations

A server gives one enrolled runner a stable hosting identity. Each server has
one immutable `docker_compose` destination named `default`. To admit a server,
use an active, credentialed runner with a consumed, nonrevoked enrollment,
`compose_deploy` capability, and no `shell` capability. A runner can belong to
one server. These records do not reserve capacity or change legacy scheduling.

Global `runner_admin` and `system_admin` principals can list and admit servers,
inspect destinations, and manage explicit project grants. Project viewers can
list destinations granted to their project. Project maintainers can bind a
resource from that project to a granted destination. Service accounts use their
persisted project role and cannot administer global server infrastructure.

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
