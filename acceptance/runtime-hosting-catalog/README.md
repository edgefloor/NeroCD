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
The outer container has no host bind or Docker socket mount and no published
port. Existing host containers, volumes and the user UI remain outside this target.

Inside that daemon, the established PostgreSQL fixture uses
`postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94`,
one CPU, 1536 MiB memory, 128 PIDs, a 1 GiB data tmpfs, WAL targets 128/32 MiB,
and default durability. The existing pinned registry, Traefik, Pebble and local
storage helper images remain prerequisites of the reused fixture, although the
catalog test does not create application routes or run backup/delete machinery.
The new exact image pull is the Linkding digest in [the catalog contract](../../docs/hosting-catalog.md).
All application mutations occur inside the owned guest. Guest API TLS uses the
existing fixture CA. The compiled runner uses the actual public API and journal.

`TestHostingCatalogLinkdingCompiledRunnerHTTPS` installs from the catalog,
replays the same request, explicitly reveals the managed references, deploys the
ordinary image, checks `/health` and version 1.47.0, submits Linkding's login form,
and saves a bookmark using its session-authenticated API. Scraping and HTML
snapshots are disabled for the fixture bookmark, so no external website is fetched.
It starts a fresh cookie jar after runner restart/redeploy and after a failed
candidate restores the predecessor. It verifies bookmark data, volume origin,
secret IDs, versions and values, original template copy, and absence of plaintext
in operation metadata, runner logs and journal. The local unit, API and PostgreSQL
checks separately cover malformed requests, concurrent installation, permission
and destination denial, and revoked replay.

The gate must report the exact test as passed with no skip. Reviewers must still
assess `code_hygiene`, `specification` and `critical_security` at the exact SHA.
Runtime and browser qualification remain pending until their evidence exists.
