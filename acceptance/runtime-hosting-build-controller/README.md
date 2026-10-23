# Compiled hosting build controller gate

Run `bash acceptance/runtime-hosting-build-controller/gate.sh` from the assigned repository root. The default `full` scenario exercises Dockerfile, Railpack, and static builds using a compiled Linux CLI, the registered controller API, PostgreSQL, and disposable Docker-in-Docker.

## Trusted trigger revision scenario

Set `NEROCD_BUILD_CONTROLLER_SCENARIO=trigger-revision` to run the bounded revision qualification instead of the full strategy suite. The default remains `full`.

The optional scenario builds a Linux store-test binary and runs its revision admission, idempotency, capability, historical acknowledgment, and private grant tests against the owned inner PostgreSQL. It then runs public/private controlled TLS Git checkout tests and a compiled CLI static build. The CLI build is admitted at A, the branch moves to B before its first checkout, and the resulting artifact serves A. The unavailable-A cases must fail without using B. The gate prints SHA-256 identities for the CLI and test binaries.

The test CA wrapper and trusted internal repository policy are fixture inputs. No provider push controller is included. No outer PostgreSQL port is published; cleanup retains the existing full-ID, owner-label, volume, and owned-cgroup checks. Tool and image inputs remain pinned by the existing gate. `NEROCD_BUILD_CONTROLLER_PRELOAD_POSTGRES=1` may preload the exact verified PostgreSQL image into the inner daemon.
