# Server readiness diagnostic

`basic_image_hosting.v1` reports prerequisites for an enrolled trusted image host.
The diagnostic does not authorize deployment, reserve capacity, or provision machines.
It does not inspect workspaces, machine pools, VMs, or SSH targets.
The agent collector is explicitly enabled with `--hosting-readiness`. UI presentation has a separate implementation and verification boundary.

## Agent collection

`nerocd runner --hosting-readiness` requires `--execute` and forbids `--once`,
`--complete-status`, and an explicit `shell` capability. The option adds
`hosting_readiness_v1` and removes only the implicit default `shell` capability.
Both identity checks require parity between the persisted capability and local opt-in.
The command requires HTTPS with validated TLS. Readiness requests and their identity checks
reject redirects, disable environment proxies, and have two-second request limits.
Readiness-enabled enrollment and registration use the same TLS, redirect, and proxy policy.
Bootstrap has a ten-second overall window and two-second request limits under signal cancellation.
Enrollment retries preserve the persisted request ID and credential hash; registration has one
attempt because its endpoint has no enrollment-style idempotent receipt.
The literal-loopback HTTP allowance exists only in test configuration, never as a CLI option.

Collection starts after every startup journal reconciliation. It samples immediately, then
runs one sequential cycle every ten seconds. It creates no journal records or jobs.
Each cycle retains its original ten-second limit and challenge expiry. A sample has a
five-second limit. A transient report failure can retry once with the identical report;
HTTP 401, 403, 404, or 409 abandons the cycle. Cancellation closes the sampler and joins
its collector before the runtime returns.

The sampler makes only Unix-socket Docker GET requests to `/version` and `/v1.54/info`.
It repeats `/v1.54/info` to check daemon identity stability. Raw response buffers are bounded
and cleared. The collector does not pull, create containers, execute a CLI, or initialize
an execution validator. CLI availability records safe executable-file metadata, not proof
that a deployment can run. Architecture aliases `x86_64` and `aarch64` map explicitly to
`amd64` and `arm64`; unsupported native platforms cannot qualify.

Linux qualification assumes responsive local filesystems for the CLI path and the work and journal roots.
Metadata and available-byte checks run synchronously without creating files. They reject
symlinks and unsafe permissions; the resolved journal root must remain owned by the runner
and mode 0700. Available-byte multiplication rejects signed 64-bit overflow.
Filesystem syscalls cannot be forcibly cancelled, so this is not a hard OS deadline.
The collector discards a sample that returns late instead of leaving a timeout goroutine.
Shutdown join relies on the same responsive-filesystem assumption.

Configuration, identity, journal opening, or replay failures exit before collection.
A host without an accepted sample remains `unknown`; earlier evidence ages under its
original freshness limit. Startup failures do not fabricate a blocked or ready sample.

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

## Runtime verification fixture

`bash scripts/ci/server-readiness-runtime-gate.sh` compiles the actual agent and runs
it on native Linux in a uniquely owned Docker 29.4 fixture with PostgreSQL 17.6.
The gate checks real-clock staleness, response-loss retries, recovery, unsafe roots,
unsupported and unreachable Docker, revocation, and signal-driven shutdown.
Its metadata-only Docker executable records accidental invocation; the collector must
never execute it. The fixture uses a trusted local TLS certificate and credential file.
This verifies that local test setup, not production TLS or deployment execution.
The gate removes its exact labelled containers and build image without pruning host state.


Set `NEROCD_READINESS_BROWSER=1` when running the gate to qualify the manual
onboarding flow through the served UI and real API. Build dependencies must already
be installed in `web/app`. This mode builds `web/dist` before compiling the Go
fixture, records its asset manifest and binary hashes, and checks served asset bytes.
The browser creates a one-time readiness enrollment, hands its displayed command to
a bounded test-only helper, and registers the consumed runner using the normal server
form. The helper validates the exact generated command and invokes parsed argv with
`exec.Command`; only the four fixture paths and polling interval change. The compiled
Linux agent consumes the enrollment over fixture-CA-verified HTTPS and persists a
mode-0600 credential. A subsequent restart removes only the consumed enrollment-file
flag and uses that persisted credential; it must not consume enrollment again. Browser UI traffic uses separately labelled loopback HTTP.

The actual browser case uses no fabricated API responses or readiness database
writes. It observes ready, failed prerequisites, recovery, real-clock staleness,
agent credential revocation, and current administrator role loss. The separate native
collector case also checks consumed-enrollment revocation. A nonce-scoped helper accepts only
bounded named actions, the pinned authenticated session, exact Origin and CSRF header;
a separate recovery administrator completes cleanup after the tested actor loses its
role. Secrets stay out of traces and screenshots, which are captured after dismissal.
Checks include keyboard refresh and widths 320, 390, 430 and 1440. The existing
synthetic browser case remains rendering evidence only. The gate owns ports 32965
and 32966 while active and removes its exact labelled fixtures. This qualifies the
bounded manual enrollment prerequisite; it does not configure SSH, verify production
TLS, deploy an application, or complete the full A01 workflow.
