# Test catalog application workflows

The catalog tests install pinned applications through the API and browser, deploy
with a compiled runner, and verify application data after replacement. They share
the [storage gate](../runtime-hosting-storage/README.md), disposable guest and
cleanup checks.

## Run an application profile

Run from a clean committed repository root with the storage gate's Docker target,
pinned images and browser dependencies available.

| Application | Command |
| --- | --- |
| Linkding 1.47.0 | `bash acceptance/runtime-hosting-catalog/gate.sh` |
| Uptime Kuma 2.5.8 | `bash acceptance/runtime-hosting-storage/gate.sh --uptime-kuma` |
| Actual Budget 26.10.0 | `bash acceptance/runtime-hosting-storage/gate.sh --actual-budget` |
| Vikunja with PostgreSQL | `bash acceptance/runtime-hosting-storage/gate.sh --managed-compose` |

Each profile selects its own application test. The gate builds the WebUI and
checks served assets against its manifest. Browser tests cover 320, 390, 430 and
1280 pixel widths without intercepting application responses. A successful run
requires the selected runtime test and browser assertions; skipped tests fail.

## What the tests check

All application profiles test catalog installation, identical retry after a lost
response, viewer denial, managed HTTPS and persistent data after replacement.

- **Linkding:** explicit credential reveal/hide, bookmarks after redeploy and
  failed replacement, backup and restore into a distinct resource and volume.
  Restored sessions read archived bookmarks and make writes isolated from the
  source. Tests also check secret references, UID/GID, modes, completion replay
  and recovery after a writer-start failure.
- **Uptime Kuma:** SQLite first-run setup, administrator creation, a saved monitor
  and server setting. Fresh login after redeploy and failed replacement retains
  settings and permits new monitor writes.
- **Actual Budget:** password setup, sign-in, budget/account/transaction creation
  and server sync. Fresh browser contexts download server data after redeploy and
  failed replacement, then make new writes without carrying IndexedDB state.
- **Vikunja/PostgreSQL:** catalog install preserves the public Compose source and
  generated runtime references. Tests cover routing, records/uploads across
  redeployment, private-image failure preservation, Stop/Start, component database
  backup/import, named-volume backups, schedules and archive download.

Application HTTPS uses the fixture CA. The guest has no host bind mounts or Docker
socket mount. The API uses a dynamic loopback port; Kuma and Actual also publish
guest HTTPS on `127.0.0.1:25443`. Cleanup verifies owned container, volume and port
identities and preserves unrelated resources and the existing user instance.

## Catalog metadata and destination selection

Run the focused browser test from the repository root:

```sh
cd web/app
NEROCD_BROWSER_SMOKE_PORT=18182 bunx playwright test tests/browser/hosting-catalog.spec.ts --project=chromium --workers=1 --no-deps
```

It uses [browser-server.sh](../../web/app/scripts/browser-server.sh) to start the
API with an owned PostgreSQL fixture and cleanup trap. It tests selected-template
requirements, destination eligibility, installation retry and resource readback.
It also checks served assets and captures four viewport widths. No application
runner starts in this test.

<a id="linkding-catalog-runtime-acceptance"></a>
<a id="qualification-evidence"></a>
<a id="archive-extension-and-qualification-attempts"></a>
<a id="final-physical-qualification"></a>

## Further reading

- [Catalog definitions and requirements](../../docs/hosting-catalog.md)
- [Linkding recovery](../../docs/hosting-catalog-recovery.md)
- [Uptime Kuma results](../../docs/hosting-catalog.md#uptime-kuma-qualification)
- [Actual Budget results](../../docs/hosting-catalog.md#actual-budget-qualification)
- [Vikunja/PostgreSQL results](../../docs/hosting-catalog.md#catalog-compose-qualification)
- [Destination selection results](../../docs/hosting-catalog.md#destination-requirements-qualification)
- [Compose behavior and results](../../docs/hosting-compose.md)
