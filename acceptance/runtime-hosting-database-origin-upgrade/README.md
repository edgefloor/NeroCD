# Private database origin and hostname upgrade fixture

This fixture exercises the accepted private engine directly on native Linux arm64.
It does not qualify the API, River task, runner admission, credential transport, or
R42 application binding workflow. Its materialization receipt is local test
context derived from actual Docker volume evidence, not controller provenance.

For PostgreSQL 17.11 and Redis 8.2.10, it creates a real v1 writer and writes A as
`ncd_app`, captures the retained initialized origin with the read-only observer,
then upgrades to empty-membership v2 B on the same volume and network. A temporary
client resolves the deterministic hostname and reads A/writes B over an
application-role authenticated connection. After closing and reopening the engine,
read-only upgrade observation checks actual alias configuration and Docker top
host PID/UID 999. V2 C redeploy then reads A+B and writes C through a new client.
Original volume creation evidence, retained record bytes and network identity must
survive both replacements. Temporary clients are removed before observations or
replacement, except the deliberate foreign-client rejection case.

Missing or changed retained origin, expired authority/credentials and a foreign client must
reject upgrade without any Docker mutation or source stop. A deliberately wrong
actual alias must reject read-only observation; the fixture restores the intended
alias before C. Observation commands are restricted to inspect/list/info/top and
must preserve all protected state and secret files. The exact owned-volume listing
used by the engine is permitted; other volume actions remain refused. Failed B/C
applications retain a bounded sequence of closed command tags and candidate shape
booleans/counts, excluding raw command arguments, paths and credentials. Source
comparisons independently validate both unique mounts against the inspected owned
volume and protected credential directory before normalizing mount order; all
other fields, state, PID and network identity remain exact. Cleanup uses its own
bounded uncanceled context and reports unconfirmed identity/removal/absence. Client credential JSON travels
only over Docker exec stdin, and client diagnostics are discarded. The local
redaction test verifies malformed/oversized inputs cannot echo credential markers.

## Local checks

Run from the assigned repository root without the physical opt-in:

```sh
python3 -B -m unittest discover -s acceptance/runtime-hosting-database-origin-upgrade -p gate_test.py -v
go test ./internal/runnerruntime -run 'TestDatabaseOriginUpgradeClientFailureRedaction|TestDatabaseNetworkOriginObserver|TestDatabaseNetworkUpgrade' -count=1
go vet ./internal/runnerruntime
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c -o /tmp/nerocd-origin-upgrade.test ./internal/runnerruntime
```

The fault suite uses harmless CLI stubs, including ambiguous creation responses,
foreign volumes, malformed identities, build failures, deadlines, signal delivery,
process-group joining and cleanup failures. Archive fetch, size, hash, signature,
installation and version failures must clean the exact owned outer and volume.
Unsupported, malformed or oversized numeric process output must fail before the
database test. These checks do not contact Docker or install packages.

## Separately authorized physical run

After exact-SHA code hygiene, specification and critical security reviews, obtain
Leader authorization for this exact external fixture, then run:

```sh
bash acceptance/runtime-hosting-database-origin-upgrade/gate.sh
```

The fixed outer target is `unix:///var/run/docker.sock`, daemon
`e37dd219-b13f-47dd-99ed-21d79442ed0c`, Linux aarch64. Owner is
`nerocd-database-origin-upgrade-20261024-b084`; outer container is that owner plus
`-dind`, and its sole private local Docker data volume is that owner plus
`-docker-data`. The pinned, already available outer image is
`docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489`.

The launcher creates one privileged rootful nested daemon with no host socket or
filesystem mounts and no published ports. It mounts only the exact newly owned
Docker data volume. Database images are pulled by production digest-pinned
preparation inside the nested daemon. The launcher builds only the static runtime
test binary, copies it into the outer container, and selects exactly
`^TestHostingDatabaseOriginUpgradeNativeRuntime$`. It never touches PG32967.

The daemon host must support `ps -eo pid,uid` with numeric PID and UID columns.
The pinned Alpine image's BusyBox `ps` rejects `uid`, so the launcher provisions
[Alpine v3.23 main aarch64 procps-ng 4.0.5-r0](https://pkgs.alpinelinux.org/package/v3.23/main/aarch64/procps-ng)
inside its disposable daemon before any database test. Production process
ownership still requires every observed UID to equal 999, the inspected host PID
to occur in the list, and container identity to remain stable across inspection.
The preflight checks tool support, while the database test checks actual ownership.

The complete package closure is fixed in `gate.sh`, including archive SHA-256
identities. Its versions are:

| Package | Version |
| --- | --- |
| procps-ng, libproc2 | 4.0.5-r0 |
| libintl | 0.24.1-r1 |
| libncursesw, ncurses-terminfo-base | 6.5_p20251123-r0 |
| utmps-libs | 0.1.3.1-r0 |
| skalibs-libs | 2.14.4.0-r0 |
| musl | 1.2.5-r24 |

Only the eight fixed archive URLs under
`https://dl-cdn.alpinelinux.org/alpine/v3.23/main/aarch64/` supply packages.
An isolated Python helper downloads each archive into the host's new evidence
directory and verifies its SHA-256 before publishing the file. Its environment
contains only the trusted tool PATH; Python configuration, proxy and credential
environment variables are excluded. The helper explicitly disables proxies and
redirects and requires HTTPS certificate and hostname verification. Each archive
is limited to one MiB, each helper to 25 seconds, and all downloads share a
120-second budget. The existing process-group runner bounds and joins a stalled
helper. Downloads do not use an APK repository index or package solver.

After all eight hashes pass, the launcher copies the archives into the exact
owned daemon. Explicit `apk verify` uses the pinned image's `/etc/apk/keys` before
offline installation of the eight local archives. APK commands use a clean
environment and an empty repository configuration. Signature trust is required;
the launcher does not permit untrusted packages or disable certificate checks.
An exact installed-version check records the closure, including musl, before the
bounded PID/UID-only probe. Every provisioning stage must succeed before the
database test starts. Missing pinned archives or incompatible existing package
constraints fail preflight and trigger the same owned-resource cleanup.

This package set qualifies only this fixed disposable fixture profile. It does
not establish support for other Docker hosts or Alpine versions. Package fetching
is part of the separately reviewed and authorized physical run, and never installs
anything on the outer host. The outer image, mount, network and port policy remain
the fixed profile described above.

Evidence is bounded under a new `/tmp/nerocd-database-origin-upgrade-20261024.*`
directory: candidate/tree/source blobs, binary hash/build info, daemon profiles,
verified downloaded archives, per-archive fetch output,
signature/install/version output, the installed closure and
numeric PID/UID probe, physical test output, before/after inventories, process
absence and cleanup result.
It verifies exact outer labels, image/configuration and the complete observed data
volume metadata before removal. Ambiguous outcomes preserve reserved storage;
inspection errors never establish absence. On interruption, task-owned host process
groups are joined and fixed guest executable paths are stopped/joined before the
owned outer and volume are removed. Unrelated outer container and volume identities,
plus image and network inventories, must remain unchanged. Network listings require unique full IDs and names. Each exact ID is inspected
read-only for its structured label map; flattened listing labels are never parsed.
Raw listings, complete inspect documents and structured comparison records are
retained. Label ordering compares equally, while changed values (including commas
inside a value), duplicate documents and missing/mismatched inspections fail.
Every final inventory kind is captured even if an earlier comparison fails. A nonzero exit or
preserved resource requires reconciliation; it is not a successful physical
qualification.
