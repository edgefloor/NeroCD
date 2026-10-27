<a id="hosting-catalog-contract"></a>

# Install Linkding from the catalog

The Linkding catalog entry translates the Coolify template at
`b12d1386f3d458618b4c579babcd1e339c6c40b3` into an ordinary image application.
The installed application uses the existing resource, destination, deployment,
managed-secret and owned-storage APIs. It has no automation TaskTemplate.

## Pinned definition

The immutable version is `coolify-b12d1386.linkding-1.47.0.v1`. It selects
`docker.io/sissbruecker/linkding@sha256:e35cb50e0581178f245125ffaa909c565416c94c9f22ace305a7d234a4345522`,
container port 9090, readiness `/health`, and a retained local Docker volume at
`/etc/linkding/data`. The runtime configuration references separately generated,
encrypted `LD_SUPERUSER_NAME` and `LD_SUPERUSER_PASSWORD` secrets at version one.
The original template remains byte-for-byte available in the installation copy.
It is not executed as Compose or as a shell script.

The original comes from
[Coolify's Linkding template](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/templates/compose/linkding.yaml),
licensed under [Apache License 2.0](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/LICENSE).
The typed translation replaces `latest` with a digest, maps generated values to
managed-secret references, replaces its TCP shell health check with Linkding's
HTTP database health endpoint, and uses NeroCD's owned-volume provenance. The
Coolify `SERVICE_URL_LINKDING_9090` declaration is URL metadata, not a required
Linkding environment variable. NeroCD uses its ordinary explicit route workflow.
Arbitrary or pasted Compose imports remain incomplete and are rejected.

## Install and deploy

1. Select the project and shared environment under Project hosting.
2. Select Linkding in Install from catalog, enter a name, and select a granted
   destination. Project owner access is required.
3. Install the application. The transaction saves resource placement, storage,
   encrypted credentials, runtime references, attribution and the exact request.
4. Open the saved application deployment panel. Its catalog container port and
   readiness settings are prefilled. Enter the runner-local host port and review
   the first storage deployment before submitting it.
5. After the deployment succeeds, use the ordinary route controls for HTTPS
   access, or the runner-local endpoint on an explicitly authorized local target.
6. Use Reveal login credentials to retrieve the saved username and password.
   This calls the existing managed-secret reveal operation explicitly. Values
   are never included in installation metadata or query caches.

The request identity survives an unconfirmed response. Retry uses the same
project, environment, destination, name, template and immutable version. Current
owner, destination and secret authority is checked before replay. Concurrent
identical requests commit one installation. Catalog refreshes cannot replace an
installation's original bytes, hash, normalized definition or component references.
A resource may later change through the ordinary resource APIs; installation
metadata continues to describe the original install.

Linkding's `create_initial_superuser` creates a missing username and skips an
existing username. Changing the runtime password does not rotate an existing
account password. Redeployment keeps the original startup references and volume,
which includes `data/db.sqlite3` and `data/secretkey.txt`. Password changes inside
Linkding follow Linkding's account controls.

## Combined runtime and storage family

Ordinary image applications can combine saved runtime configuration and owned
storage through `public-image.owned-storage.runtime-env.release-ports.v1`, compiler
`image-hosting.owned-storage.runtime-env.v1`, and capability
`hosting_owned_storage_runtime_v1`. Runtime environment configuration on the runner
also advertises the existing storage capability. Admission still requires image,
release-port, storage and runtime-environment capabilities and current authority.

The compiler pins the exact runtime generation/hash and storage creation origin.
It permits image applications only, rejects database/local-build fields, and
sets `RetainPrevious=false` to enforce one writable-volume user. Existing
compilers continue to reject mixed fields. Storage materialization is journaled
before apply admission. Protected runtime delivery recognizes a prepared storage
hold only after exact writer materialization evidence exists. Values reach the
Docker create process through stdin and remain outside plans, audit and journal.
Recovery uses committed provenance and observations; replay never fetches values
to recreate a container. Image rollback cannot undo application writes to data.

A catalog draft validates the installation name before assigning an immutable
request ID. A definitive HTTP 400 rejection permits correction and a new request.
An ambiguous response retains the original request and disables edits so a retry
cannot silently create a second installation.

After a combined owned-storage/runtime-environment deployment is prepared, a
runner restart retains an explicit ownership barrier. It does not request a new
secret delivery, create storage, or recreate a container. Owner-requested
read-only reconciliation is checked before that barrier and may retire journal
evidence only when the controller returns an exact trusted terminal receipt.
The runner cannot accept unrelated work while this unresolved global startup
barrier remains. Observation does not itself release uncertain or restored
ownership; the existing trusted recovery boundary still applies.

<a id="upstream-behavior-and-remaining-scope"></a>

<a id="current-catalog-criteria"></a>

## Supported scope

Linkding 1.47.0 is the only catalog entry. The installation keeps its original
template bytes, normalized definition, attribution, initial name and placement,
secret references, component identities, and resolved digest. Later resource
changes do not rewrite that installed copy.

Catalog search, filtering, a general template editor, catalog refresh and version
upgrades are unavailable. The entry shows its data mount and credential names;
it does not provide general required-variable, domain, architecture, resource-needs,
or component-role presentation. General Compose import is unsupported.

## Back up and recover into a separate application

The [catalog backup and recovery workflow](hosting-catalog-recovery.md) pins
the healthy deployment's runtime generation/hash and reuses the exact existing
source writer. It does not fetch secret values or recreate that writer. Restore
copies exact managed-secret references into a distinct resource and volume on the
same destination. It does not copy catalog identity, routes or readiness results.

1. Open the installed application's upload backup controls, review the local-disk
   retention limit and acknowledge that writers may stop.
2. Request a backup and wait for its successful result and resumed source writer.
3. Select that backup and restore into a new named resource.
4. Wait for byte validation to clear the target hold. Open the restored application
   and review its ordinary deployment, storage and pinned runtime settings.
5. Deploy the restored application. Verify a fresh login, expected saved data and
   a new application write before relying on the recovered target.

The physical fixture completed those checks with real Linkding sessions and
bookmark writes isolated from the source. It also verified UID/GID and modes,
completion replay without duplicate helpers or materialization, and owner recovery
after an injected exact-writer resume failure. A failed resume keeps a writer
obligation and mutation hold until authorized recovery settles it. The operator
must preserve the source and follow the existing owner recovery controls.
See [the runtime checks and limits](../acceptance/runtime-hosting-catalog/README.md#final-physical-qualification).
This qualifies the pinned Linkding profile only. Local archives remain subject to
same-destination disk retention; no host-disk-loss or cross-host recovery is implied.

## Source and runtime evidence

The [catalog definition](../internal/domain/hosting_catalog.go),
[installation store](../internal/store/postgres_hosting_catalog.go), and
[catalog UI](../web/app/src/pages/HostingCatalog.tsx) define the current entry.
The [catalog runtime guide](../acceptance/runtime-hosting-catalog/README.md)
records the passing install, replacement, backup, and restored-application checks.
Its profile is pinned Linkding 1.47.0, native Linux arm64, trusted rootful
classic-store Docker/overlay2, one destination, and a private fixture CA.
Other architectures, production ACME, arbitrary applications, catalog upgrades,
Compose, cross-host recovery, and destination disk loss are unqualified.
