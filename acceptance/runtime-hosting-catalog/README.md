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
catalog test exercises a managed HTTPS application route and does not run
backup/delete machinery.
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
error remains unknown; physical persistence qualification still requires a pass.

After each settled deployment and before older cleanup callbacks, the fixture
reads the exact owned application's state, exit/OOM/restart counters and admitted
CPU/memory/PID limits. It also records numeric uWSGI PIDs and allowlisted cgroup
OOM counters. Each of the three diagnostic Docker commands has a five-second
bound; raw inspect, environment, application logs and process arguments are not
recorded.

The gate must report the exact test as passed with no skip. Reviewers must still
assess `code_hygiene`, `specification` and `critical_security` at the exact SHA.
Runtime and browser qualification remain pending until their evidence exists.
The browser writes only fixed boolean markers, viewport widths, a fixed phase
and an error class to `catalog-browser-safe.json`; the gate requires every marker
and the exact runtime PASS with no skip. Compilation and syntax checks do not
establish that these physical assertions pass.
