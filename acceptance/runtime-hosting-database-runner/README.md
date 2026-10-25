# Compiled database and connected HTTP app acceptance

## Current connected storage qualification

The connected image-application workflow with PostgreSQL or Redis and one owned
upload volume is physically qualified at
`09a432407c41671517fa342d28c07e9c79978aea`, tree
`8f52ef4b6d8696b5fb184773e73f4170b32cf1e5`, through migration 0074 and
journal format 17. First Connect deploys without application storage or routes.
After it settles, attach one platform-managed named volume and explicitly approve
replacement with interruption. Later routed replacements retain database records
and uploads while enforcing one running volume writer.

The full gate passed in 683.423 seconds on 2026-10-25. All seven Go groups and
14 named subtests passed with zero Go skips or failures. Six serialized Chromium
phases passed, with 30 passing tests and six explicit opposite-profile skips.
The required readiness, proxy-failure recovery, later deployment C, HTTPS,
capacity and cleanup markers all passed for both database engines. PostgreSQL
resume and Redis forward completion also passed through the live owner recovery
form, with viewer denial and keyboard review at the tested viewport sizes.

The full receipt is
`/tmp/nerocd-operation-status-retry-integrated-full-handoff.json`.
Its exact profile is
`/tmp/nerocd-operation-status-retry-integrated-physical-profile.json`.
Named events and sanitized runtime output are retained in
`/tmp/nerocd-operation-status-retry-integrated-full-group-events.json` and
`/tmp/nerocd-operation-status-retry-integrated-full-runtime-redacted.log`.
The receipt binds source objects, compiled binary hashes, the generated and
served UI manifest, image identities, joined processes, and exact owned cleanup.
The unrelated 35 containers and 209 volumes, root graph, generated UI bytes and
independent user UI were preserved.

Qualification covers one same-host native Linux arm64 trusted Docker classic-store
fixture, PostgreSQL 17.11, Redis 8.2.10, and private fixture-CA HTTPS. The storage
image runs as root to initialize the fresh volume. The fixture verifies bounded
file UID/GID and modes. This does not qualify arbitrary nonroot image initialization
or production ACME. Recovery preserves candidate-written database records and
uploads. Resume does not undo those writes or establish compatibility with the
previous app version. Backup/restore, disk loss, migration, cross-host or external
storage, source builds, credential rotation, in-flight request draining and
control-plane restart remain outside this qualification. Full R/A/matrix gates
remain incomplete.

The proxy-failure case deliberately pauses the exact owned proxy, interrupting
unrelated fixture routes too. Their restoration is verified after unpause and
recovery. Ordinary route preservation is checked separately without that fault.
The earlier failed full run remains failed in
`/tmp/nerocd-operation-snapshot-integrated-full-handoff.json`.

## Run the fixture

This fixture runs the authenticated public API, River, the compiled production runner, protected secret delivery, and the native PostgreSQL and Redis adapters. Source implementation and local stub tests do not qualify these behaviors. Run the physical gate only after the Leader approves the exact combined controller, runner, bridge, and fixture revision.

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

The base profile covers one app and one database on one host, an ordinary registry image, no app storage or routes, and same-attempt lost-ACK recovery. The route profile below adds continuation of existing HTTPS routes. It does not qualify expired-authority mutation recovery, host or disk loss, native backup, archive restore, or initial routed Connect. Database metadata remains `adapter_unqualified` until a separate qualification decision. Combined live UI qualification is recorded below.

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

The connected public-image workflow is physically qualified at
`5277ab2ff5258ae6a4aee2fb642ed925d9a827cb`, tree
`a197801c8a7a66dd493f9eef7acd940070d845a2`, through migration 0073 and
journal format 16. An owner can connect one PostgreSQL or Redis database on the
same destination, then redeploy the application through UI or API while continuing
all reviewed existing HTTPS routes. Data and credential references survive app
replacement and database maintenance. The candidate starts and passes readiness
while the old app remains healthy. A strict HTTPS proxy switch precedes a fresh
finalize lease that retires the exact predecessor.

The full gate passed in 333.592 seconds with five runtime groups and zero skips.
Route failure checks took 136.17 seconds, the connected app group 89.70 seconds,
capture 25.11 seconds, normal database recovery 24.91 seconds, and upgrade 24.64
seconds. Four Chromium tests passed per engine, including the owner workflow in
38.4 seconds for PostgreSQL and 37.3 seconds for Redis. Readiness and cutover
failures preserve the old HTTPS app and database reads/writes for both engines.
Lost yield, proxy Complete and final Complete acknowledgments replay the original
retained attempt without repeating effects or credential delivery.

The physical receipt is
`/tmp/nerocd-connected-route-authority-integrated-physical-handoff.json`.
Source and binary identities are retained in
`/tmp/nerocd-connected-route-authority-integrated-sourceproof.json`, with sanitized
output in `/tmp/nerocd-connected-route-authority-integrated-runtime-redacted.log`.
Owned processes and resources were removed. The unrelated 35 containers and 209
volumes retained identical inventories; root graph and 75 generated UI files were
unchanged, and the independent user UI remained ready.

Qualification covers the pinned native Linux arm64 trusted Docker classic-store
fixture and verified private CA. Initial Connect cannot include routes. Source
builds, application storage, cross-server binding presentation, rotation,
database-native backup/restore, disk loss and public ACME remain unqualified.
There is no in-flight request draining or control-plane restart proof. Full R42,
A17 and all full requirement, acceptance and matrix gates remain incomplete.

The six prior route gate failures remain failures at their original revisions:

- `/tmp/nerocd-database-runner-20261024.sz62wpzi`
- `/tmp/nerocd-database-runner-20261024.7_uzg26w`
- `/tmp/nerocd-database-runner-20261024.er27jnw5`
- `/tmp/nerocd-database-runner-20261024.sk6wo1zu`
- `/tmp/nerocd-database-runner-20261024.8xkvabak`
- `/tmp/nerocd-database-runner-20261024.r6t4_4ux`

The earlier successful four-group `9a69228` checkpoint and its 174.487-second
receipt remain historical evidence. Neither it nor the six failed route attempts
is relabeled by the final pass. Six physical failure/repair rounds preceded the
333.592-second pass. Those observed durations provide no cost or ETA estimate.

## Qualified connected application uploads profile

The separate `NEROCD_DATABASE_CONNECTION_STORAGE=1` profile requires both
`NEROCD_DATABASE_CONNECTION_ROUTES=1` and `NEROCD_DATABASE_CONNECTION_BROWSER=1`.
It retains all five original runtime groups and adds the connected storage
success and recovery groups. Every PostgreSQL and Redis result, cleanup capacity
marker, and six serialized browser results must appear exactly once. Local unit
and launcher tests do not qualify this profile. The current `09a4324` full receipt above
qualifies the exact reviewed fixture. A new physical run still requires approval
of its exact revision and complete disposable profile.

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

The accepted storage-less `5277ab2` evidence remains labeled with its original
revision and dependencies. It does not prove these new mounts or recovery paths.
Database-native backup/archive, disk loss, arbitrary image initialization,
production ACME and full parity remain outside this profile's claims.

The current full run contains these observed named-group durations. They are
measurements of this fixture, not cost or completion-time estimates.

| Named group | Seconds | Result |
| --- | ---: | --- |
| `TestHostingApplicationDatabaseRouteFailuresCompiledRunner` | 144.59 | passed |
| `TestHostingApplicationDatabaseCompiledRunner` | 91.41 | passed |
| `TestHostingApplicationDatabaseStorageCompiledRunner` | 122.01 | passed |
| `TestHostingApplicationDatabaseStorageFailuresCompiledRunner` | 219.18 | passed |
| `TestHostingDatabaseCaptureCompiledRunner` | 25.21 | passed |
| `TestHostingDatabaseCompiledRunner` | 23.99 | passed |
| `TestHostingDatabaseUpgradeCompiledRunner` | 25.98 | passed |

The total gate includes build and setup time. It ran from
2026-10-25T11:22:24.626169Z to 2026-10-25T11:33:48.049611Z. All 32 required
markers occurred exactly once. The six browser skips select the opposite profile,
with four recovery tests skipped in ordinary phases and two ordinary owner tests
skipped in recovery phases. No required Go case skipped.

The pinned PHP storage correspondence is recorded in the
[connection guide](../../docs/hosting-database-connections.md#implementation-and-upstream-mapping).
Its volume metadata and deployment review inform the behavior. NeroCD's Go
runner uses immutable plans, one-writer interruption, lease/fence authority and
durable recovery receipts instead of SSH execution. These receipts do not
qualify upstream backup, external storage or ownership-transfer features.
