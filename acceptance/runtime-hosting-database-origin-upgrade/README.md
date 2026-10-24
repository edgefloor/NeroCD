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
process-group joining and cleanup failures. It does not contact Docker.

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

Evidence is bounded under a new `/tmp/nerocd-database-origin-upgrade-20261024.*`
directory: candidate/tree/source blobs, binary hash/build info, daemon profiles,
physical test output, before/after inventories, process absence and cleanup result.
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
