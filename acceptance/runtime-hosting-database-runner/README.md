# Compiled database and connected HTTP app acceptance

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

This fixture covers one app and one database on one host, an ordinary registry image, no app storage or routes, and same-attempt lost-ACK recovery. It does not qualify expired-authority mutation recovery, host or disk loss, native backup, archive restore, or route continuity. Database metadata remains `adapter_unqualified` until a separate qualification decision. UI verification against the combined real backend is a later check.

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
