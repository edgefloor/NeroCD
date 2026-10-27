# Managed Compose hosting

A hosting application can save a `compose` source without a Git repository.
The source contains the exact original Compose text, the public component name,
and its container port. Each resource generation retains the original bytes,
their SHA-256 hash, and the normalized definition in
`hosting_compose_source_versions`.

Creation requires a 32-character lowercase hexadecimal `request_id`. The create
receipt commits with the resource and normalization. Retrying an identical
request returns the existing resource without overwriting later edits. Reusing
the identity with a different request returns a conflict. Updates use the
ordinary resource `expected_generation` compare-and-swap.

## Managed profile

The `managed-compose.v1` normalizer accepts up to 16 services with image tags or
repository digests, exec-form commands, environment mappings or lists, named
volume mounts, container healthchecks, and dependency conditions. Images without
a tag normalize to `latest`; immutable image resolution belongs to deployment
preparation. Dependency conditions include `service_started`, `service_healthy`,
and `service_completed_successfully`.

The original source preserves variable references. Runtime configuration stores
nonsecret literals or versioned managed-secret references separately. Supported
interpolation includes `$NAME`, `${NAME}`, unset and empty defaults, required
values, alternate values, and escaped `$$`. Missing required values produce an
error containing the variable name, without the source error message or value.
Nested interpolation is unsupported in this profile.

Unknown fields, duplicate YAML keys or environment names, aliases, bind mounts,
external volumes, custom volume drivers, dependency cycles, and missing health
contracts fail preflight. Build directives, Git Compose execution, and arbitrary
raw Compose remain outside this profile. The legacy Git Compose volume policy
is unchanged.

## Deploy and redeploy

In Project hosting, create an application with **Compose stack** as its source.
Paste the original source, choose its public component and container port, then
save the desired destination. Save referenced nonsecret variables and exact
managed-secret versions in Runtime configuration. Secret creation and rotation
remain separate explicit actions; redeployment never generates new credentials.

Review the saved Compose deployment and acknowledge interruption. The first
request can allocate a runner-local port or use an available explicit port.
After the stack settles healthy, configure its ordinary managed HTTPS route.
Subsequent deployments reuse that port and automatically submit the existing
route-continuity selection. The same selected HTTPS routes move to the healthy
successor through the ordinary proxy operation. Foreign routes, stale versions,
revoked grants, and unsettled proxy ownership keep their existing barriers.

The runner needs `hosting_managed_compose_v1`, `hosting_runtime_env_v1`,
`image_release_ports_v1`, and `image_deploy`, with shell execution disabled.
The current CLI advertises the stack capability with `--hosting-runtime-env`.
Docker Compose v2 or later must be installed as a normal CLI plugin or the
`docker-compose` executable. Both invocation forms use the bounded command
adapter, an isolated registry configuration, an explicit local Docker socket,
and a protected stdin document. The agent never inherits interpolation values
from its host environment or persists expanded Compose credentials in `.env`.

Preparation resolves every service image tag to an immutable release before
requesting apply admission. The durable `managed-compose.compiler.v1` plan
freezes the normalized source, image set, runtime configuration reference,
destination, stable component identities, private network, and owned volume
origins. The existing lease, fence, local journal and binding mutation hold
control execution. Journal version 23 prevents older agents from interpreting
stack intents as single-container operations while preserving mixed history.

An apply grant can create missing new owned volumes once. Their Docker creation
identity is sealed with the controller before any application writer starts.
Retained volumes are external to the rendered Compose document; a missing
retained volume fails instead of creating an empty replacement. Replacement
verifies every predecessor, stops all old writers, verifies that they stopped,
and removes only those exact containers without deleting volumes. Compose then
starts the pinned services using their declared dependency conditions and
private service DNS. Only the selected public component publishes a loopback
port. Successful completion requires all long-running components healthy,
successful initialization jobs exited zero, and the public HTTP readiness check.

Operation details show each component's recorded container, image, running state,
exit code and health. A failed or canceled replacement retains a mutation hold
when its outcome cannot be verified. Its receipt reports actual inspected
components or an explicitly incomplete observation. It does not claim prior
recovery or undo application/database writes. Another mutation cannot compete
with that hold; ordinary observation cannot silently authorize replay or volume
creation. Named volumes are retained when components disappear from later
source revisions; no implicit data deletion or stack volume cleanup is provided.

## Component runtime logs

For a settled healthy stack, select a Compose component in Runtime logs, then
choose **Follow runtime logs**. Application, PostgreSQL and retained successful
init containers are separate targets. Selection alone opens no collection.
Changing selection closes the prior follow; an init container may have no output.
A completed container stream ends as disconnected after delivering its framed
output. Target discovery uses the current settled receipt, independently of
route availability or changed desired deployment settings.
Redeployment ends the old session as `target_changed`, retaining its original
component and operation identity. Start a new Follow explicitly for the successor.
Failed, held and superseded deployments cannot open component follows.

The runner must opt in with `--hosting-runtime-logs` and advertise both
`hosting_runtime_logs_v1` and `hosting_runtime_stack_logs_v1`. Existing log
permissions, sensitive-output handling, short demand leases and retention apply.
The extended Compose gate checks browser component selection and Follow/Stop,
actual app/PostgreSQL output, finite output from an explicit test-only init
command added to the pasted example, and redeployment ending an
old follow. Component logs passed qualification at the candidate recorded below.

## Qualification

Focused source/runtime checks cover unsupported definitions, reference expansion,
protected dollar escaping through installed Compose configuration, stale writer
fences, creation-grant replay, stable volume origins, and held failed replacement.
PostgreSQL and Docker checks require their existing isolated prerequisites; a
skipped test is not physical evidence.

The opt-in existing runtime gate is
`bash acceptance/runtime-hosting-storage/gate.sh --managed-compose`. Run it only
against the explicitly authorized committed candidate and owned target profile.
It uses the distinct `nerocd-compose-vikunja-runtime-20261027` DinD owner, its
Docker-data volume, an ephemeral loopback administration port, and loopback
`25443` forwarded to the guest managed HTTPS port. The connected browser pastes
the shipped Vikunja/PostgreSQL example, saves variables and secret refs,
deploys, configures the managed route, creates an application account/task/file,
redeploys, then checks fresh login, retained data and new writes. The Go fixture
checks exact secret bytes, private PostgreSQL, component health, volume origins,
writer stop/start events, HTTPS trust and a controlled failed replacement hold.
Physical qualification and independent reviews remain required for acceptance;
source and build checks alone do not establish a working application.

Physical qualification passed on 2026-10-27 at
`1c2ee593f07d30f7ce25c1719c5a3b0caa5a4279`:

- [x] Saved Compose source, variables, managed-secret references and destination;
  deployed through the UI and opened the application over verified HTTPS.
- [x] Redeployed at the same URL; fresh login retained application records and
  uploaded file bytes, with successful new writes and unchanged volume ownership.
- [x] A controlled failed replacement reported actual component state, retained
  its mutation hold and rejected a competing deployment.

The existing gate evidence is preserved under
`$HOME/.local/share/nerocd/acceptance/runtime-hosting-compose-20261027/1c2ee593f07d30f7ce25c1719c5a3b0caa5a4279.fdSE3b`.
This qualifies the managed profile and shipped example, not raw/Git/build parity
or unverified rollback after a failed stateful replacement.

Physical qualification also passed on 2026-10-27 at
`b8f249c431f9ac6e33914789db3adb03c6629b78` with the existing
`--managed-compose` gate:

- [x] Saved and deployed the managed Compose configuration over verified HTTPS;
  redeployment retained application records and uploaded bytes, with fresh login
  and successful new writes.
- [x] The compiled API/runtime test passed its controlled failed replacement
  checks, including the mutation hold and rejection of a competing deployment.
- [x] Component log selection, Follow and Stop passed in the browser, alongside
  application/PostgreSQL output, finite init output and old-follow replacement.
- [x] Stack Stop/Start retained application data and uploads.
- [x] PostgreSQL component backup and separate-target import passed. This restores
  database data only; original source uploads are excluded. Later source writes
  remained excluded and target writes were independent.
- [x] All 39 baseline container profiles and all 222 volume profiles remained
  unchanged, with no unclassified additions. The user's UI on port 18081 retained
  its identity, healthy response and content.

Safe receipts are preserved under
`$HOME/.local/share/nerocd/acceptance/runtime-hosting-compose-20261027/b8f249c431f9ac6e33914789db3adb03c6629b78.GUz1EX`.
The gate ran once from `2026-10-27T08:22:46Z` to `2026-10-27T08:26:18Z`
(212 seconds); `TestHostingComposeVikunjaCompiledRunnerHTTPS` passed in
127.26 seconds. Separate command windows completed cheap checks in 12 seconds
in total. This qualification required no product or fixture repairs and no reruns.
The qualified image-store profile is classic overlay2; containerd image-store
support remains unqualified.

Local [PostgreSQL component schedules](hosting-database-backup-schedules.md#schedule-a-compose-postgresql-component)
passed qualification on 2026-10-27 at
`09cd2670154443f788963d9812aa6b77a2c919c8`:

- [x] Created a local schedule for the immutable PostgreSQL 16 component after
  review of the current settled deployment; each due backup follows the current
  deployment and retains its exact operation identity.
- [x] Disabled the schedule and inspected occurrence history. Current authorization
  applies to history, disable, and identical-request replay while unavailable.
- [x] One scheduled backup produced a local archive and passed separate-target
  import with the same immutable blob identity. This covers database records only.
- [x] All 39 pre-existing containers, all 223 volumes, and the user's UI identity,
  content, and health on port 18081 remained unchanged.
- [x] Independent hygiene, specification, and security reviews reported no findings
  at this candidate.

Safe evidence is preserved under
`$HOME/.local/share/nerocd/acceptance/runtime-hosting-compose-20261027/09cd2670154443f788963d9812aa6b77a2c919c8.mFI17j`.
The gate ran once from `2026-10-27T08:50:32Z` to `2026-10-27T08:54:17Z`,
225 seconds, with the physical runtime test passing in 134.33 seconds.
`make verify`, `make check-generated`, `make build`, focused PostgreSQL checks,
API preflight, and the full frontend suite passed at `39c7e49672498e564735eda1d5f1894cbab24aa6`.
Only the schedule UI test changed afterward, and its 11 tests passed at `09cd267`;
the built UI manifest was unchanged. The initial frontend run retained one DOM
assertion failure. Targeted reproductions passed, followed by one full rerun with
1,879 passing tests. A baseline flake is not proven. Broad `make check` was not run.

Implementation and checks overlapped from 08:30:14 to 08:43:43 UTC. Recorded
estimates were seven minutes for backend implementation, one for product
corrections, two for tool fixtures, and three for checks; these are not additive.
UI implementation from 08:30:56 to 08:37:47 UTC overlapped tests. Browser fixture
repair ran from 08:42:31 to 08:42:49 UTC, 18 seconds. Later test-only coverage ran
from 08:47:09 to 08:47:45 UTC, 36 seconds. Integration verify,
generated checks, and build took 226, 49, and 14 seconds in overlapping windows.
Review waiting was not instrumented.

The remaining gap is coordinated Compose application and upload recovery.
Compose schedules support local database archives only; standalone PostgreSQL 17
remote-target behavior is unchanged. Raw Compose, Git Compose, and mixed
build/image stacks remain unqualified. These results do not qualify full parity
or automatic rollback after a failed stateful replacement.

## Pasteable Vikunja/PostgreSQL example

Use `examples/compose/vikunja-postgresql.yaml` for the tested application workflow.
It pins Vikunja 2.7.1, PostgreSQL 16 and BusyBox 1.37.0 by immutable image digest.
Supply the public HTTPS URL and exact managed PostgreSQL and JWT secret references
through runtime configuration; the username and database default to `vikunja`.

The unmodified Coolify PostgreSQL template is retained byte-for-byte in
`internal/domain/testdata/vikunja-with-postgresql.yaml` as a parser comparison.
Its whole-directory `/app/vikunja/` volume is incompatible with the qualified
Vikunja 2.7.1 image: the application runs as UID 1000 but Docker copies that
directory as root, so creating uploaded files fails. A bounded ordinary Compose
comparison reproduced the same ownership for both Compose-created and precreated
external volumes. This is an upstream template/image issue.

The shipped example adapts the BusyBox initialization pattern from Coolify's
SQLite sibling template at revision
`b12d1386f3d458618b4c579babcd1e339c6c40b3`. It mounts only Vikunja's upload files,
so application binaries stay in the selected image. A one-shot initialization
service sets UID/GID 1000 and mode 0755 only on empty upload storage. It leaves an
already-correct directory unchanged, and fails on incompatible nonempty storage.
It performs no recursive ownership changes. The application waits for successful
initialization and healthy PostgreSQL through ordinary Compose dependencies.
These are explicit example instructions, not implicit platform permission changes.

## Upstream reference

The reference is Coolify commit
`b12d1386f3d458618b4c579babcd1e339c6c40b3`. The vendored
[Vikunja source](../internal/domain/testdata/vikunja-with-postgresql.yaml) has
SHA-256 `4ae479af1c9ee76836315b952db8d177fb1b1426316ead2f0f5c09bfa4acdb54`.
Its upstream path is `templates/compose/vikunja-with-postgresql.yaml`, licensed
under Apache-2.0 by the Coolify project.

The inspected paths are `DockerCompose.php::submit`,
`ServicesController.php::create_service`, `Service::parse`,
`Service::saveComposeConfigs`, `serviceParser`, `StartService`, and `StopService`.
Coolify saves raw source and retains component and generated variable identities
with `firstOrCreate`. It resolves remote secrets before stopping a service,
keeps a stable Compose project identity, and preserves dependency conditions.
NeroCD retains those behaviors through its existing Go runner authority,
protected material delivery, durable receipts, and owned-volume provenance.
SSH scripts, persistent `.env` files, and remote shell cleanup are not the
execution mechanism for this managed profile.
