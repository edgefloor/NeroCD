# Test catalog application workflows

The existing storage fixture installs pinned Linkding 1.47.0, Uptime Kuma 2.5.8 or Actual Budget 26.10.0
through the public API and connected browser. The existing `--managed-compose`
profile also installs Vikunja with PostgreSQL from the catalog. These modes deploy with the compiled
runner and verify real application data. Linkding uses `--catalog`; Uptime Kuma
uses `--uptime-kuma`; Actual Budget uses `--actual-budget` on the same [storage gate](../runtime-hosting-storage/README.md).

## Run the gate

Run from a clean committed checkout after approval for the exact candidate and
Docker target:

```sh
bash acceptance/runtime-hosting-catalog/gate.sh
```

The wrapper selects `TestHostingCatalogLinkdingCompiledRunnerHTTPS`. A pass
requires the exact test result without a skip and every required browser marker.
Compilation does not establish a runtime pass.

For Uptime Kuma, use the existing gate directly:

```sh
bash acceptance/runtime-hosting-storage/gate.sh --uptime-kuma
```

This selects `TestHostingCatalogUptimeKumaCompiledRunnerHTTPS` and the same
catalog browser driver, ownership guards and preservation checks. It does not
run the completed Linkding workflow.

## Target and prerequisites

The outer target is `unix:///var/run/docker.sock`, daemon
`e37dd219-b13f-47dd-99ed-21d79442ed0c`, Linux aarch64. The Linkding owner is
`nerocd-catalog-linkding-runtime-20261026`; Uptime Kuma uses
`nerocd-catalog-uptime-kuma-runtime-20261027`; Actual Budget uses
`nerocd-catalog-actual-budget-runtime-20261027`. Each container ends in `-dind` and its
sole data volume ends in `-docker-data`. The owner label is
`io.nerocd.storage-runtime.owner`.

The gate uses
`docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489`
with four CPUs, 6 GiB memory, and 2048 PIDs. The privileged guest has no host bind
or Docker socket mount. API port 18190 is published to a dynamic host
`127.0.0.1` port for the browser. The gate verifies the requested and assigned
binding before use and cleanup. Linkding publishes no host application port. The Uptime Kuma and Actual Budget profiles additionally
publish guest HTTPS port 443 at `127.0.0.1:25443`; exact owner, container name
and bindings are checked, including rejection of foreign owners or extra ports.

PostgreSQL, registry, Traefik, Pebble, and the prepared archive helper are
prerequisites of the reused storage fixture. The exact Linkding image and runtime
settings are in the [catalog definition](../../docs/hosting-catalog.md#pinned-definition).
All application changes occur inside the owned guest. API and application HTTPS
use the fixture CA.

## Linkding assertions

The [runtime test](../../internal/api/hosting_catalog_runtime_integration_test.go)
and connected browser verify:

- Candidate served assets match the build manifest. Layouts fit 1280, 320, 390,
  and 430 pixel widths without intercepted browser responses.
- Installation survives a lost committed response through an identical retry.
  Credential reveal/hide is explicit, ordinary deployment succeeds, and a fresh
  viewer cannot install or replay the request.
- Runner-local health and the managed HTTPS route pass separately. Fresh Linkding
  sessions can log in and read the saved bookmark after redeploy and failed
  candidate recovery. The original volume and secret references remain unchanged.
- Backup uses the exact source writer. Restore creates a distinct resource and
  volume, retains the backup-pinned runtime document despite later desired changes,
  and uses the exact managed-secret references. A fresh restored session reads
  archived bookmarks and makes writes isolated from the source.
- UID/GID and modes survive restore. Completion replay creates no duplicate helper
  or volume. An injected writer-start failure retains its hold until owner recovery.

Linkding bookmark query flags suppress metadata scraping and automatic HTML
snapshots. They do not prevent all background network work. The client uses the
pinned `ld_csrftoken` cookie and fresh cookie jars after replacements. Its private
HTTP transport disables connection reuse to avoid an observed reused-connection
failure; it does not retry POSTs. That failure's underlying transport cause was
not retained.

Diagnostics record fixed operation/error classes, elapsed time, numeric HTTP
status, bounded container/process counters, and boolean assertions. They omit
URLs, cookies, response bodies, credential values, raw environments, and logs.
Cleanup verifies owned identities and preserves unrelated containers, volumes,
and the existing user UI.

<a id="linkding-catalog-runtime-acceptance"></a>
<a id="qualification-evidence"></a>
<a id="archive-extension-and-qualification-attempts"></a>
<a id="final-physical-qualification"></a>

## Tests and supported environments

The complete install, backup, and new-target recovery workflow has passed on
pinned Linkding 1.47.0, native Linux arm64, trusted rootful classic-store
Docker/overlay2, one destination, and a private fixture CA. The [catalog recovery
guide](../../docs/hosting-catalog-recovery.md) describes the runtime and secret
reference rules.

This does not qualify production ACME, arbitrary applications or Compose,
catalog version upgrades, other architectures, in-place restore, cross-host
recovery, or destination disk loss. Retained installation-only runs do not supply
backup/restore evidence; the complete fixture must pass for a new candidate.

## Uptime Kuma assertions and accepted profile

The [Kuma runtime test](../../internal/api/hosting_catalog_uptime_kuma_runtime_integration_test.go)
reuses the existing catalog setup, handshake, route and failed-runner join helpers.
The shared browser installs the credential-free entry, retries an identical lost
response and verifies viewer denial. It selects SQLite through the visible
first-run screen and creates an administrator. A monitor checks the task-owned
application endpoint; a server setting is saved through the real application UI.
Fresh login after redeploy retains both, allows another monitor write and proves
retention again after a controlled failed replacement. Trusted managed HTTPS,
stable volume/runtime references, restored ownership, responsive layouts and exact
served asset hashes are required.

The accepted source is `397a8d826a97cceb37ae79f4384588b78860a8d8`. The repaired gate
passed in 105 seconds, including a 43.46-second runtime test. Its first 129-second
attempt remains failed because the fixture decoded a runtime response without its
required JSON envelope. Deterministic decoder checks precede the repaired run.
Both runs preserve all 41 baseline containers and 236 volumes, with no additions,
and leave the user instance unchanged. The exact failed response was not retained.

The qualified profile is pinned Uptime Kuma 2.5.8, SQLite, `/app/data`, port 3001,
root readiness with a 120-second timeout, native Linux arm64, trusted rootful
classic-store Docker/overlay2, one destination and a private fixture CA.
NeroCD accepts 2xx/3xx root responses; the image health check accepts any status.
Kuma backup/restore, MariaDB, other architectures and production ACME remain
unqualified. The Linkding recovery checks above do not supply Kuma recovery proof.

[Uptime Kuma qualification](../../docs/hosting-catalog.md#uptime-kuma-qualification)
records the durable evidence index and hash, retained source/check dependencies,
the original full WebUI timeout and isolated pass, review limits and delivery
timings. Private snapshots, sessions and credentials are excluded from safe evidence.

## Actual Budget assertions and accepted profile

After exact-candidate approval, use the same gate:

```sh
bash acceptance/runtime-hosting-storage/gate.sh --actual-budget
```

The [Actual runtime test](../../internal/api/hosting_catalog_actual_budget_runtime_integration_test.go)
reuses the shared catalog lifecycle and browser. It verifies exact
`ACTUAL_LOGIN_METHOD=password`, empty credentials, trusted HTTPS and stable
volume/runtime identity. Actual's UI sets the server password, signs in, creates
a budget/account/transaction and completes server sync. New contexts download the
server budget after redeploy and failed replacement, read old data and make new
writes. IndexedDB is not carried across those reads.

Source `ca3b8dcdd430129b8bc2c3f56aa7bf1c3c3d04f4` passed in 79 seconds, including
31.81 seconds in the runtime test. The original 100-second gate remains failed;
a complete initial-flow probe exposed the missing post-bootstrap sign-in.
Both gates preserve 41 containers/236 volumes exactly and leave the user instance
unchanged. The qualified profile is pinned 26.10.0, `/data`, port 5006,
`/health` with timeout 120 seconds, native arm64, trusted rootful classic-store
Docker/overlay2 and private fixture HTTPS. Actual backup/restore, upgrades,
cross-host recovery, other architectures and production ACME remain unqualified.
[Actual Budget qualification](../../docs/hosting-catalog.md#actual-budget-qualification)
records the evidence index/hash, failures, retained checks, review limits and timings.

## Vikunja with PostgreSQL catalog profile

Use `bash acceptance/runtime-hosting-storage/gate.sh --managed-compose` after
exact-candidate review and target approval. It reuses the Compose browser, setup,
phase handshake and preservation checks. Catalog install/retry saves untouched
public source and generated runtime references; later source editing selects the
protected replacement. No new gate mode or driver is introduced.

The accepted source is `eeee82b6cf9350949e01121d59ec736e336dc734`. The failed
167-second first run and corrected 246-second pass remain separate. The actual
API test passed in 159.53 seconds; all 41 containers, 236 volumes and user-instance
profiles were preserved. The browser receipt is copied separately from the legacy
safe tar allowlist. See [catalog qualification and evidence](../../docs/hosting-catalog.md#catalog-compose-qualification)
for the index hash, checks, failure diagnosis, source dependencies and limits.
Prior Linkding, Kuma and Actual physical evidence is retained, not rerun.

## Catalog metadata and destination selection

For selected-template requirements and destination eligibility, use the existing
Playwright catalog test after exact-candidate approval:

```sh
cd web/app
NEROCD_BROWSER_SMOKE_PORT=18182 bunx playwright test tests/browser/hosting-catalog.spec.ts --project=chromium --workers=1 --no-deps
```

This reuses `scripts/browser-server.sh` and its owned PostgreSQL, local bootstrap,
private runtime directory and cleanup trap. It installs metadata through the real
API, deliberately drops a committed response, retries the identical request and
reads the receipt through its registered resource URL. It verifies the served
manifest inside the live test and screenshots four viewport widths. It starts no
application runner and does not repeat the accepted physical catalog workflows.

The [accepted `985652b9` qualification](../../docs/hosting-catalog.md#destination-requirements-qualification)
retains both failed fixture attempts, actual HTTP/PostgreSQL proof, source/build
provenance, screenshot inspection and exact host/user preservation. Keep hardware
and capacity claims unknown until independently qualified.
