# Hosting catalog contract

The first catalog entry translates the Coolify Linkding template at
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

Registry v2 reads on 2026-10-26 resolved version `1.47.0` and `latest` to that
index. The Linux arm64 manifest is
`sha256:36318731b9be48b516c0d27cbebd0d98588e4d4a849cbf5cc7cbdcb9a41ea02e`;
the amd64 manifest is
`sha256:b44256a4ff37e4cb1a39c40d3a5b560337f1477a55428b3320e813d6004d8001`.
Registry metadata is separate from runtime evidence. The Linux arm64 fixture
passed at `3b0d9bbc5bd4159379d58aabe412f55f71bf147e`; see
[qualification evidence](../acceptance/runtime-hosting-catalog/README.md#qualification-evidence).
The extended workflow is physically qualified at `dd19157ce2e454b2ff6ad6c2d74b028a7dda9c86`;
the catalog work is integrated in accepted commit `8b0ae4c17ade2145f273963d38bafe6c0057c31d`. The original physical evidence remains labeled with its tested revisions.

The original comes from
[Coolify's Linkding template](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/templates/compose/linkding.yaml),
licensed under [Apache License 2.0](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/LICENSE).
The typed translation replaces `latest` with a digest, maps generated values to
managed-secret references, replaces its TCP shell health check with Linkding's
HTTP database health endpoint, and uses NeroCD's owned-volume provenance. The
Coolify `SERVICE_URL_LINKDING_9090` declaration is URL metadata, not a required
Linkding environment variable. NeroCD uses its ordinary explicit route workflow.
Pasted Compose source storage is described in [Managed Compose sources](hosting-compose.md). Its deployment qualification is separate from the Linkding catalog workflow.

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

Archive, restore and backup qualification for this combined family is separate
from the original install/redeploy receipt. The reviewed
[backup and recovery extension](hosting-catalog-recovery.md) adds source support;
its bounded PostgreSQL and physical qualification is recorded below.


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

The catalog acceptance gate includes a connected browser against the candidate
Go-served assets, an identical retry after a committed response is lost, explicit
credential reveal, ordinary deployment review, viewer denial and responsive
layouts. The same fixture selects an ordinary managed route and separately
verifies private-CA application HTTPS and route continuity across replacement.
Actual Linkding login and bookmark persistence use fresh HTTP cookie jars after
each replacement. These are distinct evidence layers; source checks and fixture
compilation alone do not qualify the physical workflow.

## Upstream behavior and remaining scope

At the pinned Coolify revision, `templates/compose/linkding.yaml` declares a
single service, generated username/password variables and `linkding_data`.
`bootstrap/helpers/parsers.php` retains saved variables with `firstOrCreate` and
maps local named volumes to stable resource-scoped names. The API's
`app/Http/Controllers/Api/ServicesController.php` accepts catalog service types
and raw Compose. `app/Actions/Service/StartService.php` reparses the saved service
and starts it with Docker Compose while keeping existing volumes.
NeroCD translates this one entry into its ordinary Go resource/deployment APIs.
Lease fences, protected secret delivery through stdin, owned-volume origin and
journal replay remain part of that existing execution contract.

The installed copy stores the template ID/version, original bytes and hash,
normalized definition, initial placement, generated secret references, component
identities and digest. Initial name and placement choices are preserved, but a
general template-change editor is absent. Stored copies are independent of the
current catalog definition. An actual catalog-refresh workflow and upgrade test
remain absent, so redeploy evidence does not qualify catalog refresh or upgrade.

Search and filtering are missing. Credential names and later route controls cover
only part of required-variable/domain presentation. Storage review covers the
retained volume in this profile. Architecture, resource-needs and component-role
presentation remain missing. General and pasted Compose are incomplete. Ordinary
authorization and storage rules are qualified only for this Linkding profile.
A versioned multi-entry fixture matrix remains missing. The historical parity
rows and aggregate A21/A22 gates remain unchanged.

## Back up and recover into a separate application

The [catalog backup and recovery contract](hosting-catalog-recovery.md) is
physically qualified at `dd19157ce2e454b2ff6ad6c2d74b028a7dda9c86`. Backup pins
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
See [the exact receipt and limits](../acceptance/runtime-hosting-catalog/README.md#final-physical-qualification).
This qualifies the pinned Linkding profile only. Local archives remain subject to
same-destination disk retention; no host-disk-loss or cross-host recovery is implied.

## Current catalog criteria

The frozen consolidated parity audit at accepted-main cutoff
`f96d8c6267fcab2273b6f748d09801931660ceaf` excludes this later catalog
qualification. The table records current presence and bounded runtime evidence
without changing that snapshot, its historical rows, its score or any aggregate
gate. The table uses the original installation receipt; the later backup/restore
qualification below does not add audit credit.

The physical evidence is the exact `TestHostingCatalogLinkdingCompiledRunnerHTTPS`
pass at `3b0d9bbc5bd4159379d58aabe412f55f71bf147e`.
See [the retained receipt and hashes](../acceptance/runtime-hosting-catalog/README.md#qualification-evidence).
Source-only presence below comes from `internal/domain/hosting_catalog.go`,
`internal/store/postgres_hosting_catalog.go` and
`web/app/src/pages/HostingCatalog.tsx` at that source. The separate PostgreSQL
contracts are `TestHostingCatalogPostgresAtomicReplaySecretsAndDeployment` and
`TestHostingCatalogPostgresDenialAndRollback`. The original full PostgreSQL run at that source finished with 567 passing and
three failing parent tests. Those failures remain historical. The separately
repaired extended source has the final bounded checks recorded in the acceptance
guide; no passing full-suite claim is made here.
A source test name alone does not establish a passing execution.

| Criterion | Current bounded assessment | Evidence and remaining limit |
| --- | --- | --- |
| `R50-C01` Persist catalog template identifier | implemented | Immutable installed template ID is persisted. |
| `R50-C02` Persist catalog source version | implemented | Immutable source version and upstream commit are persisted. |
| `R50-C03` Persist original definition | implemented | Original template bytes and hash are stored in each installation. |
| `R50-C04` Persist user changes | partial | Initial name and placement choices are stored; general template-edit history is absent. |
| `R50-C05` Persist generated values | validated | Generated values are encrypted managed-secret versions, referenced by the installed runtime configuration. |
| `R50-C06` Persist component identities | validated | Resource, binding, storage and secret identities remain fixed across replacement. |
| `R50-C07` Persist resolved images | validated | The exact image digest runs on the qualified Linux arm64 profile; other architectures lack runtime evidence. |
| `R50-C08` Generate password once | validated | One committed installation creates one password and replays identical requests without replacing it. |
| `R50-C09` Generate stable identifiers once | validated | Stable resource and secret IDs and owned-volume origin survive identical retry and redeploy. |
| `R50-C10` Preserve credentials on redeploy | validated | Fresh login and unchanged secret IDs, versions and values prove credential continuity on redeploy. |
| `R50-C11` Preserve credentials on catalog refresh | partial | Stored original/normalized copies are independent of current catalog definitions; no actual catalog-refresh workflow is qualified. |
| `R51-C01` Search catalog | missing | Catalog search is absent. |
| `R51-C02` Filter catalog | missing | Catalog filtering is absent. |
| `R51-C03` Show required variables | partial | The entry exposes credential names and explicit reveal; general required-variable presentation is absent. |
| `R51-C04` Show required domains | partial | Ordinary route controls provide a domain after install; required-domain presentation remains incomplete. |
| `R51-C05` Show required storage | implemented | `HostingCatalog.tsx:139` displays the selected mount path as Persistent data before install. The retained physical/unit evidence has no explicit assertion of that displayed content, so presentation is not runtime-validated. |
| `R51-C06` Show supported architecture | missing | Supported architecture presentation is absent; Linux arm64 runtime evidence is separate. |
| `R51-C07` Show resource needs | missing | Catalog resource-needs presentation is absent. |
| `R51-C08` Show component roles | missing | Catalog component-role presentation is absent. |
| `R51-C09` Install via same engine as pasted Compose | missing | Pasted Compose engine support is incomplete; the closed typed image translation does not establish it. |
| `R51-C10` Enforce ordinary authorization | validated | Current owner/destination authority gates install and replay; the physical browser proves viewer denial. |
| `R51-C11` Enforce ordinary storage rules | validated | Ordinary owned-storage admission and one-writer release rules apply to the qualified profile. |
| `R53-C01` Qualify useful initial catalog by published fixtures | partial | One published Linkding fixture passes; useful initial catalog breadth remains open. |
| `R53-C02` Expand catalog through versioned fixture matrix | missing | A broader versioned template fixture matrix is absent. |
| `R53-C03` Record imported origin/version | implemented | Installed copies record upstream repository, commit, path and immutable translation version. |
| `R53-C04` Retain required template attribution | implemented | Original upstream template and Apache 2.0 attribution are retained. |
| `R53-C05` Verify installation before support claim | validated | Connected candidate UI, actual API/runner deployment and application login pass for one pinned entry. |
| `R53-C06` Verify persistence before support claim | validated | Fresh authenticated sessions read the real saved bookmark after redeploy and failed-deployment recovery. |
| `R53-C07` Verify updates before support claim | partial | Same-digest redeploy is qualified; a catalog version upgrade and update matrix remain absent. |

Every validated row applies only to Linkding 1.47.0 on native Linux arm64,
rootful Docker with classic image store/overlay2 and the private fixture CA.
These installation rows supply no production ACME, other architecture, general
Compose, catalog upgrade or template-breadth claim. Separate-target backup/restore
is qualified only by the later exact receipt above. Complete R50/R51/R53 and
A21/A22 remain open.
