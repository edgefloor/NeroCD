# Uploaded-file runtime qualification

Run `bash acceptance/runtime-hosting-storage/gate.sh` from a clean committed checkout only after authorizing its exact disposable local Docker target. It requires the pinned arm64 Docker Desktop daemon in the script. It creates one labelled outer DinD container and one labelled data volume, with no host socket, host bind mounts, host PID namespace, or published host ports. A privileged guest provides the independent daemon required by the compiled runner. The gate records source, tree, binary hashes, image identities, container and volume inventories, joined processes, and exact outer object absence in its printed temporary evidence directory.

The Linux test initializes an isolated database schema and bootstrap identity, then uses authenticated HTTP for enrollment, placement, resource creation, immutable storage metadata, deployments, proxy configuration, hostname grants, and routes. Real queue workers dispatch to a compiled candidate runner. Fixture registration explicitly enables `hosting_owned_storage_v1`; this does not qualify or advertise arbitrary agents.

Proxy configuration is asynchronous. The fixture waits for matching desired and loaded generations with no active operation or ownership hold and the exact workload daemon. Route creation supplies exact operation receipts. Later replacements read the public storage deployment options and continuity status, then verify the returned loaded operation and current route selections before submitting another deployment. These reads send no request body.

The upload application writes unique binary bytes, then atomically renames the uploaded file through HTTPS. The test checks those bytes and the application version after A-to-B replacement, committed materialization response loss, committed Complete response loss with runner restart, and failed-readiness restoration. It compares the exact volume name, closed labels, creation timestamp, origin plan, daemon, mount, and single running writer. An unrelated HTTPS route must remain unchanged. A separate operation loses its irreversible creation-admission response before any volume exists. Restart must retain the hold and journal while leaving that volume absent.

All issuance uses an isolated Pebble CA and fake DNS inside the guest. HTTPS validates the fixture CA and hostname. This fixture does not prove public DNS or production ACME, UI workflow wiring, backup restore, unknown-writer recovery, or recovery of retained route continuity after an ambiguous failure. It does not create hosting authority by inserting database rows.

The outer gate removes only the exact verified owned outer container and its verified unused labelled data volume. Inner resources and their anonymous volumes exist only inside that disposable data volume. If ownership, mounts, or process joining cannot be verified, cleanup fails and preserves the target for inspection. It never prunes a daemon.

## Remote archive qualification

Run `bash acceptance/runtime-hosting-storage/gate.sh --remote` only after the Leader authorizes the exact committed combined candidate and this physical profile. The standalone acceptance commit needs the storage remote product commit. Its `nerocd_storage_remote_contract` build tag deliberately fails if production routes or request types are absent.

The fresh outer target is `nerocd-storage-schedules-runtime-20261026-5d45412a-dind`, with data volume `nerocd-storage-schedules-runtime-20261026-5d45412a-docker-data`. It uses the pinned daemon and DinD image above, four CPUs, 6 GiB memory, 12 GiB memory plus swap, and 2048 PIDs. It has one Docker volume mount, no host binds or socket, and one dynamically assigned publication `127.0.0.1::18190`. That port reaches only the authenticated fixture API and candidate UI. Admission and cleanup verify the requested and assigned publication. Extra ports, non-loopback bindings, or a changed assignment stop cleanup and preserve uncertain resources.

PostgreSQL runs inside that guest with one CPU, 1536 MiB memory, 128 PIDs, and a 1 GiB tmpfs at `/var/lib/postgresql/data`. Its WAL targets are `max_wal_size=128MB` and `min_wal_size=32MB`; these are checkpoint targets, not hard WAL limits. Durability settings retain PostgreSQL defaults. The 1 GiB data allowance fits below its memory cap, which leaves 512 MiB for PostgreSQL execution and buffers. The outer 6 GiB limit is unchanged, leaving 4.5 GiB outside PostgreSQL's cap for Docker and the other fixture services.

The first scheduled-workflow attempt exhausted the earlier 256 MiB data tmpfs during repeated migrations with SQLSTATE `53100`. Store tests run serially in separate schemas and register schema cleanup before applying migrations. Dropping schemas does not immediately reclaim catalog files or WAL, so schema isolation alone does not bound physical database size. The gate keeps every required test. `postgres-capacity-before-test` and `postgres-capacity-final` capture filesystem blocks and inodes, database and WAL bytes, schema count, selected catalog statistics, and effective WAL and durability settings. Final capture also runs on test failure before removing the owned guest. Each diagnostic command has a five-second deadline and verifies the exact PostgreSQL ID and owner label first. The diagnostics use the container's local socket and export no passwords or application rows. This capacity profile still requires a separately authorized physical run.

All prerequisites must already exist in the host image cache for comparison. Remote mode explicitly pulls the existing exact PostgreSQL, registry, Traefik, Pebble, and Silo pins into the disposable guest before any workload starts. Each guest image must retain the exact canonical repository digest and match the host's native platform variant, executable configuration, and ordered RootFS layer identities. Reported image IDs must match unless the host explicitly reports its pinned OCI descriptor as `Id` and the guest reports classic overlay2 metadata. That supported cross-store representation retains both independent IDs. Other unexplained ID differences fail qualification. `preload-<image>-proof.json` records the IDs, representation, platform, and configuration and RootFS hashes without copying environment values into the comparison proof. Saving and loading an image tar alone does not preserve lookup by repository digest. Direct fixture starts use `--pull=never` and consume stdout separately from Docker diagnostics.

Production behavior still performs explicit pulls. `internal/hostingproxy/engine.go` pulls the fixed Traefik pin during initial proxy preparation, and `internal/runnerruntime/image_engine.go` pulls app images from the guest's loopback registry. This profile permits those exact existing paths; it does not claim an offline or cached-only workflow. The app images are built and pushed only to the disposable guest's loopback registry at `127.0.0.1:25000`. The pinned Silo image is `pgsty/silo@sha256:87585bb783b7822318b3e1bcc4497c8427c98e2b021826d20b5f888713299f95`. Silo runs within the guest with one CPU, 512 MiB memory, 128 PIDs, a labelled data volume, and generated credentials. No cloud account is contacted.

Before compilation, the gate builds `web/dist` from the candidate. It records source Git blobs and the deterministic UI manifest. The real browser compares every served asset with that manifest before it signs in. The browser uses `/projects#storage-{resource_id}`, selects the fixture project and environment, and scopes the application storage region. There is no separate storage page route. Browser requests use the production controllers without request interception. SIGINT, SIGTERM, and the workflow deadline stop phase polling and close the driver's owned browser and API contexts.

The browser creates an S3 target from the uploads-only application, starts storage-specific validation, selects that exact revision, requests a backup, reloads its history after denied PUT, retries the same archive, and requests remote restore. Generated target credentials cross only the private HTTPS fixture handshake and password inputs. Logs, safe markers, and screenshots do not include them. The final history read repeats after a page reload, and the browser checks horizontal containment at 320, 390, 430, and 1280 pixels.

The API fixture verifies these physical outcomes:

- The original stopped writer stays stopped and the active writer resumes before either denied or successful PUT reaches Silo.
- Denied PUT preserves the local archive. A runner restart preserves its exact size and SHA-256. Retry starts no new archive helper.
- A lost successful upload receipt replays the same request and committed response. The successful object sees one PUT.
- Corrupt and truncated GET responses fail even while a valid local CAS object exists. The dependent restore is failed and held, with no extraction run, helper, or target volume.
- Download retry keeps the held restore and archive identity. Repeating the retry request returns the same transfer. Successful verification admits one extraction helper.
- An additional remote restore performs GET despite the local CAS, creates a different volume on the same destination, and preserves numeric UID, GID, modes, and archived bytes. A real restored app reads old bytes, writes new bytes, and leaves the source unchanged.
- Anonymous and cross-project reads fail. A viewer can read transfer history after receiving project membership, but cannot request a backup or retry an upload.

Remote mode also repeats `TestHostingStorageArchiveCompiledRunnerHTTPS`, including its oversize failure and writer compensation checks. Before the app scenarios, it runs these product PostgreSQL tests in the same disposable database service, each in its own isolated schema:

```text
TestPostgresStorageRemoteLocalCompletionAtomicEnqueueAndReplay
TestPostgresStorageRemoteRetryPreservesArchiveAndRevocation
TestPostgresStorageRemoteDownloadFailureAndHeldRetry
```

The gate first checks the exact test-name set with `-test.list`, then requires each PASS marker and rejects SKIP. Production contracts cover the storage transfer routes, optional `remote_target` and `remote_source` request shapes, storage archive metadata, object key, and family capability. Focused local checks are:

```sh
go test -race ./internal/api -run '^TestHostingStorageRemote(ReceiptFaultContract|ObservationFaultContract)$' -count=1
python3 -B acceptance/runtime-hosting-storage/remote_evidence_test.py
python3 acceptance/runtime-hosting-storage/gate_test.py
bash -n acceptance/runtime-hosting-storage/gate.sh
node --check acceptance/runtime-hosting-storage/remote_browser.mjs
```

The gate writes private evidence under `$HOME/.local/share/nerocd/acceptance/runtime-hosting-storage-schedules-20261026/<candidate-sha>.<random>/`. It preserves `runtime/owned-upload.tar` and closed markers before deleting the guest. Full baseline inspections remain private. Before and after comparisons cover host container identity, configuration, state, mounts, and network, with only rotating health log entries excluded. They also cover volume profiles, the existing user UI's executable and configuration fingerprints, listener identity, and successful HTTP responses on port 18081. The gate never changes `/Users/bubble/.local/share/nerocd/user-ui-20261025`.

`safe-receipt.tar.gz` includes only explicitly allowed result, binary hash, helper provenance, UI manifest, preservation summary, browser summary, remote marker, and fixture archive files. It excludes inventories, private baseline documents, browser logs, environment files, and credentials. Missing required runtime evidence remains `not_run`; a standalone fixture compile is not physical qualification.

The comparison reference is Coolify commit `b12d1386f3d458618b4c579babcd1e339c6c40b3`. `VolumeBackupJob` provides stop, archive, resume, then upload behavior. `VolumeBackupRecoveryJob` recovers stopped containers and cleans partial uploads. It is backup-failure recovery, not a new-volume restore implementation. `ScheduledVolumeBackup` records target selection and backup policy, while `VolumeBackupsController` and Livewire `VolumeBackups` expose authorized configuration and actions. This fixture exercises NeroCD's scheduled owned-upload path, separate transfer history, and safe new-volume restore. It does not qualify arbitrary host directories, public S3 providers, or production DNS and ACME.


## Scheduled workflow extension

The remote path now creates a schedule in the existing upload panel. A committed
create response is lost deliberately. The browser retries the saved request,
then edits and disables the same schedule and reads its occurrence history.
Concurrent controller passes must retain one archive operation and helper.
The fixture stops, joins, and restarts the hosting controller before admission.
Viewer schedule reads succeed and viewer schedule creation fails.

The scheduled archive follows the existing denied-upload and retry workflow.
Real SMTP and HTTPS webhook receivers accept the resulting upload failure.
A second scheduled archive encounters an oversized source file. Its actual
archive failure must resume the active writer and send both notifications.
Each transport receipt must contain the exact persisted event and delivery IDs.
No download or restore failure enters the backup notification stream.

Run the gate only after authorization for its exact committed candidate.
Compilation and local PostgreSQL checks do not establish physical qualification.
