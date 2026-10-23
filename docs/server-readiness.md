# Server readiness diagnostic

`basic_image_hosting.v1` reports prerequisites for an enrolled trusted image host.
The diagnostic does not authorize deployment, reserve capacity, or provision machines.
It does not inspect workspaces, machine pools, VMs, or SSH targets.
Agent collection and UI presentation require separate implementation and runtime verification.

## HTTP contract

`POST /api/v1/runner/server-readiness/challenge` accepts exactly `{}` under the current runner bearer credential.
The server resolves the immutable server, destination, and runner IDs. The runner must have
`hosting_readiness_v1`, an active nonempty credential, and a used, nonrevoked enrollment matching that exact credential.
A runner with `shell` capability cannot collect this diagnostic. Credential rotation without a matching enrollment leaves readiness unknown.

The challenge contains `sample_id`, `profile`, `server_id`, `destination_id`, `runner_id`, `issued_at`, and `expires_at`.
`sample_id` is `srd_` followed by 32 random lowercase hexadecimal digits.
The database clock sets the ten-second deadline after authority and readiness locks.
An unexpired pending challenge under the same authority returns unchanged.

`POST /api/v1/runner/server-readiness/report` accepts the exact flat `ServerReadinessReport` in [OpenAPI](../openapi.yaml).
Every field is required, including zero values and empty strings.
Both POST bodies are limited to 8 KiB and reject duplicate, unknown, case-folded, or null fields.
Reports contain no paths, environment variables, raw stderr, or raw Docker output.
Enum values and numeric bounds appear in the OpenAPI schema.
The report duration is 0 through 5000 milliseconds. Free byte counts are nonnegative signed 64-bit integers.
Docker API versions are empty or numeric `major.minor`, with one to three digits per component.
Daemon IDs contain at most 128 printable ASCII bytes.

The acknowledgment contains only `sample_id`, the original database `observed_at`, and `accepted: true`.
Acceptance records evidence. It does not qualify current readiness.
Exact canonical report replay returns the original acknowledgment without extending freshness.
Changed replay bodies, expired unaccepted samples, superseded challenges, and changed authority return HTTP 409.
One server row retains at most one pending challenge and one accepted report and receipt.
A pending challenge does not erase the previous accepted sample.

`GET /api/v1/servers/{id}/readiness` requires the existing global destination administrator role.
All responses use `Cache-Control: no-store`.
The response contains `server_id`, `profile`, `status`, and `reason`, with optional accepted `sample_id`, `observed_at`, and `report`.
Private authority hashes never appear in response DTOs.

## Qualification

GET derives status from one SQL snapshot, in this order:

1. Unavailable or changed current runner authority returns `unknown`, with `authority_unavailable`.
2. A current binding restore hold or `restore_admitted=false` returns `blocked`, with `restore_hold`, including absent or stale samples.
3. No accepted sample returns `unknown`, with `no_sample`. A sample older than 30 seconds returns `unknown`, with `stale`.
4. Any nonnull current binding daemon pin differing from the observed daemon returns `blocked`, with `daemon_mismatch`.
5. Failed profile prerequisites return `blocked`, with `prerequisite_failed`.
6. A fresh, current, qualifying sample returns `ready`, with `qualified`.

Qualification requires Linux on both the agent and daemon, matching native `amd64` or `arm64` architecture,
enabled execution, matched configuration, available Docker, API `1.54`, a nonempty daemon ID,
an available safe CLI, accessible work and journal roots, at least 1 GiB free for each root, and no collection error.
The profile supports the known Docker API fixture. Another API version cannot qualify through this profile.
Heartbeat age does not extend or invalidate the independent sample clock.
There is no new permanent daemon baseline. Current binding pins determine daemon mismatch on each GET.

## Transaction and restore behavior

Issue and report lock the runner for SHARE, then the matching used enrollment for SHARE,
then the readiness row for UPDATE. Row insertion occurs after both authority locks.
The database clock is read after lock waits. A final-write clock check rolls back an acceptance that crosses the original challenge deadline.
Immutable placement relations require no destination,
resource, binding, job, or lease lock. Collection does not use River or append history.

Successful database restore deletes all pending and accepted readiness state, including unused servers.
The deletion shares the existing atomic restore invalidation transaction.
The migration adds `nerocd_server_readiness_schema_compatible()` to the current schema check.
The diagnostic does not change deployment or image compatibility behavior.
