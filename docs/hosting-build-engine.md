# Local hosting build engine reference

`internal/hostingbuild` builds a local image from an immutable checkout supplied by its caller. Its adapters cover Dockerfile builds, Railpack plans, and directories of static assets. The package has no API route, Git client, deployment controller, registry publisher, or HTTPS integration.

## Host and toolchain contract

The engine runs as an administrator on the Linux host of its fixed rootful Docker daemon. It requires cgroup v2, visibility of the daemon's storage and actual host process tree, an administrator-delegated cgroup directory, and support for OCI archive imports. The runtime fixture uses Docker 29.4.0 with its containerd image store. Classic Docker image stores are not a tested support target.

The rootful BuildKit container is privileged. The host, Docker daemon, Docker executable, Buildx executable, helper image, and administrative state directory are trusted. This boundary does not isolate hostile forks from the host. Build steps have no granted privileged entitlement, host network, host device, production credential, or workload Docker socket. BuildKit's own privileged daemon remains part of the trusted host.

`Config` is operator configuration. It never comes from a build request. `DockerBinary`, `BuildxBinary`, and `Socket` are fixed absolute paths. The engine supplies an empty Docker configuration, fixed Unix endpoint, and explicit process environment. It does not inherit Docker contexts, credential helpers, bearer tokens, SSH agents, registry passwords, or proxy variables.

The supported toolchain is pinned below. Image digests identify the published multi-architecture index. The runtime fixture selects the daemon's native Linux architecture.

| Component | Version | Immutable reference |
| --- | --- | --- |
| BuildKit | 0.34.1 | `moby/buildkit@sha256:99368f3e46177c6405b9740e4461d91cee004128daeecca78d4ceac50ec1da5c` |
| Dockerfile frontend | 1.28.0 | `docker/dockerfile@sha256:bb22d9815c728170f72750f4e5b0d672e06176142e1d602c7e66c050100b7e5b` |
| Railpack frontend | 0.40.1 | `ghcr.io/railwayapp/railpack-frontend@sha256:f1973377693af30c9b37a92c97c661c07b277ccdc6be909213c74c771f8d2d6d` |
| Static runtime | nginx 1.29.3 Alpine | `nginx@sha256:b3c656d55d7ad751196f21b7fd2e8d4da9cb430e32f646adcf92441b72f82b14` |
| Buildx Linux amd64 binary | 0.38.1 | `sha256:e25d5cd675092bb5d1d03632fdc46cdf3fede7b514dc5f023a105e00d3ce5ad4` |
| Buildx Linux arm64 binary | 0.38.1 | `sha256:3b83b3731916ba433feccaaa689fe5bcdc81c641d92c8e6c2ea865dfa141c97a` |
| Railpack amd64 musl archive | 0.40.1 | `sha256:2842de93e68713af9037e0bc0a398d7da78f3b96aa4804303a638db2bc69bd30` |
| Railpack arm64 musl archive | 0.40.1 | `sha256:c24a064b586b8f4f8c2fab44dd5ef19253e4c6cc4e1df793b3ae19cd87f7a5d4` |
| Mise amd64 musl archive | 2026.9.15 | `sha256:e331f3fa3c360c63e996ef70bc183ce178cb16d21f8bf4926c61cf6fa6b9fafd` |
| Mise arm64 musl archive | 2026.9.15 | `sha256:cf9a7c3e64688a7909fa69a4989d3b8daf6dd1b3e583fa0ab7c08139ae6d3dcd` |

Release asset checksums come from the official [Buildx release](https://github.com/docker/buildx/releases/tag/v0.38.1), [Railpack release](https://github.com/railwayapp/railpack/releases/tag/v0.40.1), and [Mise release](https://github.com/jdx/mise/releases/tag/v2026.9.15). BuildKit and Dockerfile references come from the official Docker registry. Railpack's custom frontend comes from its official GHCR repository. `gate.sh` verifies downloaded archive checksums before use. `New` checks the Buildx executable against the architecture's compiled-in checksum.

The pinned BuildKit image must already exist on the fixed daemon. `PlannerImage` is an immutable digest or local image ID of an operator-built helper. That helper contains the verified Railpack CLI at `/usr/local/bin/railpack` and Mise binary at `/usr/local/bin/mise`. The fixture builds this helper from verified archives. The engine does not download administrative executables.

## Requests and receipts

`Request` includes a unique `BuildID`, canonical absolute `Checkout`, immutable `SourceID` and `InputID`, expected `DaemonID`, adapter strategy, single Linux platform, and explicit `Limits`. The caller validates source provenance before admission. Source and input identity strings are caller assertions, not proof of Git ownership.

The engine copies the checkout through `os.Root` into a private bounded context. It rejects symbolic links and special files, including links that remain inside the checkout. It excludes `.git`, common credential directories, environment files, private-key filenames, and package-manager authentication files. The filename exclusions do not detect credentials embedded in arbitrary source. The caller supplies a checkout without production secrets.

`Result.Origin` is `local_build`. The receipt retains source, input, request, and context identity, the fixed daemon ID, the observed image ID, and distinct OCI index, manifest, config, and platform identities. No `RepoDigest` is manufactured. Docker 29 can identify an imported image by its manifest digest while its config has a different digest.

Before import, the engine validates the bounded outer OCI archive's paths, regular-file types, duplicate entries, blob hashes, descriptor media types and sizes, single manifest, expected platform, gzip layers, total expanded bytes, and config diff IDs. Image layer paths cannot traverse above the image root. OCI image symbolic links remain layer metadata. The engine never extracts those links onto the host.

The image ID must be one of the validated archive's exact index, manifest, or config identities and must match daemon inspection on the expected platform. An arbitrary caller-selected image ID is not an input. The fixture proves that direct OCI loading works with its containerd store. An incompatible store fails the build.

A completed `BuildID` replays only when the complete request and operator configuration match, a fresh bounded source copy matches the recorded context digest, the retained OCI archive still validates, and the exact image remains on the same daemon. It returns the original receipt. Missing or changed retained output fails. An incomplete or failed build ID cannot be reused silently.

A receipt establishes immutable retained output identity. It does not promise reproducible dependency resolution from mutable package registries or version ranges.

## Build directories and Dockerfile settings

`Request.ContextDir` selects a directory relative to the immutable `Checkout`. Empty means the checkout root. Dockerfile and Railpack adapters receive only that selected sanitized context. `Request.DockerfilePath` selects a regular Dockerfile relative to that context; empty means `Dockerfile`. `Request.BuildTarget` selects a named Dockerfile stage. Dockerfile path and target are accepted only by the Dockerfile adapter. Railpack remains a separate strategy.

Paths must be canonical relative slash paths, at most 512 bytes and 32 components. Absolute paths, `.`, traversal, repeated separators, backslashes, control characters, and credential-directory names are rejected. An explicit `.` is not a default alias; use the empty field. The engine opens selected directories through anchored `os.Root` handles. Every selected path component must be a real directory, and the Dockerfile must be a regular file. Links and special files are rejected. The target is a bounded ASCII identifier passed as a separate `--target` argument.

All optional settings are omitted from JSON when zero. Existing Dockerfile and Railpack requests therefore retain their exact original request hash. Nonzero context, Dockerfile, or target settings participate in request identity. Changing a setting on a completed `BuildID` fails immutable replay rather than rebuilding it.

## Prepared static assets and SPA routes

`Request.PublishDir` selects public assets relative to `ContextDir`. Empty means that selected context's root. This setting applies only to the static adapter. It does not run a frontend compiler, bake environment values into browser assets, or implement environment-driven rebuilds. Supply assets that have already been prepared and contain no production secrets.

New static builds stage only the selected publish directory. They exclude the normal credential filenames plus Dockerfile instructions, `.dockerignore`, files ending in `.conf`, and Compose YAML files. Names beginning with `.nerocd-static.` are reserved and fail staging instead of overwriting source input. Other public files, including JavaScript and JSON assets, remain available. Filename filtering cannot detect a secret committed under an ordinary asset name.

Generated Dockerfile and nginx configuration live beside a separate `public/` directory in the private build context. The generated Dockerfile copies only `public/` into the document root and places nginx configuration in `/etc/nginx/conf.d/`. It removes the base image's default document root first. Source instructions, generated instructions, and generated configuration are not served as public files. Public file and directory modes are normalized to `0755`, as in the accepted static adapter.

`Request.SPAFallback` opts into fallback for missing paths whose final component has no dot. Such a route serves `/index.html`; the selected publish directory must contain a regular `index.html`. A missing asset-like path, such as `/assets/missing.js` or `/missing.html`, returns 404. Existing assets are served normally. An extensionless missing asset also receives the SPA fallback, so use asset extensions when 404 behavior matters. Without the option, missing routes and assets return 404. The caller cannot supply nginx configuration.

The static receipt records `ContextPolicy=static-assets-v1`. That fixed policy is included in the request hash, and `ContextDigest` covers the selected sanitized public assets before generated files are added. Replay reconstructs identity and staging from the owned receipt's policy. A receipt without `ContextPolicy` uses the accepted legacy request serializer and legacy context staging, verifies its retained OCI artifact and daemon image, and returns the original receipt without invoking a builder. It does not adopt the new static packaging policy. Changed settings, changed source/input/config identity, unknown policies, or inconsistent receipt fields fail closed. No existing `BuildID` silently rebuilds during this transition.

## Adapters

The Dockerfile adapter uses the checkout's `Dockerfile` with the pinned frontend forced through `BUILDKIT_SYNTAX`. It grants no build entitlement and passes no build secrets, SSH socket, custom network, extra context, output destination, or caller build arguments.

The static adapter packages the prepared assets and fixed nginx configuration described above, using the pinned nginx base. It performs no frontend compilation.

The Railpack adapter runs `prepare` in a separate helper with a read-only root filesystem, read-only source mount, no Docker socket, no capabilities, no new privileges, a PID cap, and bounded memory-backed temporary/output storage. The helper has ordinary bridge networking for package version metadata. It has no host network or inherited credentials. The source can request language/version planning within Railpack's capabilities. Secrets in the generated plan are rejected. The pinned custom frontend executes the generated plan through the same owned BuildKit engine.

The helper copies the verified Mise binary into Railpack's expected private cache path. The helper's temporary filesystem permits execution of that trusted binary. Build commands do not execute against a writable host checkout.

## Resource and disk limits

`CPUQuota` uses a fixed 100000-microsecond period. `MemoryBytes` is RAM, and `SwapBytes` is additional swap. `Config.CgroupRoot` names an administrator-delegated directory beneath `/sys/fs/cgroup`. Its `cpu`, `memory`, and `pids` subtree controllers must already be enabled, and it contains no direct process. The engine does not enable controllers on ancestors or move unrelated processes. Unsupported delegation fails preflight.

Each build creates an owned subgroup beneath this root. The subgroup's actual `cpu.max`, `memory.max`, and `memory.swap.max` must be finite and within the aggregate limits before work. Builder, helper, and BuildKit OCI executor groups all use this explicit ancestor. The builder and helper share the daemon host's cgroup namespace so worker paths resolve under the delegated root. Every sampling pass checks process descendants for escaped cgroup membership and traverses the actual enforcing group's process subtree. The runtime gate finds the uniquely marked actual Dockerfile `RUN` process beneath this ancestor and observes kernel CPU throttling. A separate memory workload must produce an observed ancestor OOM kill event.

Railpack splits CPU, RAM, and swap between the builder and planner. The helper and builder each receive half of the aggregate budget and remain beneath the shared aggregate cgroup. Idle helper capacity stays reserved until cleanup. Railpack requires at least 512 MiB of aggregate configured RAM, and workloads can need more. Limits apply to build execution containers. The engine process, Docker daemon, fixed pre-provisioned tool images, and unrelated workloads consume separate host resources.

A fixed `/run/nerocd-hostingbuild.lock` prevents concurrent engine builds across processes and state directories. The private `StateDir` also has a single-writer lock and must remain the same across restarts. Changing the configured state directory requires operator reconciliation of outstanding resources and cleanup intent.

`DiskMode` accepts only `monitored`. Requested quota mode fails. No filesystem quota is installed or claimed. Explicit budgets cover context bytes and file count, planner output, command logs, exported archive bytes, expanded layer bytes, and owned workspace/builder volume usage. Planner output has a tmpfs size cap. Logs have a hard writer cap of at most 64 MiB. OCI metadata has a 4 MiB per-document cap, and planner output is limited to at most 16 MiB.

Admission requires the nominal owned-usage and expanded-image budgets to fit above the reserve on both workspace and daemon filesystems. During execution, sampling checks the reserve on workspace and builder storage, owned workspace and volume size, current export size, cgroup containment, and cancellation. The sample interval must be between 10 milliseconds and one second. Scans check cancellation before and after directory batches of at most 128 entries, and before each entry or process metadata read. Each scan permits at most one million work units and 256 directory levels. Process discovery permits at most 4096 unique processes and bounded metadata reads. Exceeding a scan budget fails the build. Cancellation joins the scanner before owned cleanup starts. Local filesystem syscalls must return; the engine does not abandon a scanner goroutine to simulate an I/O timeout. Disk writes can exceed a threshold between samples and during command cancellation or Docker stop. There is no finite byte overshoot guarantee on an ordinary host. Other workloads can also consume free space. BuildKit GC is not a disk quota.

The deadline cancels the build client, then cleanup stops the exact owned builder and planner, kills the exact owned container if stop is unsuccessful, verifies stopped state, removes those containers, and removes only their verified owned volume. Cleanup has a separate 30-second bound. Docker daemon identity and resource ownership are checked before destructive operations. The engine then kills any remaining process in the exact owned cgroup subtree, verifies that the group is empty, and removes only its empty subgroups. Uncertain cleanup blocks reuse. The engine never prunes globally or removes completed images that workloads might use.

## Recovery and validation

A durable `cleanup_unconfirmed.json` intent exists before resource creation. Cleanup removes that intent only after Docker confirms owned cleanup. Daemon loss, a crash, unknown ownership, or uncertain removal leaves the intent and blocks new builds. The state directory is owner-only and rejects symlink ancestors.

The package has no automatic recovery override. Administrative recovery requires the fixed daemon, intent token, exact container identities, and exact owned volume to be reconciled before the operator removes the intent. A successful image import followed by cancellation can leave a retained local image. Cleanup does not infer permission to delete it.

The runtime fixture is `acceptance/runtime-build/gate.sh`. Its outer DinD container shares the disposable Docker VM's host PID namespace so the engine can inspect only owned cgroups and process descendants. The engine verifies that an inspected builder PID belongs to the expected owned cgroup before building. A PID namespace or cgroup view that does not match fails preflight. It creates a labeled disposable DinD daemon and publishes no host port. It downloads pinned tools, builds the helper, runs Linux package tests, and exercises real builds and HTTP serving. Its cleanup removes only the labeled fixture container and fixture image. It does not stop user workloads or run Docker prune.

The required candidate checks are package unit tests, race tests, vet, Linux cross-compilation, pinned changed-code lint, and the disposable runtime gate. The gate names descendant CPU/memory enforcement, expanded output rejection, timeout cleanup, lost-daemon cleanup admission, context escape, independent workload survival, and owned-resource cleanup explicitly. Independent code hygiene, specification, and critical security reviews still apply to the final candidate.

The runtime fixture also compiles the accepted engine at `d63108b5dc4882fd6471fa8867d7278a3ccced78` to create a genuine legacy static receipt and OCI artifact. The candidate replays it with the configured Buildx executable temporarily absent, proves no builder appears, and rejects changed settings. Current settings fixtures exercise nested Dockerfile contexts, custom files and named targets, selected publish directories, SPA deep links, missing assets, private/generated-file exclusion, changed-setting conflicts, and survivor HTTP through failed builds.
