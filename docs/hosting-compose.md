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

For private service images, enable `--hosting-registry-pull` and
`hosting_registry_pull_v1` in addition to those Compose capabilities. In the saved
Compose deployment review, choose **Private registry**, an active project secret,
and its exact version. The secret has the existing closed
[`registry`, `username`, `password` contract](hosting-image-deployments.md#pull-a-private-image).
Its normalized registry must match at least one service image. That credential
authenticates only matching services; services from other registries pull
anonymously. Initial deployment, redeployment, and Start retain the selected
`registry_credential` reference in their immutable snapshots.

All service images are pulled before the prepared receipt or workload mutation.
Credential delivery uses verified controller HTTPS and fenced attempt authority; Engine
authentication stays in memory, without Docker login or durable Docker config.
Prepared recovery reuses its frozen release without credential redelivery. One
registry credential can be selected per stack. Server registry inventory,
login/logout, push, and multiple selected private registries remain unsupported.

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

## Prepare and deploy Git Compose

The `git-compose.v1` profile keeps the application as a Git resource with its
repository, ref and immutable source history. The bounded private HTTPS mixed
build/image workflow is [qualified](#git-compose-qualification); pasted Compose
evidence below retains its separate scope.

Enable the existing [build runtime](hosting-build-runtime.md) first. In the server
policy file selected by `NEROCD_HOSTING_BUILD_POLICY_FILE`, set `git_compose` to
`true`. In the runner file passed to `--build-config-file`, set top-level `compose`
to `true` and set `policy.git_compose` to `true`. The runner's complete policy must
match the server policy. These settings advertise `hosting_git_compose_build_v1`
alongside `hosting_build_v1`. Deployment also needs the managed Compose
capabilities and `--hosting-runtime-env` described above. Use the same bound runner
and Docker daemon for preparation and deployment.

1. In **Git repository**, enter the HTTPS repository and **Git branch, tag, or
   commit**. Choose **Docker Compose** as the build strategy. Set the optional
   context directory, Compose file, public service and container port. For example:

   ```json
   {
     "strategy": "compose",
     "context_dir": "apps/tasks",
     "compose_file": "deploy/compose.yaml",
     "public_component": "web",
     "container_port": 8080
   }
   ```

   The Compose file path is relative to the context directory. Each `build.context`
   is relative to the Compose file's directory and must stay inside the checkout.
   Its Dockerfile path is relative to that build context.
2. Save referenced values in Runtime configuration. For private HTTPS Git, use the
   existing [source grant and explicit secret version](hosting-git-resources.md#request-a-private-https-git-build).
   Saving configuration does not fetch source or change a running stack.
3. Request the existing public or private Git build. Wait for a succeeded build
   with its resolved commit, tree digest and prepared Compose result. An identical
   retry retains the frozen source receipt; a new build request may resolve a
   moving branch again.
4. Select that exact successful build ID. Review its stack, runtime generation,
   ports and readiness. For private service images, set **Image access** to
   **Private registry**, then select the **Registry credential secret** and explicit
   **Registry credential version**. Review the credential and route readback,
   acknowledge interruption, then choose **Deploy selected build**. Repeat the
   explicit selection for **Start selected Compose build**. The existing
   `/api/v1/resources/{id}/deployments/from-build` route selects persisted evidence.
   Configure the managed route after initial deployment.
5. To deploy changed Git source, prepare a new build and review its exact result.
   A failed fetch, missing file or failed build leaves the serving stack and its
   data untouched. Named volumes retain their identities across redeployment;
   changing a volume name does not delete the old volume.

A Compose file can mix registry images with local Dockerfile builds. Build
accepts a context string or a mapping with `context`, `dockerfile` and `target`.
An image-only service that names a built service's image uses that same local
artifact. Multiple build owners for one declared image are rejected. Local images
must remain on the recorded daemon; a missing artifact never falls back to a
registry pull.

Component logs, Stop and applicable backups use the deployed or stopped stack's
recorded proof after a desired-source edit or failed newer build. Start and a new
deployment require current resource and source generations. After changing the
source, prepare a new eligible build before Start; a denied stale Start preserves
the stopped receipt and volumes.

This profile does not support build arguments, build secrets, SSH credential
delivery, remote build contexts, repository file bind mounts, unrestricted raw
Compose or cross-daemon artifact transfer. Other unsupported fields in the
managed profile remain rejected. Private Git access uses source grants separately
from service-image registry selection. One selected registry credential authorizes
only matching registry-only service pulls. Built services and shared-image aliases
retain their exact local artifacts; other registries remain anonymous. This does
not provide Dockerfile base-image authentication, registry push, inventory,
login/logout or multiple selected private registries.

The sequence follows pinned PHP
[`ApplicationDeploymentJob::deploy_docker_compose_buildpack`](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Jobs/ApplicationDeploymentJob.php#L751):
clone the selected revision, load and validate the Compose file, prepare service
images, then start services. Its
[`Application::loadComposeFile`](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Models/Application.php#L2250)
restores source settings on read failure. NeroCD retains that preparation-before-
replacement order through its existing fenced build receipts and managed stack
admission.

### Git Compose qualification

The existing `gate.sh --git-compose` qualification passed at
`a5da2826a47854628c04449c23580a64eca71d25` on 2026-10-27. Private HTTPS Git
prepared a mixed Dockerfile/registry stack with a shared-image alias, then deployed
Vikunja/PostgreSQL over HTTPS. A changed commit redeployed with retained records
and upload bytes. Whole-stack Stop/Start preserved those data and allowed new
writes. Failed source/build/pull attempts preserved the prior workload; a failed
readiness replacement retained its reconciliation hold and rejected competing work.

Evidence, source hashes, independent review attribution, failed attempts, timing
limits and retained check scope are indexed at
`~/.local/share/nerocd/acceptance/runtime-hosting-git-compose-20261027/a5da2826a47854628c04449c23580a64eca71d25/evidence-index.json`.
Exact owned-process/container/volume cleanup preserved the 41-container,
236-volume baseline and running user instance. The index retains earlier
aggregate check failures and the historic anonymous-volume ownership evidence gap.

The unsupported fields and broader registry limits above remain open; this does
not complete A15/A16 or qualify standalone archive/database recovery again.
Unresolved proxy queuing after successful stack execution has a separate
[explicit recovery workflow](#stack-route-continuity-recovery).

### Git Compose private registry qualification

The same Git Compose gate passed at
`db58c6abc3b41948a66b3bc7bcf93b9748a6cc35` on 2026-10-27 with PostgreSQL in
one protected registry and Vikunja plus its alias kept as local build artifacts.
Wrong and revoked credentials preserved the serving containers, HTTPS routes,
records and uploads. Valid UI selection then deployed the changed commit and
started it again after removing the PostgreSQL image, retaining data and allowing
new writes. The existing failed-readiness hold and cleanup checks also passed.

The bounded evidence index records source hashes, checks, reviews, timing limits
and the retained prerequisite failures without replacing earlier Git evidence:
`~/.local/share/nerocd/acceptance/runtime-hosting-git-compose-registry-20261027/db58c6abc3b41948a66b3bc7bcf93b9748a6cc35/evidence-index.json`.
The broader profile limits above still apply.


### Stack route-continuity recovery

A stack can complete successfully while its proxy queue transaction fails. The
operation remains succeeded and continuity becomes unresolved; the separate
reservation blocks another deployment. Recovery retries only proxy queuing. It
does not deploy again, recreate volumes or change the completed workload receipt.

In the recorded operation detail, choose **Refresh operation** and **Refresh
continuity evidence**. For an unresolved continuity result with a successful,
ready, current workload, choose **Review proxy continuity recovery**. Review the
descriptor and completion receipt, select the displayed acknowledgment, then
choose **Retry proxy continuity queue**. An unconfirmed response retains the
exact request for **Retry identical proxy continuity recovery**. Current
permissions, grants and workload evidence are checked again; drift remains
blocked. After the successor loads, verify HTTPS and the application separately:
a loaded generation is historical evidence, not a current traffic guarantee.

Qualification passed at `a50aac18f0c97ac061127e3053843c9d237f4257` after an actual
queue failure during private Git Compose replacement. Explicit UI recovery and
identical replay produced one successor without changing writers, volumes or
receipts; records/uploads, uncached Start and failed-readiness holds also passed.
The earlier `6d5304e6` attempt failed in the UI. A rendered-component test proved
selection loss during a redundant membership fetch, repaired at `7e06bf1f`; the
exact historical timeout action was not retained. Source hashes, both attempts,
repair proof, reviews and cleanup are indexed at
`~/.local/share/nerocd/acceptance/runtime-hosting-stack-route-recovery-20261027/a50aac18f0c97ac061127e3053843c9d237f4257/evidence-index.json`.

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

## Local named-volume backups

In **Compose volume backups**, select a named volume and choose **Review Compose
volume backup**. Review its resource generation, deployed operation, component
mounts, and interruption warning. A project owner must acknowledge interruption
before choosing **Confirm Compose volume backup**. The controller requires a
settled deployment and a configured [archive controller](hosting-storage-archive.md).
The runner requires `hosting_compose_storage_archive_v1` in addition to its
managed Compose and archive capabilities.

A backup contains one selected local named volume. The runner verifies its
recorded creation origin, daemon identity, and current and retained component
users before stopping any container. It stops and resumes only users that were
running at the start, including read-only users. Completed init containers stay
stopped. Components that use other volumes keep running. Unknown users or changed
mounts reject the operation.

History records the selected volume, exact source deployment, archive identity,
and writer recovery result. After an unconfirmed submission, retry the identical
saved request to recover its receipt. Inspect history before creating a separate
request. A failed archive can still report successful writer resumption. An
unverified resumption retains the mutation hold and requires the existing
explicit storage-archive recovery action. Recovery resumes recorded writers;
it does not restore archived application data.

Compose volume backups reuse the installed archive helper and configured size
limits. [Named-volume schedules](hosting-storage-backup-schedules.md#schedule-a-compose-named-volume)
reuse that controller and archive runner. Each occurrence resolves the current
settled deployment while preserving the selected volume's recorded origin.
Schedule creation and enabling require fresh deployment review and consent for
every due interruption. Reads, history, disable, and identical replay retain
current authorization when runtime evidence is unavailable.

The [local archive download](hosting-storage-archive.md#download-a-local-archive)
control returns a selected successful backup without another interruption.
Compose archive restore, bind mounts, and coordinated database and upload
snapshots are unsupported. Archive selection does not establish database
consistency. Use the separate PostgreSQL component backup
workflow for database archives.

For an immediate remote copy, select a validated project target revision before
reviewing the backup. The [Compose remote workflow](hosting-storage-remote-backups.md#copy-an-immediate-compose-volume-backup-remotely)
retains the local archive, resumes writers, and then uploads the exact blob.
Failed upload retry does not stop writers or archive the volume again. The
remote extension [passed qualification](hosting-storage-remote-backups.md#accepted-compose-remote-copy).
Recurring backups have a separate **Remote scheduled Compose backup target**
selection and review. Each due occurrence uses its saved target revision and
current authority, preserves the local archive, then uploads after writer
resumption. See [scheduled remote-copy qualification](hosting-storage-remote-backups.md#compose-remote-schedule-qualification)
for its status and evidence.

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

### Accepted local named-volume backup

The bounded immediate local Compose volume-backup workflow is accepted on
2026-10-27 using product runtime and browser evidence at
`02a56396b514a15dc26b96f3cedc5456b12b110d` and preservation replay at
`37ddc31dab67ba5a230b4bbd8e319b7dc20eea52`. The latter changes only three acceptance
fixture files. Product source is unchanged. Independent hygiene, specification,
and security reviews reported no findings at `37ddc31`.

- [x] Selected and reviewed the exact named volume and deployment in the browser,
  acknowledged interruption, and inspected successful and failed backup history.
- [x] Verified a 5,120-byte local CAS archive with five members against actual
  uploaded file bytes, numeric UID/GID, and modes. The application resumed.
- [x] A task-owned FIFO caused archive failure with no published artifact.
  Writer resumption succeeded, PostgreSQL stayed running, and completed init
  containers remained stopped.
- [x] Application records, uploaded bytes, fresh login, and new writes survived
  backup and redeployment. Component logs, Stop/Start, PostgreSQL schedules and
  separate-target database import retained their existing assertions.
- [x] Failed replacement retained its mutation hold and rejected a competing
  deployment. Current authorization, exact volume origins, and durable recovery
  checks remained required.
- [x] All 39 baseline container profiles and 230 baseline volume profiles stayed
  unchanged. Task cleanup completed, and the user's instance on port 18081 kept
  its identity, content, and health.
- [x] The reviewed verifier classified two added containers and their anonymous
  volumes as non-task additions using captured mount provenance. Their creator
  attribution is not proven. The earlier capture with a replaced baseline
  container still fails verification.

Safe receipts and their hashes are indexed under
`$HOME/.local/share/nerocd/acceptance/runtime-hosting-compose-20261027/37ddc31dab67ba5a230b4bbd8e319b7dc20eea52/evidence-index.json`.
Both original aggregate gate commands remain failed. No Docker gate ran at
`37ddc31`, and no third full run was performed. Acceptance combines retained
product execution with focused verifier tests and read-only replay of captured
preservation evidence. The original receipt hashes are unchanged.

The first named-volume qualification attempt at
`b133bd59fadd20f07f7949405f3662f8f853337f` failed from
`2026-10-27T09:48:35Z` to `2026-10-27T09:52:46Z`, 251 seconds. The shared phase
handler omitted both new volume-backup browser transitions. Backup admission
returned 202, but the completion report returned 409 before the required archive
assertions could run. Cleanup also rejected an unrelated pre-existing container
replacement and a new anonymous volume. Owned resources were cleaned up, and the
user's instance on port 18081 preserved its identity, content, and health. This
attempt does not qualify the feature.

A deterministic Linux handler check reproduced both missing transitions as 409.
The fixture repair passed all six handler cases, including the existing
wrong-action rejection, before a targeted rerun. Its source commit is
`94983642`; the integrated candidate is
`02a56396b514a15dc26b96f3cedc5456b12b110d`. Product implementation is unchanged
from `b133bd5`. The repair took 82 seconds from 09:54:10 to 09:55:32 UTC,
including checks. Each test execution took less than 0.01 seconds; compilation
was approximately eight seconds. These checks have tool receipts, not retained
log files.

The second attempt at `02a5639` ran from `2026-10-27T09:57:30Z` to
`2026-10-27T10:01:11Z`, 221 seconds. The product runtime test passed in 139.92
seconds, and the browser workflow passed. Its aggregate command failed because
the shared preservation checks treated anonymous volumes attached to classified
non-task additions as unclassified, despite unchanged baseline profiles. The
focused verifier repair retained exact baseline checks, rejection of unknown
changes, task cleanup, and user-instance preservation. Sixteen tests passed in
0.007 seconds, and captured-snapshot replay took less than 0.1 seconds. Repair
work was approximately 10:03–10:06:21 UTC, including design and waiting.

Source checks retain their original revisions. The full frontend suite passed
1,898 tests in 128 files at `25d6d690eff58de0fed1ad88b2785c8a3229c622`, and its
build passed in 17 seconds. The only
later frontend change added recovery coverage, with seven passing tests at
`ccf9f829fa694ba74f33771cd642d78d930638da`; production UI and the built manifest
were unchanged. Generated checks
passed in 54 seconds at `78fc02404763466d0c4a0916c338ea2e25d64ff0`, with no later
SQL or schema changes. Isolated
PostgreSQL Compose admission, recovery, and replay checks passed in 2.408 seconds
before the journal-only repairs. Final focused Go checks, formatting, pinned
lint with zero issues, shell checks, and a fresh Go build passed at `b133bd5`.
The original 266-second `make verify` command failed on nine exported comments,
after formatting, vet, and all Go package tests passed. Comment repair and the
later lint pass do not turn that aggregate command into a pass. Broad
`make check` was not run.

Implementation and test windows overlapped. Initial backend implementation was
approximately 09:04–09:23 UTC and UI implementation approximately 09:05–09:12 UTC.
Fixture preparation took 76 seconds. An early browser phase-state repair, async
UI test repairs, and a generated-type adapter correction have no separately
measured totals. The first environment-map codec repair took approximately six
minutes including checks. The later mixed-journal repair took 222 seconds,
including focused tests and race checks. Final focused checks took 18 seconds,
formatting and lint took ten seconds, and the Go build took six seconds in
overlapping windows. Review and waiting time were not instrumented. These
measurements do not provide an additive elapsed-time partition.

### Accepted local named-volume schedules

Local named-volume schedules passed the planned workflow on 2026-10-27 at
`1df3c90a1aef7b3f4235b4ba43eb239415b29e51`. Independent hygiene, specification, and
security reviews reported no findings at that candidate.

- [x] Saved a disabled schedule against the first settled deployment and retained
  its identity and selected volume across redeployment and Stop/Start.
- [x] Enabled it after fresh review of the current deployment and explicit consent
  to interrupt the selected volume's running writers on every due occurrence.
- [x] One actual due occurrence produced a 5,120-byte local CAS archive with five
  members. File bytes, numeric UID/GID, modes, and source deployment matched.
- [x] Disabled the schedule and inspected occurrence and backup history. The app
  retained records and uploads and accepted new writes. PostgreSQL and completed
  init containers stayed in their expected states.
- [x] FIFO archive failure resumed writers without publishing an artifact.
  Component PostgreSQL scheduling and separate-target import, component logs,
  and failed replacement with a held mutation and rejected competitor passed.
- [x] All 41 baseline container profiles and 232 baseline volume profiles remained
  exact. Two added containers and their anonymous volumes were classified as
  non-task additions. Global inventory equality remained false. No task-owned or
  unclassified additions leaked, and owned cleanup completed.
- [x] The user's instance on port 18081 preserved its identity, content, and health.
  Served UI assets matched their manifest at 320, 390, 430, and 1280 pixels.

The complete aggregate gate passed from `2026-10-27T10:47:43Z` to
`2026-10-27T10:51:42Z`, 239 seconds. The physical runtime test took 153.57 seconds.
Safe evidence is indexed under
`$HOME/.local/share/nerocd/acceptance/runtime-hosting-compose-20261027/1df3c90a1aef7b3f4235b4ba43eb239415b29e51/evidence-index.json`.
No further full run was needed.

The first schedule gate at `531803554c28112ae0cc43358dde93abb4a01f2f` remains
failed. It ran from `2026-10-27T10:28:05Z` to `2026-10-27T10:30:33Z`, 148 seconds,
and timed out while saving the initial disabled schedule. All 41 baseline
containers and 232 volumes remained unchanged, with no additions. User-instance
preservation and owned cleanup passed. The original timeout cause is unresolved.

Before the second gate, focused PostgreSQL checks of the protected runtime's
initial deployment and route passed in 4.390 seconds. The full resource-page
browser check passed under normal conditions. Unavailable volume evidence and
query retirement reproduced possible timeout conditions but did not establish
the original cause. The two-file fixture diagnostic change at `1df3c90` did not
change production behavior or UI code. The passing rerun does not establish the
cause of the first failure.

Required affected Go package tests, formatting, vet, pinned lint with zero issues,
shell checks, generated checks, and the full frontend suite and embedded build
passed at `95b9413977752f48ffdc64a6708a9ccd061e8c12`. The frontend suite passed
1,922 tests in 128 files. Actual PostgreSQL schedule, revocation, overlap, and
rollback checks passed at `1685973304dbaae2c21cf0aebd97b6d9d759a136`. Domain and
API race checks passed at `cc3722000664d1b23356328492520b3ae5408a80` and carry
forward through unchanged package blobs. Final formatting, lint, real request
decoder, and route checks passed at `5318035`. Later changes affected fixture
assertions and diagnostics only. Protected-runtime diagnostic checks passed
before the final gate. Repository-wide `make test`, `make verify`, broad
`make check`, and unrelated product suites were not run in this schedule slice.
Retained passes apply to these unchanged dependencies, not full hosting parity.

Implementation estimates are six minutes for backend work and five for UI work,
with tests overlapping both. Product authority repair took approximately one
minute. A test fixture configuration mistake took less than one minute to repair.
The browser investigation ran approximately 10:31–10:44 UTC, and fixture
diagnostics approximately 10:44–10:46 UTC. Their command boundaries do not provide
separate active repair and waiting totals. Test windows at `95b9413` took 62
seconds for affected Go packages, 23 seconds for static checks, 33 seconds for
frontend tests and build, and 64 seconds for generated checks, concurrently.
Later backend command windows took approximately 72, 38, three, and 55 seconds
for focused and race checks, also overlapping. Final preflight took 13 seconds.
Go fixture diagnostic compilation and contract checks took 37 seconds. The
protected-runtime PostgreSQL experiment took 4.390 seconds separately. Review
and waiting time were not instrumented. These categories cannot be added into an
elapsed-time percentage.

This local schedule qualification did not cover remote transfer. The separate
[immediate remote-copy qualification](hosting-storage-remote-backups.md#accepted-compose-remote-copy)
and [scheduled remote-copy qualification](hosting-storage-remote-backups.md#compose-remote-schedule-qualification)
record their own status and scope. Local archive download is separately
[qualified at `ff6b200c`](hosting-storage-archive.md#local-download-qualification).
Compose archive restore, bind mounts,
raw Compose, Git Compose and mixed build/image stacks remain unsupported or
unqualified.
Coordinated database and upload recovery is a separate NeroCD expansion, not an
upstream workflow implemented by `VolumeBackupRecoveryJob`. No coordinated
snapshot, full parity, or automatic rollback claim follows from this qualification.

## Private registry qualification

The existing `--managed-compose` gate passed on 2026-10-27 at
`c4b9ef45b94d4628007bb9b7a8b327190788f130`:

- [x] Saved and deployed a protected, digest-pinned Vikunja image with public
  PostgreSQL and init images, using explicit credential selection in the UI.
- [x] Rejected wrong and revoked credentials before mutation, preserving exact
  previous containers and loaded routes. A later valid redeployment and Stop/Start
  retained records, uploads, saved runtime variables, and successful new writes.
- [x] Retained the existing component logs, local and remote backup/retry,
  database-only separate-target import, and held failed-replacement assertions.
- [x] Verified the served manifest and 1280/320/390/430-pixel layouts. All 41
  baseline containers and 236 volumes, and the user's instance, remained unchanged.

The gate ran from `16:25:49Z` to `16:30:04Z` in 255 seconds; the actual API test
passed in 162.57 seconds. Two earlier runs remain failed: the first exposed
Docker's hidden digest cache in the negative fixture; the second exposed a runner
loop exiting after a safely settled pre-apply failure. A never-pulled immutable
manifest with identical image content now exercises rejection, and the runner
continues only after validating and acknowledging safe completion. An existing
fixture-channel check now reports early runner exit without waiting 90 seconds.

Safe evidence, source dependencies, independent reviews, and separate overlapping
timing categories are indexed at
`~/.local/share/nerocd/acceptance/runtime-hosting-compose-registry-20261027/c4b9ef45b94d4628007bb9b7a8b327190788f130/evidence-index.json`
(SHA-256 `cd8feaec05929ed9252024441ea9aa27e52c5ebc4c250007788be9bbb63acb1d`).
This qualifies one selected registry on the existing native arm64, rootful classic
Docker profile with private fixture-CA HTTPS. Other architectures, rootless or
cross-host execution, public ACME, standalone database or single-artifact local-build
credential selection, and full Coolify registry parity remain unqualified. Earlier qualification evidence
above remains attributed to its original revisions.

## Install the saved stack from the catalog

The [Vikunja/PostgreSQL catalog entry](hosting-catalog.md#catalog-compose-qualification)
now saves the accepted example and generated runtime references directly. Its
qualified workflow deploys untouched public source first, then exercises private
replacement, persistence and the existing recovery safeguards. This extends the
catalog admission path; earlier Compose qualifications retain their own source
revisions and limits.

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

The volume-backup reference paths are `app/Jobs/VolumeBackupJob.php` and
`app/Jobs/VolumeBackupRecoveryJob.php` at the same pinned revision. Coolify discovers
running volume users, stops them when configured, archives the selected source
through a read-only helper mount, and resumes the stopped containers. Recovery
retries unfinished container resumption and partial S3 cleanup. NeroCD reuses
that sequence with its owned-volume proofs, fenced authority, durable journal,
and existing archive helper.
