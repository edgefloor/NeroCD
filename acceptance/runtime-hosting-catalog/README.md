# Linkding catalog runtime acceptance

Run `bash acceptance/runtime-hosting-catalog/gate.sh` from the assigned clean,
committed checkout only after approval for that candidate and this exact target.
The wrapper reuses `runtime-hosting-storage/gate.sh --catalog` and its inventory,
ownership, process joining, rollback and cleanup guards. Source compilation is
safe before launch. A compiled fixture is not a passed runtime test.

The physical target is `unix:///var/run/docker.sock`, daemon
`e37dd219-b13f-47dd-99ed-21d79442ed0c`, Linux aarch64. The outer owner is
`nerocd-catalog-linkding-runtime-20261026`, container suffix `-dind`, sole named
volume suffix `-docker-data`, label `io.nerocd.storage-runtime.owner`. It uses
`docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489`,
four CPUs, 6 GiB memory, 2048 PIDs, and the existing privileged DinD profile.
The outer container has no host bind or Docker socket mount. It publishes only
API port 18190 to one dynamically assigned `127.0.0.1` host port for the connected
browser. The gate verifies the exact requested and assigned binding before use
and before cleanup; no application port is published on the host. Existing host containers, volumes and the user UI remain outside this target.

Inside that daemon, the established PostgreSQL fixture uses
`postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94`,
one CPU, 1536 MiB memory, 128 PIDs, a 1 GiB data tmpfs, WAL targets 128/32 MiB,
and default durability. The existing pinned registry, Traefik, Pebble and local
storage helper images remain prerequisites of the reused fixture, although the
original install/redeploy receipt below exercises a managed HTTPS application
route without backup/delete machinery. The later reviewed extension adds backup
and new-target restore coverage qualified by its separate final receipt below.
The new exact image pull is the Linkding digest in [the catalog contract](../../docs/hosting-catalog.md).
All application mutations occur inside the owned guest. Guest API TLS uses the
existing fixture CA. The compiled runner uses the actual public API and journal.

The connected Playwright browser loads the candidate Go-served UI and compares
all served asset hashes with the candidate build manifest. It signs in, selects
the catalog entry, checks desktop and 320/390/430 px layouts, installs, observes a
server-side lost-response fault after the real commit, and retries identical
request bytes. It explicitly reveals and hides credentials, deploys through the
ordinary deployment review, checks the recorded successful readiness result,
and verifies a fresh viewer session cannot install or replay the request. It
selects the existing managed route to the settled Linkding release. No browser
responses are intercepted and no credential values or DOM are recorded.

`TestHostingCatalogLinkdingCompiledRunnerHTTPS` sequences that browser against
the real candidate API and runner. It separately verifies the managed route at
`https://catalog-runtime.example.test/health` with the fixture CA and version
1.47.0, preserving the route across redeploy and failed-candidate recovery. This
application HTTPS evidence is distinct from browser-to-API TLS. It also checks
runner-local `/health`, submits Linkding's real login form,
and saves a bookmark using its session-authenticated API. The bare query flags
are valid [presence-based switches](https://github.com/sissbruecker/linkding/blob/24b5ad6cc9bde497b5d1b1e86aed5a2fb7d25c2f/bookmarks/api/routes.py#L67-L75).
They suppress only the documented metadata-scraping and automatic HTML-snapshot
paths. Bookmark creation still [queues archive, favicon and preview work](https://github.com/sissbruecker/linkding/blob/24b5ad6cc9bde497b5d1b1e86aed5a2fb7d25c2f/bookmarks/services/bookmarks.py#L36-L47)
according to Linkding's existing settings; the flags do not guarantee absence of
background network requests. The fixture uses the pinned version's
[`ld_csrftoken` cookie](https://github.com/sissbruecker/linkding/blob/24b5ad6cc9bde497b5d1b1e86aed5a2fb7d25c2f/bookmarks/settings/base.py#L110-L112)
for session-authenticated API requests.
It starts a fresh cookie jar after runner restart/redeploy and after a failed
candidate restores the predecessor. It verifies bookmark data, volume origin,
secret IDs, versions and values, original template copy, and absence of plaintext
in operation metadata, runner logs and journal. The local unit, API and PostgreSQL
checks separately cover malformed requests, concurrent installation, permission
and destination denial, and revoked replay.

The original bookmark transport failure remains unexplained because its
underlying error was not retained. Correcting CSRF handling does not establish
that failure's cause. Every login, bookmark and health request records a fixed
operation, error class, elapsed milliseconds and numeric HTTP status. Connection,
reuse, write and first-response counters include automatic redirect requests.
Diagnostics omit error text, URLs, cookies and response bodies.

The run at `636acbdd3512214cf617767157eda17ccca6a0ca` completed fresh login,
then failed the first bookmark POST immediately on a reused connection after
writing the request and before receiving a response byte. The application stayed
running with unchanged workers and no OOM or restart. The session client now
clones the standard transport privately and disables keep-alive reuse; it keeps
proxy selection, cookie jars, redirects and the ten-second timeout. It does not
retry POSTs. This is fixture compatibility with that observed connection failure,
not proof of a universal application defect. The pinned
[bootstrap](https://github.com/sissbruecker/linkding/blob/24b5ad6cc9bde497b5d1b1e86aed5a2fb7d25c2f/bootstrap.sh#L36)
starts uWSGI's HTTP server with the pinned
[configuration](https://github.com/sissbruecker/linkding/blob/24b5ad6cc9bde497b5d1b1e86aed5a2fb7d25c2f/uwsgi.ini),
which has no explicit HTTP keep-alive setting. The exact underlying transport
error remains unknown. The later passing run below qualifies persistence only
for the stated fixture profile.

After each settled deployment and before older cleanup callbacks, the fixture
reads the exact owned application's state, exit/OOM/restart counters and admitted
CPU/memory/PID limits. It also records numeric uWSGI PIDs and allowlisted cgroup
OOM counters. Each of the three diagnostic Docker commands has a five-second
bound; raw inspect, environment, application logs and process arguments are not
recorded.

The gate must report the exact test as passed with no skip. Reviewers must still
assess `code_hygiene`, `specification` and `critical_security` at the exact SHA.
The retained passing runtime and browser evidence is recorded below.
The browser writes only fixed boolean markers, viewport widths, a fixed phase
and an error class to `catalog-browser-safe.json`; the gate requires every marker
and the exact runtime PASS with no skip. Compilation and syntax checks do not
establish that these physical assertions pass.

## Qualification evidence

The physical gate passed at source `3b0d9bbc5bd4159379d58aabe412f55f71bf147e`,
tree `6b03a4905580b99a69968b1bb2589045206fb371`, on 2026-10-26.
The command exited zero after 213.639 seconds. The exact
`TestHostingCatalogLinkdingCompiledRunnerHTTPS` passed in 36.13 seconds with no
skip. The browser markers all passed, including the candidate served-asset hashes,
1280/320/390/430 px layouts, identical lost-response retry, credential reveal/hide,
ordinary deployment, viewer replay denial and managed-route selection.
Fresh application sessions verified login and bookmark persistence after redeploy
and failed-candidate predecessor recovery. The runner-local endpoint and managed
HTTPS route passed separately with the private fixture CA.

Evidence is retained under `/Users/bubble/.codex/validation-evidence/catalog-final-runtime-20261026-3b0d9bbc`.
`handoff.json` has SHA-256
`6d104ea7fce079d399979d59579af63ff320b40dba6a5cc8f476ebb07e0e5e4d`.
The directory includes `SHA256-manifest.json`, `physical-gate-result.json`,
`runtime-receipt/api-tests.log`, `runtime-receipt/catalog-browser-safe.json`,
`runtime-receipt/preservation-safe.json` and a nested receipt manifest.
The preservation checks found no task-owned resources/processes remaining and
retained the existing 39 containers, 219 volumes and user UI.
`make web-policy` passed in 1.259 seconds after correcting a missing Node PATH.
The Docker build passed in 18.354 seconds. The original PATH failure remains in
the evidence. The original full PostgreSQL run at this source later finished
with 567 passing and three failing parent tests. It is not a passing full-suite
receipt. Repairs and their bounded validation are recorded below; no full-suite
pass is inferred.

The documentation base `bdef719182ea6284ae9a3bbbb45f7c15fa536797` differs from
that source only in `.github/workflows/check.yml`, increasing the CI job timeout
from 30 to 90 minutes. Product sources, dependencies, migrations, frontend and
embedded assets, and acceptance fixtures are identical. Retained runtime evidence
stays bound to its original source SHA.

This evidence covers one Linkding 1.47.0 entry on native Linux arm64 with rootful
classic-store Docker/overlay2. It uses a private fixture CA, not production ACME.
Failed-deployment recovery preserves existing data. Backup/new-target restore,
catalog upgrade, broader templates and general Compose remain unqualified at
that original source. The later backup/restore pass is separate below.

## Archive extension and qualification attempts

Earlier candidate `bdc8d031150f421d8cbad4b18ef6c531c15fa55f` is a source checkpoint for the
[catalog backup and recovery contract](../../docs/hosting-catalog-recovery.md).
The Leader reported passing `code_hygiene`, `specification`, and
`critical_security` reviews at that candidate. Selected real PostgreSQL execution finished with seven of ten parent tests
passing, three failing and no skips. The physical gate was not run. This candidate
is unqualified; source review and fixture compilation establish no physical pass.

That candidate extends `TestHostingCatalogLinkdingCompiledRunnerHTTPS` and its
connected browser to request a backup, restore into a distinct resource/volume,
and deploy the restored application through ordinary controls. The fixture is
intended to verify the backup-pinned runtime document despite later desired
configuration changes, exact secret references, fresh login, archived bookmark
reads, isolated new writes, ownership/modes, completion replay, exact existing
writer reuse, and owner recovery after an injected writer-start failure.
The browser also checks restore controls at 1280/320/390/430 px. At that earlier checkpoint these were proposed
assertions without an executed passing receipt. The final pass below supplies
separate exact-source evidence.
The earlier 3b0d9bbc receipt supplies none of this backup/restore evidence.

The later physical attempt at `d248e865a493b959a11d74faa6fb5e638b4db3fd`,
tree `948189c37adcd3d905f273abb689260611a62f17`, exited 1 after
104.705 seconds. Its browser receipt confirms served-asset matching, install/retry,
credential controls, ordinary deployment, viewer denial, route selection, backup
and restore requests. It records `restore_deploy` as the failed phase with
`TimeoutError`; no restored-deployment success marker exists. The run reached
source writer resume/read-write checks and separate archive extraction, but this
partial progression is not complete application recovery qualification. The later backend
deployment-options repair and final exact-candidate qualification are recorded
below.

The failed attempt is retained at
`/Users/bubble/.codex/validation-evidence/catalog-readiness-runtime-20261026`.
`physical-gate-result.json` binds the source/tree, exit and elapsed time.
`physical-receipt-sha256.json` retains full-file hashes, including
`physical-receipt/catalog-browser-safe.json` with SHA-256
`cdf8ab8d97e4d20cd44c14948322aaa61a9a9a98060a8280e75cd2276194cf95`.
Earlier passing installation evidence remains bound to 3b0d9bbc; neither it nor
this failed extended attempt establishes a passing final recovery workflow.

## Final physical qualification

The extended workflow is physically qualified at
`dd19157ce2e454b2ff6ad6c2d74b028a7dda9c86`, tree
`ab97ec210e15b2615cfc61329387f6b48ba9d1a7`. On 2026-10-26 its physical gate
exited zero after 119.823 seconds, using original evidence directory
`/tmp/nerocd-storage-runtime-20261024.IqYciv`.
`TestHostingCatalogLinkdingCompiledRunnerHTTPS` passed in 54.45 seconds with no
skip. All browser markers passed, including served-asset matching,
1280/320/390/430 px layouts, identical install retry, explicit credential controls,
ordinary deployment, viewer denial, route selection, backup, restore and restored
deployment. No browser requests were intercepted.

The compiled API/runner fixture verified actual fresh restored login, archived
bookmark reads and isolated new writes; backup-pinned runtime despite later
source desired-configuration changes; exact secret references; separate restored
volume; ownership/modes; exact source writer reuse; completion replay; and
failed-resume owner recovery. Protected container/volume inventories and the
existing user UI were unchanged. Task-owned container/volume cleanup completed.
The Leader confirmed `code_hygiene`, `specification` and `critical_security`
reviews passed at this exact source and approved it for integration. Combined
staging checks and final integrated acceptance remain separate.

Durable evidence is under
`/Users/bubble/.codex/validation-evidence/catalog-final-dd19157c-20261026`.
`SHA256-manifest.json` has SHA-256
`d71fe32f9f7cdb5636f317b45be22de2369d9ac287b0593cce702f6aefb43fde`;
all 31 listed files matched it. It includes `physical-gate-result.json`,
`physical-receipt/api-tests.log`, `physical-receipt/catalog-browser-safe.json`,
`physical-preservation.json`, `candidate-identity.json` and
`retained-source-proof.json`. The browser receipt SHA-256 is
`e7908ee9da1bf616f49bc45c3f29465b380dcd416dcaf50074426d98575c0240`.

Retained checks at `6f0e3aae81efa9b9c3c68b06398d54daf42741d0` are in
`/Users/bubble/.codex/validation-evidence/catalog-utc-restore-6f0e3aae-20261026`.
Selected real PostgreSQL execution passed 22 groups with zero failures/skips in
101.500 seconds ordinarily and 122.832 seconds under the race detector.
The 35 focused web tests passed in 1.689 seconds and the web build in 5.099 seconds.
These are selected checks, not a full product-suite pass. Git comparison from
6f0e3aae to dd19157c changes only
`internal/api/hosting_catalog_runtime_integration_test.go`. Production Go, SQL/SQLC,
store tests, frontend, archive helper, browser and gate sources are identical,
which preserves those selected check dependencies. The final changed API fixture
was executed physically at dd19157c. Documentation closure changes only its five
prose files and does not requalify unrelated receipts.

Qualification is limited to pinned Linkding 1.47.0, native Linux arm64, trusted
rootful Docker classic image store/overlay2, one destination and the private
fixture CA. It does not qualify production ACME, arbitrary applications or
Compose, broader catalog versions, cross-host/in-place/disk-loss recovery or
full hosting parity. Earlier failures above retain their original revisions and
results. The frozen consolidated audit cutoff remains unchanged.
