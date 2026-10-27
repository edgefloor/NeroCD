<a id="compiled-database-and-connected-http-app-acceptance"></a>

# Test compiled database and connected application execution

This fixture runs the public API, River, compiled runner, protected secret delivery,
and native PostgreSQL and Redis adapters. Optional profiles add a connected browser,
existing HTTPS route continuation, and one owned upload volume.

## Run the fixture

This fixture runs the authenticated public API, River, the compiled production runner, protected secret delivery, and the native PostgreSQL and Redis adapters. Run the physical gate only after the Leader approves the exact combined controller, runner, bridge, and fixture revision.

The launcher selects `^TestHosting(Database(CompiledRunner|CaptureCompiledRunner|UpgradeCompiledRunner)|ApplicationDatabaseCompiledRunner)$`. Every named test must pass without a skip:

- `TestHostingDatabaseCompiledRunner`
- `TestHostingDatabaseCaptureCompiledRunner`
- `TestHostingDatabaseUpgradeCompiledRunner`
- `TestHostingApplicationDatabaseCompiledRunner`

After separate physical authorization, run from the approved clean committed checkout:

```sh
bash acceptance/runtime-hosting-database-runner/gate.sh
```

The fixed outer target is daemon `e37dd219-b13f-47dd-99ed-21d79442ed0c` through `unix:///var/run/docker.sock`, native Linux arm64. The owned container is `nerocd-database-runner-20261024-dbr7319-dind`. Its only Docker-data volume is `nerocd-database-runner-20261024-dbr7319-docker-data`. Both carry `io.nerocd.database-runner.owner=nerocd-database-runner-20261024-dbr7319`. The pinned Docker 29.4.0 DinD image publishes no outer-host ports and receives no host socket or source bind mount.

The gate builds the API test binary, production runner, and fixture client from that checkout. Its evidence directory records the full commit and tree, source blobs, Go build information, and SHA-256 of each binary. It initializes isolated control PostgreSQL inside DinD. Only schema, seed, and River infrastructure are initialized directly. Session login, enrollment, server and destination grants, database and image resource creation, runtime configuration, Connect, app replacement, and database maintenance use public HTTP over the verified fixture CA. The API holds a fresh in-memory secret keyring.

The gate installs the exact eight-package procps closure used by the [origin-upgrade fixture](../runtime-hosting-database-origin-upgrade/gate.sh). Versions and SHA-256 values are pinned for `libintl`, `libncursesw`, `libproc2`, `musl`, `ncurses-terminfo-base`, `procps-ng`, `skalibs-libs`, and `utmps-libs`. Fixed Alpine v3.23/main/aarch64 HTTPS downloads verify TLS, refuse redirects and ambient proxies, and enforce byte and deadline bounds. APK verifies signatures using the image's trusted keys, installs offline, and checks exact versions. A bounded numeric `ps -eo pid,uid` probe must pass before compiled tests run. Existing PostgreSQL 17.11, Redis 8.2.10, and control/tool profiles remain unchanged.

Inside the owned DinD, the gate creates `nerocd-database-runner-20261024-dbr7319-registry` from `registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373`. Registry storage is temporary. Port 5000 has a dynamically assigned publication on the **inner** daemon's loopback only. The gate verifies its owner, ID, running state, and exact publication before use. This disposable registry serves a nonsecret test image over inner-loopback HTTP. The controller API continues using verified HTTPS.

`Dockerfile.http` builds a scratch, unprivileged image with `/client serve` as its entrypoint. The gate pushes it to `127.0.0.1:<dynamic-port>/nerocd-http-app`, captures the exact registry digest, removes its local tag, and requires no matching local image before the production pull. The public resource uses that digest. `NEROCD_DATABASE_HTTP_APP_IMAGE` supplies only this public image identity to the test binary.

For each engine, the connected test creates an ordinary V1 database, an undeployed image application, and literal engine/namespace runtime configuration. Its first public Connect must automatically capture the existing volume origin, upgrade to V2, and deploy app A. No manual V2 upgrade precedes Connect. The opted-in runner has image, release-port, runtime-environment, database, owned-storage, automatic capture, upgrade, application-binding, and membership-maintenance prerequisites. A persisted capture capability alone does not enable polling.

The running app exposes `/ready`, `PUT /records/{key}`, and `GET /records/{key}` on its assigned loopback HTTP port. PostgreSQL uses `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, and `PGPASSWORD`. Redis uses `REDIS_HOST`, `REDIS_PORT`, `REDIS_USERNAME`, and `REDIS_PASSWORD`. The production runtime-secret path supplies credentials. The fixture does not reveal them, place them on stdin, or dump the app's secret environment. Each request resolves and pins the exact private alias, opens a fresh bounded connection, and returns sanitized failures. `FIXTURE_DATABASE_ENGINE` and `FIXTURE_DATABASE_NAMESPACE` select the nonsecret profile.

App A writes and reads `a`. App B must use a new container and retire A exactly, then read `a` and write `b`. Database maintenance must replace the database container while retaining the volume, actual private network, encrypted credential metadata and references, and B's exact running endpoint. B then reads both records and writes `db_replaced`.

App C's fault commits the real controller Complete but withholds its first successful response. The fixture stops and joins the runner, verifies the exact pending journal body and original authority, and restarts with the same credential and protected roots. It requires byte-identical Complete and ACK replay, journal retirement, unchanged runtime identities, no additional admission, secret delivery or effect authorization, and no scoped Docker events during replay. C reads the retained records and writes and reads `c`. Exactly one app member and one database writer may remain. Unauthorized, stale-generation, and cross-destination public requests must fail. Logs must not contain the saved fence or protected credentials.

The original three scenarios remain in the same gate. They check ordinary database replacement, exact-ID capture response-loss replay, and repeated empty-membership V2 upgrade. Their legacy disposable client retains its bounded stdin behavior and explicit owner-authorized reveal/audit checks. The new HTTP app uses the separate production runtime delivery flow. The legacy client must be removed before another database operation.

The reference behavior follows the pinned Coolify source at `b12d1386f3d458618b4c579babcd1e339c6c40b3`: PostgreSQL and Redis starts retain named mounts and destination networks; private URLs use the database identity; application deployment supplies environment and destination-network configuration. NeroCD carries these effects through its compiled Go agent, lease and fence, durable journal, and exact acknowledgments instead of SSH execution and activity status.

The base profile covers one app and one database on one host, an ordinary registry image, no app storage or routes, and same-attempt lost-ACK recovery. The route profile below adds continuation of existing HTTPS routes. It does not qualify expired-authority mutation recovery, host or disk loss, native backup, archive restore, or initial routed Connect. Database metadata can remain `adapter_unqualified`; fixture results do not advertise support on arbitrary agents.

Harmless local checks never contact Docker:

```sh
python3 acceptance/runtime-hosting-database-runner/gate_test.py
go test -race ./acceptance/runtime-hosting-database-client
go test -race ./internal/api -run '^Test(ApplicationDatabaseRuntime|DatabaseRunner|DatabaseCaptureRuntime|DatabaseUpgradeRuntime)'
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c -o /tmp/database-api.test ./internal/api
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/database-client ./acceptance/runtime-hosting-database-client
```

Gate tests use executable CLI stubs. They require every physical result, reject foreign registry ownership, non-loopback publication and foreign digests, and preserve checks for source and binary identities, failed commands, cancellation, and joined process groups. A skip is never a physical pass.

Cleanup joins only task-owned process groups, revalidates exact container and volume ownership and users, and removes only the owned outer container and its Docker-data volume. Removing that owned DinD also removes its inner registry and app fixtures. There is no prune or global cleanup. Unknown outer-create outcomes reserve the volume. Ownership mismatch or foreign volume use preserves resources and fails. A failed guest-process join stops the verified owned outer container and preserves it for inspection. Evidence remains under `/tmp/nerocd-database-runner-20261024.*`.

## Verify the live UI against the same workflow

The optional browser mode requires the reviewed connected UI, controller, runtime, bearer-context bridge, and this fixture in one combined checkout. Its gate builds the real UI before compiling the API test binary, records a deterministic `browser-ui-manifest.json`, and serves those embedded bytes. A standalone fixture checkout without the connected UI source fails this prerequisite. Source-only TypeScript checks do not verify the rendered panel.

After separate authorization of the exact combined candidate and browser bridge target, run:

```sh
NEROCD_DATABASE_CONNECTION_BROWSER=1 bash acceptance/runtime-hosting-database-runner/gate.sh
```

Browser mode adds exactly one outer publication: container port `18080/tcp` to a dynamically assigned host `127.0.0.1` port. Normal mode still requires zero outer ports. Both configured and assigned publications are validated, and a changed origin, public bind, or extra port fails the gate. The bridge serves a test-only cookie API over owned loopback HTTP. The compiled runner continues using its separate verified TLS API and protected credential roots.

`browser-driver.mjs` reads bounded, nonsecret fixture metadata and runs the real Playwright spec once for PostgreSQL and once for Redis. Metadata contains only the exact origin, engine, project/environment labels, resource IDs, and a random nonce control path. Unknown fields, duplicate engines, a different origin, malformed paths, missing results, and child failures fail closed. Fixture and browser process groups have bounded, joined cleanup on failure, timeout, or cancellation. The gate also requires exactly one successful browser result for each engine.

The browser signs in through the actual UI. It selects the existing database, connects the application, redeploys B, requests database maintenance, and admits C for the existing committed-Complete response-loss exercise. There is no route interception or synthetic runner receipt. Go releases each next phase only after the existing HTTP record, container, endpoint, membership, and replay assertions pass. Nonce-scoped controls require the current owner cookie, current project-owner role, exact Origin and Host, empty bodies and no query. Their proofs read the actual running app; they cannot apply or settle a hosting operation.

Fixture setup uses supported service bootstrap to create and retain a distinct active installation administrator at `database-bootstrap@example.local`. Public APIs establish resources, project ownership, and viewer membership. One explicit isolated-schema exception demotes exactly the seeded `usr_bootstrap` account at `admin@example.local` to an ordinary user after public setup, with affected-row and resulting-role checks. That account signs in as project owner; the separate bootstrap administrator remains active. The fixture and browser both require public bootstrap status `complete` before login, and login actions have short explicit deadlines. This proves project-owner actions; it does not qualify self-service account provisioning. The viewer inspects the settled connection but cannot submit Connect, app redeployment, maintenance, or fixture controls. An anonymous request and an owner request for a separately owned private project must fail. The spec checks keyboard activation and panel containment at 320, 390, 430, and 1440 pixels. Browser traces and automatic screenshots/video are disabled; explicit screenshots cover only the nonsecret connection panel after login.

Harmless gate tests exercise the bridge profile and UI-build-before-embedding order with executable stubs. Driver tests run local fixture/browser stubs and verify that child PIDs are gone after success, rejection, and cancellation. They never start a physical browser, Docker daemon, or database. Actual browser and combined physical results require explicit authorization and retained passing evidence. The qualified revision below has that evidence.

## Qualified route continuation

The historical storage-less profile enables both modes for the five-group route
and live UI gate:

```sh
NEROCD_DATABASE_CONNECTION_BROWSER=1 NEROCD_DATABASE_CONNECTION_ROUTES=1 bash acceptance/runtime-hosting-database-runner/gate.sh
```

Route mode adds `TestHostingApplicationDatabaseRouteFailuresCompiledRunner` to
the four base groups and uses fail-fast execution. It proves readiness rejection
and strict cutover rollback separately for PostgreSQL and Redis. The positive
scenario observes a distinct ready candidate before switch, exact yield/proxy ACK
replay across runner restarts, and exact predecessor retirement after switch.
The UI reviews all enabled application routes and renews approval when choices
change. This is existing-route continuation, with no initial routed Connect.

The route profile checks readiness rejection, strict HTTPS cutover rollback,
receipt replay, and exact predecessor retirement for both engines. The healthy
predecessor keeps serving during storage-less candidate preparation. This profile
does not qualify initial routed Connect.

## Qualified connected application uploads profile

The separate `NEROCD_DATABASE_CONNECTION_STORAGE=1` profile requires both
`NEROCD_DATABASE_CONNECTION_ROUTES=1` and `NEROCD_DATABASE_CONNECTION_BROWSER=1`.
It retains all five original runtime groups and adds the connected storage
success and recovery groups. Every PostgreSQL and Redis result, cleanup capacity
marker, and six serialized browser results must appear exactly once. Local unit
and launcher tests do not qualify this profile. A new physical run requires approval for its exact revision and complete
disposable target profile.

The qualified full command is:

```sh
NEROCD_DATABASE_CONNECTION_BROWSER=1 NEROCD_DATABASE_CONNECTION_ROUTES=1 NEROCD_DATABASE_CONNECTION_STORAGE=1 bash acceptance/runtime-hosting-database-runner/gate.sh
```

The gate builds `Dockerfile.http-storage` separately, pushes it to the same owned
inner registry, records its distinct digest, and removes the local tag before the
production pull. This scratch image explicitly uses UID/GID 0:0 and enables only
the fixture's bounded upload handler. The original UID/GID 65532:65532 image is
unchanged. This is a trusted root-container qualification; it makes no claim that
arbitrary nonroot images initialize a fresh Docker volume correctly. There are no
new outer ports, host paths, host sockets, DNS names, CA sources, or ACME targets.

For each engine, Connect first creates a storage-less app A0. Saving the public
resource storage metadata attaches one retained `/data/uploads` mount. An
explicitly acknowledged nonrouted replacement creates storage app A. Routed B
and C replacements then preserve both database records and binary uploads.
`PUT /uploads/{key}` atomically replaces one bounded file using an exclusive
temporary file, fsync, and rename. `GET /uploads/{key}` returns its bytes. Keys
cannot choose paths; requests are bounded to 1 MiB and redirects are refused.

The fixture compares a 65,537-byte SHA-256 payload through direct app HTTP and
trusted HTTPS, including the served container identity and certificate. It reads
bounded Docker copy archives in memory without extracting them to verify file
0640 and directory 0750 permissions with UID/GID 12345:12346. New B/C uploads,
database maintenance, and final Complete ACK replay must retain both datasets,
the same volume mount, exact creation labels, and original volume metadata.

Storage replacement explicitly interrupts service. At the yielded transition,
independent Docker inspection must prove the exact A container is stopped, its
finish time precedes B's start time, both reference the same owned volume, and B
is the sole running writer before candidate data probes. This profile does not
reuse the original storage-less assertion that A stays available during B's
preparation. The pinned upstream `ApplicationDeploymentJob.php` volume generation
and ordinary rolling update permit shared volume overlap; NeroCD's one-writer
contract is intentionally stricter.

Both engines exercise failed-readiness `resume_predecessor` and terminal failed
proxy `complete_replacement` recovery. For the storage readiness case, the fixed
`/fixture-write-then-unready` path synchronously completes one deterministic
65,537-byte upload and database write before returning 503. Repeated probes stay
503 without rewriting either value. The runner stops B, records the exact
`candidate_stopped` then `replacement_failed` receipts, acknowledges its journal,
and stays alive. The fixture inspects stopped B and verifies both writes after
resuming A; it makes no HTTP request to stopped B. The proxy fault pauses only the exact
ownership-verified nested managed proxy after B is ready. A fallback unpause is
registered before the effect, and identity is checked again before each pause
and unpause. This deliberately interrupts unrelated routes too. The test asserts
their restoration after unpause/recovery, not uninterrupted availability during
this fault. The ordinary unchanged route failure matrix still proves route
preservation outside the deliberate unavailability injection.

Recovery must use offered public actions and fresh reviewed storage/stage hashes.
The original failed operation and result remain intact during admission; queued,
running, succeeded, and uncertain recovery progress are independent. PostgreSQL
resume and Redis forward completion use the live owner recovery form, keyboard
review and acknowledgement, the real POST, and viewer denial. All four runtime
cases verify immutable same-operation association, a fresh task/lease/fence,
actual one-writer/data/HTTPS settlement, and exact cleanup. Forward completion
must preserve failed P, apply distinct P2 through its actual `proxy_<P2>` task,
and finalize the original candidate. After resume, both engines also admit a
fresh routed application C from new public route and storage reviews. C must
stop recovered A before starting, retire that exact A once, retain the original
upload volume and candidate-written database/upload data, and accept new writes
through trusted fixture HTTPS. This follows maintenance against the fresh
resumed membership receipt and preserves the original failed operation.
Acceptance of a POST or a stopped phase alone is insufficient. Resume does not
undo candidate database writes or uploaded files and cannot promise data
compatibility with the previous application version.

## Current connected storage qualification

The full profile has passed on native Linux arm64, PostgreSQL 17.11, Redis 8.2.10,
trusted rootful classic-store Docker, one destination, and private fixture-CA HTTPS.
The [connection guide](../../docs/hosting-database-connections.md) documents public
requests and recovery rules. Its [runtime tests](../../internal/api/hosting_application_database_storage_runtime_integration_test.go)
are supplemented by the compiled API tests selected by this gate.

The gate records source and binary identities, served UI hashes, runtime results,
joined processes, and owned cleanup. Existing host resources and the independent
user UI must remain intact. Missing cleanup or required test evidence fails the run.
Compilation alone does not establish a runtime pass.

The storage image runs as root to initialize the fresh volume. Arbitrary nonroot
initialization, production ACME, source builds, credential rotation, in-flight
request draining, control-plane restart, external or cross-host storage, and disk
loss are unqualified. Backup and archive restore use separate fixtures. Recovery
preserves candidate-written data; it cannot prove compatibility with an older app.
