<a id="hosting-catalog-contract"></a>

# Install an application from the catalog

The Linkding and Uptime Kuma catalog entries translate Coolify templates at
`b12d1386f3d458618b4c579babcd1e339c6c40b3` into an ordinary image application.
The installed application uses the existing resource, destination, deployment,
managed-secret and owned-storage APIs. Neither entry creates an automation TaskTemplate.

## Pinned definition

Linkding's immutable version is `coolify-b12d1386.linkding-1.47.0.v1`. It selects
`docker.io/sissbruecker/linkding@sha256:e35cb50e0581178f245125ffaa909c565416c94c9f22ace305a7d234a4345522`,
container port 9090, readiness `/health`, and a retained local Docker volume at
`/etc/linkding/data`. The runtime configuration references separately generated,
encrypted `LD_SUPERUSER_NAME` and `LD_SUPERUSER_PASSWORD` secrets at version one.
The original template remains byte-for-byte available in the installation copy.
It is not executed as Compose or as a shell script.

The original comes from
[Coolify's Linkding template](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/templates/compose/linkding.yaml),
licensed under [Apache License 2.0](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/LICENSE).
The typed translation replaces `latest` with a digest, maps generated values to
managed-secret references, replaces its TCP shell health check with Linkding's
HTTP database health endpoint, and uses NeroCD's owned-volume provenance. The
Coolify `SERVICE_URL_LINKDING_9090` declaration is URL metadata, not a required
Linkding environment variable. NeroCD uses its ordinary explicit route workflow.
Arbitrary or pasted Compose imports remain incomplete and are rejected.

## Uptime Kuma pinned definition

Uptime Kuma uses immutable version `coolify-b12d1386.uptime-kuma-2.5.8.v1` and image
`docker.io/louislam/uptime-kuma@sha256:20abea8c48a163cf0a7a5d5936ab418e4b742d908704e1f58f6c06e56a9c6b67`.
Its container port is 3001 and retained data mount is `/app/data`. The original
[Coolify Uptime Kuma template](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/templates/compose/uptime-kuma.yaml)
remains byte-identical in the installation. NeroCD pins upstream image tag `2`
to version 2.5.8 and uses its ordinary explicit route workflow.

The entry generates no login credentials. It saves runtime generation one with
canonical `entries: []` and empty credential arrays, so deployment still pins an
immutable runtime reference. First-run database and administrator setup remain
application behavior. The qualified workflow selects SQLite through Uptime Kuma's
visible setup screen, then creates its administrator account.

Readiness requests `/` with a 120-second timeout, the existing API maximum.
NeroCD requires a successful 2xx or 3xx response. The image's own health check
accepts any HTTP status and has a 180-second start period; that period is not the
NeroCD deployment timeout. This entry does not inject database bootstrap variables
or account credentials.

## Install and deploy

1. Select the project and shared environment under Project hosting.
2. In Install from catalog, choose a Catalog category and search by application,
   version, image, data mount or generated variable name. Search ignores case and
   requires every word to match. Both filters apply together. Use All categories,
   Clear search or Clear filters to recover an empty result, then select Linkding or Uptime Kuma.
   Review its upstream category and architecture declaration, pinned image, data
   mount, generated variables, container port, readiness check and template source.
   Enter a name and select a granted destination. Project owner access is required.
3. Install the application. The transaction saves resource placement, storage,
   any generated encrypted credentials, runtime references, attribution and the exact request.
4. Open the saved application deployment panel. Its catalog container port and
   readiness settings are prefilled. Enter the runner-local host port and review
   the first storage deployment before submitting it.
5. After the deployment succeeds, use the ordinary route controls for HTTPS
   access, or the runner-local endpoint on an explicitly authorized local target.
6. For Linkding, use Reveal login credentials to retrieve the saved username and password.
   This calls the existing managed-secret reveal operation explicitly. Values
   are never included in installation metadata or query caches.
7. For Uptime Kuma, open the deployed application, select SQLite and create its
   administrator account. NeroCD displays first-run guidance without reveal or
   hide controls because this entry has no generated login credentials.

The request identity survives an unconfirmed response. Retry uses the same
project, environment, destination, name, template and immutable version. Current
owner, destination and applicable secret authority is checked before replay. Concurrent
identical requests commit one installation. Catalog refreshes cannot replace an
installation's original bytes, hash, normalized definition or component references.
A resource may later change through the ordinary resource APIs; installation
metadata continues to describe the original install.

Linkding's `create_initial_superuser` creates a missing username and skips an
existing username. Changing the runtime password does not rotate an existing
account password. Redeployment keeps the original startup references and volume,
which includes `data/db.sqlite3` and `data/secretkey.txt`. Password changes inside
Linkding follow Linkding's account controls.

## Combined runtime and storage family

Ordinary image applications can combine saved runtime configuration and owned
storage through `public-image.owned-storage.runtime-env.release-ports.v1`, compiler
`image-hosting.owned-storage.runtime-env.v1`, and capability
`hosting_owned_storage_runtime_v1`. Runtime environment configuration on the runner
also advertises the existing storage capability. Admission still requires image,
release-port, storage and runtime-environment capabilities and current authority.

The compiler pins the exact runtime generation/hash and storage creation origin.
It permits image applications only, rejects database/local-build fields, and
sets `RetainPrevious=false` to enforce one writable-volume user. Existing
compilers continue to reject mixed fields. Storage materialization is journaled
before apply admission. Protected runtime delivery recognizes a prepared storage
hold only after exact writer materialization evidence exists. Values reach the
Docker create process through stdin and remain outside plans, audit and journal.
Recovery uses committed provenance and observations; replay never fetches values
to recreate a container. Image rollback cannot undo application writes to data.

A catalog draft validates the installation name before assigning an immutable
request ID. A definitive HTTP 400 rejection permits correction and a new request.
An ambiguous response retains the original request and disables edits so a retry
cannot silently create a second installation.

Changing or clearing either filter clears the selected application. Select an
entry again before installing. Both filters are locked while an installation is
unconfirmed, so Retry same installation retains the exact original request. If
catalog or destination loading fails, Retry catalog loading fetches the failed
list again without submitting an installation.

After a combined owned-storage/runtime-environment deployment is prepared, a
runner restart retains an explicit ownership barrier. It does not request a new
secret delivery, create storage, or recreate a container. Owner-requested
read-only reconciliation is checked before that barrier and may retire journal
evidence only when the controller returns an exact trusted terminal receipt.
The runner cannot accept unrelated work while this unresolved global startup
barrier remains. Observation does not itself release uncertain or restored
ownership; the existing trusted recovery boundary still applies.

<a id="upstream-behavior-and-remaining-scope"></a>

<a id="current-catalog-criteria"></a>

## Supported scope

The catalog contains pinned Linkding 1.47.0 and Uptime Kuma 2.5.8 entries. Each
installation keeps its original
template bytes, normalized definition, attribution, initial name and placement,
secret references, component identities, and resolved digest. Later resource
changes do not rewrite that installed copy.

Catalog category and text search filter the authorized list locally. Category
options come from the upstream comma-separated declaration. Matching splits on
commas, trims whitespace and compares the entire category without regard to case,
as in the pinned PHP
[catalog selector](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/resources/views/livewire/project/new/select.blade.php).
Linkding's upstream category is `RSS`, from
[the pinned service metadata](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/templates/service-templates.json).

The preview displays upstream `arm_only` and `amd_only` declarations as ARM only
or AMD only. Neither current entry declares a restriction, so each reads Not declared.
These declarations do not verify compatibility with the selected destination.
CPU, memory and disk requirements are also undeclared. Use the qualified runtime
profile below when choosing a destination.

Compatibility filters, a general template editor, catalog refresh and version
upgrades remain unavailable. The preview does not provide general required-variable,
domain or component-role presentation. General Compose import is unsupported.

## Back up and recover into a separate application

The [catalog backup and recovery workflow](hosting-catalog-recovery.md) pins
the healthy deployment's runtime generation/hash and reuses the exact existing
source writer. It does not fetch secret values or recreate that writer. Restore
copies exact managed-secret references into a distinct resource and volume on the
same destination. It does not copy catalog identity, routes or readiness results.

1. Open the installed application's upload backup controls, review the local-disk
   retention limit and acknowledge that writers may stop.
2. Request a backup and wait for its successful result and resumed source writer.
3. Select that backup and restore into a new named resource.
4. Wait for byte validation to clear the target hold. Open the restored application
   and review its ordinary deployment, storage and pinned runtime settings.
5. Deploy the restored application. Verify a fresh login, expected saved data and
   a new application write before relying on the recovered target.

The physical fixture completed those checks with real Linkding sessions and
bookmark writes isolated from the source. It also verified UID/GID and modes,
completion replay without duplicate helpers or materialization, and owner recovery
after an injected exact-writer resume failure. A failed resume keeps a writer
obligation and mutation hold until authorized recovery settles it. The operator
must preserve the source and follow the existing owner recovery controls.
See [the runtime checks and limits](../acceptance/runtime-hosting-catalog/README.md#final-physical-qualification).
This qualifies the pinned Linkding profile only. Local archives remain subject to
same-destination disk retention; no host-disk-loss or cross-host recovery is implied.

## Source and runtime evidence

The [catalog definition](../internal/domain/hosting_catalog.go),
[installation store](../internal/store/postgres_hosting_catalog.go), and
[catalog UI](../web/app/src/pages/HostingCatalog.tsx) define the current entries.
The [catalog runtime guide](../acceptance/runtime-hosting-catalog/README.md)
records the separate Linkding and Uptime Kuma qualifications. Linkding includes
passing backup and restored-application checks. Uptime Kuma covers install,
replacement and persistence as described below.
The retained recovery profile is pinned Linkding 1.47.0, native Linux arm64, trusted rootful
classic-store Docker/overlay2, one destination, and a private fixture CA.
Other architectures, production ACME, arbitrary applications, catalog upgrades,
Compose, cross-host recovery, and destination disk loss are unqualified.

<a id="catalog-filter-qualification"></a>

## Category filter qualification

Category filtering and upstream declarations are accepted at
`6aeac387975da5b9b76e847f3bbaeb354692e702`. Product candidate
`4a994373fa2827191de9798a23220309e8c3f9c1` adds category filtering
and upstream architecture declarations. Independent hygiene, specification and
security reviews found no issues at that exact revision. Final candidate
`6aeac387975da5b9b76e847f3bbaeb354692e702` changes only the existing PostgreSQL
fixture cleanup to remove its anonymous volume. Product and browser sources are
identical. Hygiene, specification and narrow security review of that cleanup
change found no issues. All three reviewers retained their full product reviews
and approved the exact cleanup change.

The focused domain and API catalog checks passed in 0.511 and 0.963 seconds.
They verify the real authorized list, historical serialization and unchanged
normalized definition identity. Formatting, focused Go vet and the pinned
v2.10.1 changed-code lint passed with zero findings. The full WebUI suite passed
129 files and 1,933 tests in 13.81 seconds. A fresh UI build and Go embedding build
passed. API generation, route generation and the UI artifact manifest comparison
also passed.

The existing responsive catalog metadata-install browser check passed once on
`4a994373fa2827191de9798a23220309e8c3f9c1` at 12:18:25–12:18:44 UTC, 18.9 seconds
overall and 2.0 seconds in the test. Its initial preservation check failed because
the fixture left one anonymous PostgreSQL volume. The original failure remains
part of the evidence.

An isolated check at 12:20:56.436909–12:20:57.385237 UTC reproduced the cause.
`docker rm -f` left the anonymous volume, while `docker rm -f -v` removed it and
preserved an explicitly named mount. Docker events proved ownership of the
original leaked volume. The steward removed it after confirming it was unused.
Final preservation passed with all 41 baseline containers and 236 baseline volumes
unchanged, no added containers or volumes, and the fixture port and runtime
released. The running user instance retained its identity, content and health.
The browser check was retained because the final repair changes teardown only.
It covers category and search combinations, clearing filters, an empty result,
loading recovery, metadata installation and preview widths 320, 390, 430 and
1440 pixels.

The audited durable evidence index is
`/Users/bubble/.local/share/nerocd/acceptance/catalog-hosting-ci-20261026/6aeac387975da5b9b76e847f3bbaeb354692e702/evidence-index.json`,
SHA-256 `f613aa08b289d4b7d098bfb31cfa54deeeccee50de00d02650d0c332c5097e86`.
It retains 21 safe files, including both preservation receipts and the isolated
cleanup proof. Private inventory snapshots remain excluded.

Prior runtime and recovery evidence retains its original source revision and
qualified profile. SQL, migrations, store generation, runner, runtime and command
sources are byte-identical to accepted `27a2527a4203b6fa7526efd547d8903a27057169`.
This change does not alter deployment, secret delivery, storage or recovery, so
the completed Linkding runtime workflow was not repeated. Full `make verify` and
`make check` were not run for this metadata and UI change. Per-asset served hash
verification was not run. The fresh UI build, artifact manifest comparison, Go
embedding build and actual browser check provide the retained artifact evidence.
No additional catalog entry or architecture compatibility is qualified by these
checks.

The delivery times below use UTC command timestamps where available. Estimates
overlap because backend, UI, validation and review ran in parallel.

| Activity | Time and source |
| --- | --- |
| Implementation | Backend approximately 12:10:00–12:11:37, UI approximately 85 seconds. Estimates from worker reports. |
| Product-defect repair | None reported for this change. |
| Fixture repair | Approximately 15 seconds for a synthetic search fixture collision. Anonymous-volume diagnosis and repair approximately 12:18:44–12:21:41, including checks. Worker estimates. |
| Test execution | Focused Go checks 12:16:42–12:16:57, 15 seconds. Full WebUI tests and fresh builds 12:16:42–12:17:14, 32 seconds. API generation took 1 second. The browser run took 18.9 seconds overall, including 2.0 seconds in the test. |
| Review and waiting | Initial reviews approximately 12:14–12:18, overlapping implementation and tests. Final cleanup review durations are unavailable. |

<a id="uptime-kuma-qualification"></a>

## Uptime Kuma qualification

The install, route, application setup and persistent-data workflow is accepted at
`397a8d826a97cceb37ae79f4384588b78860a8d8`. The existing catalog browser and isolated
storage fixture deploy the actual pinned application. First-run SQLite and admin
setup, a monitor against the task-owned application's healthy endpoint, and a
saved server setting all use Uptime Kuma's real UI. A fresh login after redeploy
reads the same monitor and setting and creates a new monitor. A controlled
replacement fails against an absent status-page API route, restores the previous
writer without uncertain ownership, and retains both monitors and the setting.
The volume, runtime generation and empty credential references remain unchanged.
Trusted managed HTTPS, installation response-loss retry, viewer denial and widths
1280, 320, 390 and 430 also pass without intercepted browser responses.

The qualified profile is Uptime Kuma 2.5.8 with SQLite, one destination, native
Linux arm64, trusted rootful classic-store Docker/overlay2 and a private fixture
CA. It does not qualify Kuma backup or restore, MariaDB, other architectures,
production ACME, upgrades, cross-host recovery or disk loss. Historical receipt
replay after retiring a template version has source-order and shape-test coverage,
not an end-to-end retired-receipt fixture.

Independent hygiene, specification and security reviews cover exact product
`6e75653732c1bc2d4361339be8585275068c47ea`, fixture candidate
`cafa793c9c5118e2a54734fbcce32e690b5823b1` and the repaired fixture at `397a8d826`.
Product blobs are identical across those candidates. Backend PostgreSQL checks
passed in 6.106 seconds and real HTTP checks in 1.547 seconds at source
`c009839cbb19375d743e3f446e8cb35147ef31b7`; their exact blobs match the integrated
candidate. Formatting, Go vet across `./cmd/...` and `./internal/...`, focused catalog
domain/API/store tests,
pinned changed-code lint with zero findings, shell checks, generated comparisons,
17 publication/preservation tests and the Linux tagged fixture compile passed.
The repaired envelope decoder and 13 shared phase cases passed before runtime.
The app package had no tests matching the catalog filter; the real API and
PostgreSQL checks exercise the service.

The full WebUI run at `cafa793c` recorded 1,940 passing tests and one existing
5-second timeout in the unchanged 64-variable runtime-configuration test.
It remains a failed full run. That exact test then passed alone in 1.12 seconds
without source or timeout changes. A fresh UI-to-Go build passed in 12 seconds;
the resulting asset manifest matches the served qualification assets. These
checks remain attributed to `cafa793c` because the subsequent repair changes only
the runtime test's response decoder. Unchanged runner and Compose physical evidence retains the
[accepted `26998a435` qualification](hosting-storage-remote-backups.md#compose-remote-schedule-qualification).
[Linkding runtime and recovery evidence](../acceptance/runtime-hosting-catalog/README.md#final-physical-qualification)
retains its original source attribution in the earlier evidence. Those physical
suites were not repeated; `0cd0a715` is the unchanged-code comparison base, not
a claimed runtime-test source.

The first gate at `cafa793c` failed after 129 seconds, with a 9.04-second Go test.
The fixture decoded the runtime GET envelope as its inner DTO, yielding zero
values. Production returns `{"runtime_configuration": ...}`. A deterministic
check using the production JSON serializer reproduced this mistake and verified
the corrected decoder while retaining generation-one, nonnil empty entries and
pinned-reference assertions. The exact failed response bytes were not retained.
After the definitive Go failure, the owned browser was signalled and existing
cleanup completed. The repaired gate passed at 13:22:56–13:24:41 UTC, 105 seconds
overall and 43.46 seconds in the runtime test. Both runs preserved all 41 baseline
containers and 236 volumes exactly, with no additions and unchanged user-instance
identity, content and health. Owned gate containers and volumes are absent.

The durable [evidence index](</Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-catalog-uptime-kuma-20261027/397a8d826a97cceb37ae79f4384588b78860a8d8/evidence-index.json>)
has SHA-256 `9c46b0e410350c828d3cd26cc8cbc7ac1f8c834cf2d722d98fe006523143cd6c`.
All 55 safe file hashes were audited. The failed gate, full WebUI failure,
isolated pass, setup proof and repair checks remain separate. Raw private
inventories, API logs and sessions are excluded; their hashes and original paths
are retained where applicable.

| Activity | Recorded time and limitation |
| --- | --- |
| Implementation | Integration and fixture development ran 13:03:31–13:13:26 UTC, 595 seconds. Coding and probes overlap; an exclusive implementation total was not recorded. Backend implementation is estimated at 13:00:37–approximately 13:03:00 UTC. The UI discovery/edit/test window is approximately 85 seconds, 12:59:16–13:00:41 UTC, including approximately 15 seconds of focused tests/build; commit at 13:01:08 UTC. These worker estimates overlap and do not provide exclusive editing totals. |
| Product-defect repair | None identified in this slice. |
| Fixture repair | Actual-image selector, navigation and absent-route checks, then the runtime-envelope decoder repair. Backend fixture repairs are estimated at 30 seconds for a Go JSON hash and 19 seconds for an enrollment ID, approximately 49 seconds. Acceptance repairs overlap development; exclusive elapsed totals are unavailable. The final small probe took 0.836 seconds. |
| Test execution | Go checks 48 seconds; generated comparisons 46 seconds, overlapping. Full WebUI run 31.24 seconds; isolated test 1.40 seconds; fresh build 12 seconds. Gates took 129 and 105 seconds. |
| Review and waiting | All required reviews completed at their exact revisions. Separate elapsed waiting time was not recorded; no percentages are inferred. |

Backend command starts at 13:03:34 UTC for the core PostgreSQL check and 13:05:51 UTC
for the focused HTTP retry are worker reports. The retained summary logs contain
package durations, not those wall timestamps.
