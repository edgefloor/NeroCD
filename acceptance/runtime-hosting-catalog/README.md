# Test the Linkding catalog workflow

This fixture installs pinned Linkding 1.47.0 through the public API and connected
browser, deploys it with the compiled runner, and verifies real application data.
It uses the [storage gate](../runtime-hosting-storage/README.md) in `--catalog` mode.

## Run the gate

Run from a clean committed checkout after approval for the exact candidate and
Docker target:

```sh
bash acceptance/runtime-hosting-catalog/gate.sh
```

The wrapper selects `TestHostingCatalogLinkdingCompiledRunnerHTTPS`. A pass
requires the exact test result without a skip and every required browser marker.
Compilation does not establish a runtime pass.

## Target and prerequisites

The outer target is `unix:///var/run/docker.sock`, daemon
`e37dd219-b13f-47dd-99ed-21d79442ed0c`, Linux aarch64. Its owner is
`nerocd-catalog-linkding-runtime-20261026`; the container ends in `-dind` and its
sole data volume ends in `-docker-data`. The owner label is
`io.nerocd.storage-runtime.owner`.

The gate uses
`docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489`
with four CPUs, 6 GiB memory, and 2048 PIDs. The privileged guest has no host bind
or Docker socket mount. Only API port 18190 is published to a dynamic host
`127.0.0.1` port for the browser. The gate verifies the requested and assigned
binding before use and cleanup. No application port is published on the host.

PostgreSQL, registry, Traefik, Pebble, and the prepared archive helper are
prerequisites of the reused storage fixture. The exact Linkding image and runtime
settings are in the [catalog definition](../../docs/hosting-catalog.md#pinned-definition).
All application changes occur inside the owned guest. API and application HTTPS
use the fixture CA.

## Assertions

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
