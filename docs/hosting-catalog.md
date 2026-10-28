<a id="hosting-catalog-contract"></a>

# Install an application from the catalog

The catalog translates pinned Coolify templates at
`b12d1386f3d458618b4c579babcd1e339c6c40b3` into Linkding, Uptime Kuma and Actual
Budget image applications, or saved Vikunja/PostgreSQL and WordPress/MySQL
Compose applications.
Installation uses the existing resource, destination, deployment and managed-secret
APIs. Image entries use owned storage; Compose uses its named volumes. These
entries create no automation TaskTemplate.

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

## Actual Budget pinned definition

Actual Budget uses immutable version `coolify-b12d1386.actualbudget-26.10.0.v1`,
image `docker.io/actualbudget/actual-server@sha256:24645e971da6bb1a1f953d6859e307a0b29be1e8b4130a35b3220c209b5b860c`,
container port 5006 and retained data mount `/data`. Readiness requests `/health`
with a 120-second timeout. The original
[Coolify Actual Budget template](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/templates/compose/actualbudget.yaml)
is retained unchanged in the installation.

The additive `definition.runtime_environment` contains the curated public literal
`ACTUAL_LOGIN_METHOD=password`. Installation saves that literal in canonical
runtime generation one and keeps credential arrays empty. Earlier Linkding and
Kuma definition hashes remain unchanged because absent defaults are omitted.
NeroCD generates no Actual login password. The user sets the server password in
Actual's UI, signs in, then creates a budget and account. The runtime literal
selects the login method; it is not the user's password.

## Vikunja with PostgreSQL pinned definition

`vikunja-with-postgresql` uses immutable version
`coolify-b12d1386.vikunja-2.7.1.v1` and the exact accepted
[Vikunja/PostgreSQL example](../examples/compose/vikunja-postgresql.yaml).
The installation retains the pinned upstream template separately. It saves the
Compose source at resource generation one and allocates no image-family storage.
The deployment uses the existing saved Compose review and named-volume lifecycle.

Enter Public URL before installing. Runtime generation one contains that literal
URL, fixed database user/name `vikunja`, and two generated managed-secret references
for the PostgreSQL password and JWT secret. Installation metadata contains only
references, and the UI offers no login-credential reveal. Register an account in
Vikunja after deployment. Public URL creates neither a route nor a certificate.
The saved review prefills container port 3456 and readiness `/health`, 120 seconds.

## WordPress with MySQL pinned definition

`wordpress-with-mysql` uses immutable version
`coolify-b12d1386.wordpress-7.1.3-mysql-8.4.13.v1`. The translated
[pinned upstream template](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/templates/compose/wordpress-with-mysql.yaml)
saves WordPress 7.1.3 and private MySQL 8.4.13 as managed Compose services.
The public component is `wordpress`, container port 80, with readiness `/` and a
120-second timeout. Named volumes retain `/var/www/html` and `/var/lib/mysql`.

Enter the intended HTTPS Public URL during installation. The template stores it
as `SERVICE_URL_WORDPRESS`; installation does not create a route or certificate.
Generated `SERVICE_USER_WORDPRESS`, `SERVICE_PASSWORD_WORDPRESS`, and
`SERVICE_PASSWORD_ROOT` references configure database access. They are not
WordPress administrator credentials. After deployment, add the matching HTTPS
route and complete WordPress's own site and administrator setup in the application.

The MySQL service belongs to this Compose stack. Native MySQL resource backup
controls do not apply to it. WordPress backup, restore, upgrades, and migration
remain outside this template's qualified workflow.

## Install and deploy

1. Select the project and shared environment under Project hosting.
2. In Install from catalog, choose a Catalog category and search by application,
   version, image, data mount or generated variable name. Search ignores case and
   requires every word to match. Both filters apply together. Use All categories,
   Clear search or Clear filters to recover an empty result, then select an application.
   Review its upstream category and architecture declaration, pinned image, data
   mount, generated variables, container port, readiness check and template source.
   Review Application requirements and the reasons beside each destination.
   Only granted destinations with the required runtime and enabled runner features
   can be selected. Refresh destination compatibility rechecks the list and clears
   the selection without installing anything. Enter a name and select a destination.
   For a Compose entry,
   also enter Public URL. Project owner access is required.
3. Install the application. The first-install transaction rechecks destination
   eligibility before creating anything. A stale eligible preview can be rejected.
   The transaction saves resource placement, applicable storage,
   any generated encrypted credentials, runtime references, attribution and the exact request.
4. Open the saved application deployment panel. Its catalog container port and
   readiness settings are prefilled. Enter the runner-local host port and review
   the first storage deployment before submitting it. For a Compose entry, use
   Review current Compose deployment and acknowledge the interruption.
5. After the deployment succeeds, use the ordinary route controls for HTTPS
   access, or the runner-local endpoint on an explicitly authorized local target.
6. For Linkding, use Reveal login credentials to retrieve the saved username and password.
   This calls the existing managed-secret reveal operation explicitly. Values
   are never included in installation metadata or query caches.
7. For Uptime Kuma, open the deployed application, select SQLite and create its
   administrator account. NeroCD displays first-run guidance without reveal or
   hide controls because this entry has no generated login credentials.
8. For Actual Budget, set its server password, then sign in with that password.
   Select Start budgeting and create the budget and account in Actual. Complete
   server sync before relying on server-side persistence. A local browser copy
   alone does not prove that the server retains the budget.

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

The catalog contains pinned Linkding 1.47.0, Uptime Kuma 2.5.8 and Actual Budget
26.10.0 image entries, plus Vikunja 2.7.1 with PostgreSQL and WordPress 7.1.3
with MySQL 8.4.13 as saved Compose. Each
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
or AMD only. None of these entries declares a restriction, so each reads Not declared.
The selected-template requirements show runtime, required server features,
persistent mounts, Public URL needs and generated variables. The destination list
checks granted placement, active enrolled runner authority and required features.
Unavailable choices are disabled with a readable reason. This metadata does not
verify destination architecture, CPU, memory or disk capacity. Upstream hardware
minimums remain undeclared for these entries.

The behavioral reference is pinned PHP `b12d1386`:
[`Select.php`](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Livewire/Project/New/Select.php#L413-L459)
filters eligible service hosts and selects their Docker destinations, while
[`Server::canHostResources`](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Models/Server.php#L609-L612)
checks the effective deployment role. NeroCD's grant, current credential and
capability checks, atomic admission, and receipt replay adapt that workflow to
Go authorization and persistence. Neither path is a hardware probe.

An identical authorized retry returns the existing installation even if current
feature eligibility changed. Current owner and destination grant checks still
apply. A retry creates no new resource or secret. General template editing,
catalog refresh, upgrades, component-role presentation and general Compose import
remain unavailable.

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
records the separate Linkding, Uptime Kuma, Actual Budget and Compose qualifications. Linkding includes
passing backup and restored-application checks. Uptime Kuma covers install,
replacement and persistence as described below.
The retained recovery profile is pinned Linkding 1.47.0, native Linux arm64, trusted rootful
classic-store Docker/overlay2, one destination, and a private fixture CA.
Other architectures, production ACME, arbitrary applications, catalog upgrades,
general Compose recovery, cross-host recovery, and destination disk loss are unqualified.

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

<a id="actual-budget-qualification"></a>

## Actual Budget qualification

Actual Budget is accepted at `ca3b8dcdd430129b8bc2c3f56aa7bf1c3c3d04f4`. The
connected catalog workflow installs, retries and deploys the pinned app, verifies
exact literal runtime generation one, empty credentials and trusted HTTPS, then
sets the server password and signs in through Actual's UI. It creates a budget,
account and transaction and waits for completed server sync. After redeploy, a
completely new browser context downloads the server budget, reads the transaction
and creates another write. Another fresh context reads both writes after a
controlled failed replacement restores the previous writer without uncertain
ownership. The account UUID and volume/runtime references remain unchanged.
IndexedDB or a reused context cannot supply these persistence results.

The qualified profile is Actual Budget 26.10.0, one trusted rootful classic-store
Docker/overlay2 destination, native Linux arm64 and private fixture HTTPS.
Browser cross-origin isolation and SharedArrayBuffer checks pass; Go independently
verifies the trusted certificate chain. Unauthenticated protected `/sync/...`
returns 401 for controlled failed readiness; missing UI paths return the SPA with
200. Actual backup/restore, upgrades, cross-host recovery, other architectures and
production ACME remain outside this supported profile. Prior Linkding and Kuma
evidence keeps its original source and limits; neither physical suite was repeated.

Independent hygiene, specification and security reviews cover product `179cacbd`,
fixture `8d9f3a53` and exact three-line repair `ca3b8dcd`. Retired-version receipt
replay retains source-order/shape-test coverage rather than an end-to-end fixture.
At `8d9f3a53`, formatting, vet across `./cmd/...` and `./internal/...`, focused
catalog/runtime domain/API/store tests, pinned lint, shell/generated checks,
Linux fixture compilation, UUID phase and publication checks passed. The app
package had no matching named tests; real API/PG checks exercise the service.
WebUI passed 129 files/1,942 tests in 10.62 seconds, followed by a fresh UI-to-Go
build and manifest. Those checks remain valid: the repair changes only three
browser lines. Backend `8578646f` and UI `ffca6abc` blobs match the integration.
The original backend command remains failed due to a test-only undefined constant,
while domain and PostgreSQL checks passed; corrected actual HTTP checks then passed.
No product-defect repair was required.

The first gate at `8d9f3a53` failed after 100 seconds in `app_create`; its receipt
lacks the exact action. The complete initial source sequence reproduced a timeout
waiting for Start budgeting: password bootstrap returns to Sign in. The fixture
repair performs that sign-in, then the complete prefix and two fresh-context sync
reads passed in 6.008 seconds before qualification. The corrected gate passed
14:13:37–14:14:56 UTC, 79 seconds overall and 31.81 seconds in the runtime test.
Both gates preserve all 41 containers/236 volumes exactly, leave no additions or
owned resources, and preserve user-instance identity, content and health.

The durable [evidence index](</Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-catalog-actual-budget-20261027/ca3b8dcdd430129b8bc2c3f56aa7bf1c3c3d04f4/evidence-index.json>)
has SHA-256 `22d0a9c62624dff5204fd7e9ea3ff25af5ff7f7c73b8851af64104f0532fe20b`
and 55 audited safe files. It separates the backend compile failure, failed gate,
complete before/after probes and passing run. Private snapshots, raw interactive
output, full sessions, credentials and temporary keys are excluded.

The index records implementation, tiny-fixture development, fixture repair,
tests and review/waiting separately. Integration development/probes span
13:48:07–13:59:59 UTC, 712 seconds, with no exclusive split. Backend implementation
13:47:22–13:48:27 and roughly one minute of UI implementation are worker estimates;
validation/schema waiting overlaps. Bootstrap probes took 31.149 seconds before
repair, 1.299 seconds to confirm sign-in and 6.008 seconds after repair. Detailed
command windows remain in the index. Exclusive repair and review/wait durations
were not measured; no percentages are inferred.

<a id="catalog-compose-qualification"></a>

## Catalog Compose qualification

The catalog-to-running-application workflow is accepted at
`eeee82b6cf9350949e01121d59ec736e336dc734` on the existing native arm64,
rootful classic Docker profile with trusted fixture-CA HTTPS.

- [x] Installed through the catalog UI, retried an unconfirmed response with the
  identical request, and deployed the untouched public source and five saved runtime entries.
- [x] Verified catalog preview, Public URL and deployment controls at 1280, 320,
  390 and 430 pixels, with the same 75 assets in the binary and served manifest.
- [x] Routed the real app, created a task and upload, then retained both across
  private-image replacement and Stop/Start with fresh-login reads and new writes.
- [x] Preserved the existing wrong/revoked credential, log-follow, local/remote
  archive retry, database-only import and controlled failed-replacement assertions.
  All 41 baseline containers, 236 volumes and the user instance remained unchanged.

The first gate failed after 167 seconds at a visibility-gated log-follow control.
A focused test reproduced the offscreen actionability mechanism; the exact failed
component was not retained. The driver now scrolls the actual button and waits for
it to become enabled. The corrected gate passed in 246 seconds, including the
159.53-second API test. The two gates total 413 seconds; API time is a subset.
Earlier cheap checks also corrected source-version insertion and a late catalog
readiness race before qualification. Detailed overlapping timings and provenance,
including unavailable backend test timestamps and compilation snapshots, remain
in the safe [evidence index](</Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-catalog-compose-20261027/eeee82b6cf9350949e01121d59ec736e336dc734/evidence-index.json>),
SHA-256 `a906733fa2835ce745b79343173cf81f1205b54a95d77938dd2a3163105ddce1`.

Full WebUI evidence is 1,976 passing tests plus the affected 74-test run, not a
new full 1,979-test run. All three final independent reviews are clean. Prior
private initial-pull evidence remains at `c4b9ef45`; this run starts with public
catalog source and qualifies later private replacement and Start. General templates,
upgrades, other architectures, cross-host recovery and production ACME remain
unqualified. The existing retired-version receipt coverage limitation remains.

## Destination requirements qualification

Product source `64ee1ed2e43c568412fbbcbc39b064cb9e1318e4` and final test source
`985652b9728c3e0aaab4f9d3f12aeff9d68088c3` are accepted for the bounded
catalog selection and installation workflow.

- [x] Real HTTP/PostgreSQL tests check grant-scoped options, unavailable reasons,
  pagination, current eligibility before first-install side effects and authorized replay.
- [x] The Go-served browser checks readable requirements, disabled and valid
  choices, no blocked install request, and exact committed-response-loss retry
  with persisted resource readback at 320, 390, 430 and 1440 pixels.
- [x] All 74 served payloads match the 75-file build manifest, excluding the
  nonserved `.gitkeep`. The 320 and 1440 screenshots were visually inspected.
- [x] Existing setup cleanup preserves all 41 containers, 236 volumes and the
  user's process, content and health. No application deployment was rerun.

The accepted browser command passed on 2026-10-27, 19:32:25–19:32:44 UTC,
19 seconds wall time, with a 2.4-second test. Two earlier fixture runs each took
17 seconds: one omitted the existing server-admission capability, and one used
an unregistered receipt readback URL. The focused shared-fixture audit corrected
both contracts and moved asset verification inside the live test. A separate
mixed-reason UI decoder defect was repaired with focused response-shape tests.
Full WebUI evidence is 134 files and 2,068 tests at the unchanged product source.
All three exact final reviews passed.

The audited [evidence index](/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-catalog-compatibility-20261027/985652b9728c3e0aaab4f9d3f12aeff9d68088c3/evidence-index.json)
has SHA-256 `ce1e56e94aa2786b7dfa2437284f8824c0a548cbc791ebfbfb0e6fe3ec69a43c`
and 26 safe files. It links both failed receipts and separates implementation,
product repair, fixture repair, tests and review/waiting. Known windows overlap;
exclusive implementation and review totals are missing.

This test uses a real controller/PostgreSQL and enrolled runner records. It does
not establish physical runner execution, architecture or available capacity.
Accepted deployment/data evidence at `ff6b200c` retains its original scope because
runner, engine, Compose and pinned template dependencies are unchanged. General
templates, upgrades and broader application recovery remain outside this slice.


## WordPress qualification

At `9fa6da10` on 2026-10-28, the catalog UI installed WordPress/MySQL, retried a
committed response loss with the identical request, and deployed the saved stack.
The real WordPress installer created the administrator. Fresh authenticated
sessions published posts and uploaded a PNG through managed, fixture-CA HTTPS.
Independent HTTPS reads verified the published post and exact uploaded bytes.

Redeployment after runner restart and UI Stop/Start retained the same volumes,
runtime references, posts, and media, with new authenticated writes afterward.
An unavailable image failed before replacement and preserved the serving stack.
A failure after mutation retained reconciliation ownership, the same volumes,
and no competing writers. Direct checks found the database record and exact PNG on the
held candidate. Competing deployment was denied, and read-only reconciliation
kept the hold. This is unresolved service state, not automatic rollback or proof
that public service recovered. Replacing an image cannot roll back database or
media writes.

The [evidence index](/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-catalog-wordpress-20261028/9fa6da1053329a5fe7cfb675b8ba67e970181a2d/evidence-index.json)
records source and artifact checks, responsive hosting controls at 320, 390, 430,
and 1280 pixels, credential scans, and verified cleanup. Runtime proof covers one
Linux arm64 destination and test-CA HTTPS. It does not establish other
architectures, public ACME, WordPress backup, catalog upgrades, plugin/theme
administration, cross-host recovery, or automatic data rollback. Earlier catalog
qualifications retain their own scope.
