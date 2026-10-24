# Private database compiled-runner acceptance fixture

This fixture connects the authenticated public API, River, the compiled production runner, protected credential delivery, and the native PostgreSQL/Redis engine adapters. Implementation and harmless checks do not qualify those adapters. Run the physical gate only after the Leader authorizes the exact reviewed combined revision.

The fixed outer target is `nerocd-database-runner-20261024-dbr7319-dind`, with volume `nerocd-database-runner-20261024-dbr7319-docker-data` and owner label `io.nerocd.database-runner.owner=nerocd-database-runner-20261024-dbr7319`. The launcher requires daemon `e37dd219-b13f-47dd-99ed-21d79442ed0c` through `unix:///var/run/docker.sock`, native Linux arm64, and the pinned Docker 29.4.0 DinD image. It attaches one owned Docker-data volume and publishes no host ports. It does not bind the host Docker socket or source checkout into the guest.

After separate physical authorization, run from a clean committed checkout:

```sh
bash acceptance/runtime-hosting-database-runner/gate.sh
```

The gate records the commit, tree, source blobs, build information and SHA-256 of the API test binary, production runner, and disposable client. It initializes an isolated control PostgreSQL inside the guest. Only schema/seed and River infrastructure are initialized directly. Login, enrollment consumption, server/destination grant, shared project environment, database creation, and deployment requests use real public HTTP endpoints over a verified fixture CA. The API service holds a fresh in-memory managed-secret keyring.

For PostgreSQL and then Redis, deployment A writes a marker through an authenticated disposable client. Deployment B must have a different container and retain the exact volume, private network ID, recipe and credential references. The client reads the original marker. The compiled runner then stops and joins, restarts with its original protected state and secret roots, and handles deployment C. A fault commits C's real Complete request but withholds its response. The fixture stops and joins the runner, checks the exact pending journal result, restarts it, and checks the unchanged receipt/container, journal acknowledgment, and absence of repeated credential delivery or writer admission. The marker must remain readable and exactly one writer may run.

The client uses an explicit-version owner-authorized managed-secret reveal and checks its committed public audit. The bounded helper never prints reveal bodies or parser diagnostics. It verifies the exact disposable client identity before passing password bytes through stdin. Passwords are excluded from client arguments, environment, image layers and fixture logs. The client is removed and its absence verified before another database operation. This ephemeral test attachment does not implement R42 application binding.

The fixture also checks that workload ports remain private and scans API/runner output and public durable metadata for protected values. Database metadata continues reporting `adapter_unqualified`. C exercises runner-process restart and persistent database data across replacement; it does not claim a database restart API, application binding, or archive restore.

An operation wait retains the 180-second deadline and fails promptly if the compiled runner exits. Failure diagnostics contain only a closed operation-state value, phase-presence and hold booleans, the retained process exit classification/code, and completed response counts for fixed API method/route/status combinations. Counts saturate at 9999; status 0 groups unwritten or unrecognized responses. Dynamic operation paths become a fixed route template. Diagnostics exclude raw logs, error text, bodies, headers and dynamic URLs. Process waiting has one owner, so failure reporting and cleanup inspect the same exit result. These diagnostics do not establish a cause or qualify a failed runtime run.

Before and after Complete replay, with the runner joined and the private client absent, the fixture captures all container IDs for that binding and all users of its retained volume, including stopped containers. Each identity must belong to the known A/B/C operations, have the expected labels, image and volume mount, and preserve the exact scoped inventories across replay. Foreign users, unknown containers and extra stopped residue fail the gate. These snapshots establish the absence of additional persistent residue; they do not count transient Docker create-and-delete operations or require unrelated global inventories to stay unchanged.

Harmless validation uses executable Docker stubs and never contacts a Docker daemon:

```sh
python3 acceptance/runtime-hosting-database-runner/gate_test.py
go test -race ./internal/api -run '^TestDatabaseRunner'
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c -o /tmp/database-api.test ./internal/api
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/database-client ./acceptance/runtime-hosting-database-client
```

The launcher preserves the reviewed ownership, inventory, process-group joining and ambiguous-create cleanup checks. Unknown outer creation outcomes reserve the volume; mismatched ownership or unexpected volume users preserve resources and fail. Evidence is retained under `/tmp/nerocd-database-runner-20261024.*`. A failed guest-process join stops the positively identified owned outer and preserves it for inspection.
