<a id="uploaded-file-runtime-qualification"></a>

# Test owned upload storage and recovery

The storage gate deploys an upload application with a compiled runner and tests
HTTPS routing, persistent data, replacement and recovery. Application and database
services run inside a disposable Docker-in-Docker guest.

## Prerequisites

Run from a clean committed repository root. The gate requires:

- Go, Docker CLI and Python 3. Browser profiles also need Bun, Node.js, installed
  WebUI dependencies and Playwright Chromium.
- The Docker target pinned in [gate.sh](gate.sh):
  `unix:///var/run/docker.sock`, daemon
  `e37dd219-b13f-47dd-99ed-21d79442ed0c`, Linux aarch64.
- The script's pinned images in the host image cache. Image references and guest
  resource limits are defined in the script.
- Permission to start a privileged guest. It uses a labelled Docker data volume,
  with no host bind mounts or Docker socket mount. Browser profiles publish the
  fixture API on a dynamic loopback port.

HTTPS uses an isolated Pebble CA and fixture DNS. These tests do not use public
DNS, production ACME or a cloud storage account.

## Run a profile

```sh
bash acceptance/runtime-hosting-storage/gate.sh
```

Choose one optional profile:

| Argument | Coverage |
| --- | --- |
| none | Upload persistence across replacement, failed readiness and lost deployment responses |
| `--archive` | Local archive, size-limit failure and writer compensation |
| `--remote` | S3 validation, denied upload, same-archive retry, verified separate-volume restore, schedules and failure notifications |
| `--retention` | Policy-selected local and remote copy cleanup, replay and recovery from a retained copy |
| `--deletion` | Explicit copy selection, final-copy confirmation and physical deletion |
| `--catalog` | Linkding installation and separate-target recovery |
| `--uptime-kuma` | Uptime Kuma setup and persistence |
| `--actual-budget` | Actual Budget setup and server-data persistence |
| `--managed-compose` | Vikunja/PostgreSQL catalog deployment, records/uploads, private image pulls, Stop/Start, component backups, schedules and local archive download |
| `--private-registry` | Credential selection and private pulls for ordinary stateless and upload applications |

The [catalog README](../runtime-hosting-catalog/README.md) describes application
assertions and the metadata-only browser test.

## What the tests check

The API fixtures use real enrollment, placement, resource, deployment and route
controllers. Replacement tests check exact volume identity and a single running
writer. They cover lost responses, runner restart and failed-readiness recovery
while preserving an unrelated route.

Remote profiles use a disposable S3-compatible service. They verify local archive
preservation after denied upload, retry without another archive, response-loss
replay, corrupt/truncated download rejection and restored application reads and
new writes. Schedule tests check deduplication, viewer permissions, disable and
SMTP/webhook failure delivery.

Private pull tests use protected, uncached image references. Denied or revoked
credentials must preserve the serving application, route and data. Compose local
archive download checks the exact byte count and SHA-256 without changing backup
history or writer state.

## Focused fixture checks

These checks do not start the full stack:

```sh
go test -race ./internal/api -run '^TestHostingStorageRemote(ReceiptFaultContract|ObservationFaultContract)$' -count=1
python3 -B acceptance/runtime-hosting-storage/remote_evidence_test.py
python3 acceptance/runtime-hosting-storage/gate_test.py
bash -n acceptance/runtime-hosting-storage/gate.sh
node --check acceptance/runtime-hosting-storage/remote_browser.mjs
```

## Results and cleanup

The gate prints its evidence directory. Raw logs, inventories, browser sessions
and credentials are private. `safe-receipt.tar.gz` contains an explicit allowlist;
`catalog-browser-safe.json` and `compose-browser-safe.json` must be copied
separately when retaining those browser receipts.

Cleanup joins fixture processes and removes only the verified owned guest and
its unused labelled data volume. It never prunes the daemon. If identity, mounts,
ports or process shutdown cannot be verified, cleanup fails and preserves the
resources for inspection. Before/after checks cover unrelated containers,
volumes and the existing user instance.

See the [archive guide](../../docs/hosting-storage-archive.md),
[remote backup guide](../../docs/hosting-storage-remote-backups.md),
[retention guide](../../docs/hosting-storage-backup-retention.md),
[copy deletion guide](../../docs/hosting-storage-backup-deletion.md),
[private image pull evidence](../../docs/hosting-image-deployments.md#private-registry-qualification)
and [Compose evidence](../../docs/hosting-compose.md#qualification) for behavior,
supported environments and detailed results.
