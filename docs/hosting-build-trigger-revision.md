# Trusted build trigger revisions

A trusted internal caller can admit a configured hosting build with `RequestConfiguredHostingBuildAtRevision`. The caller supplies a version 1 trigger revision containing a full, nonzero, lowercase Git commit ID (40 or 64 hexadecimal characters). The build snapshot records that revision before any source checkout. A configured full commit must agree with it.

The public build request has no trigger revision field. Manual requests continue to use the existing configured branch or commit. A nil revision preserves the previous snapshot serialization, request hash, and input identity. A nonnil revision participates in the idempotency hash and immutable input identity. Reusing an idempotency key for another revision fails.

The selected runner must advertise `hosting_build_trigger_revision_v1` as well as the existing build capability and, for private Git, its existing private Git capability. Admission, new dispatch, claim, and current execution authority enforce this requirement. Build-configured CLI registration advertises the capability automatically. A persisted revision capability requires explicit build configuration. Existing build identities without it can continue to execute manual builds with nil trigger revisions.

Both public and authenticated HTTPS checkout pass the expected commit to the existing bounded source adapter on the first checkout. A branch moving afterward cannot substitute another commit. An unavailable selected commit fails rather than falling back to the branch. Any later frozen commit argument, source receipt, or artifact must agree with the selected revision. Source repository policy and private grant purpose remain bound to the configured source; this feature does not grant access to another repository or refresh a revoked grant.

Existing receipt replay retains its current identity, placement, creator, and grant checks. Removing only the new capability prevents fresh execution without discarding identical historical acknowledgments. Replay does not repeat checkout or build work.

The [GitHub push controller](hosting-github-push.md) uses this contract after it verifies the provider event and current branch head. Other trusted callers remain responsible for selecting and authorizing the intended revision.

## Qualification

Run focused PostgreSQL tests with an isolated `NEROCD_TEST_DATABASE_URL`:

```sh
go test -race ./internal/store -run '^TestPostgresHostingBuildTriggerRevision' -count=1
```

The existing compiled Linux controller gate has an optional revision scenario:

```sh
NEROCD_BUILD_CONTROLLER_SCENARIO=trigger-revision \
NEROCD_BUILD_CONTROLLER_PRELOAD_POSTGRES=1 \
bash acceptance/runtime-hosting-build-controller/gate.sh
```

It uses the existing disposable Docker-in-Docker and inner PostgreSQL fixture, including its pinned toolchain and exact ownership cleanup. The scenario admits commit A, moves the configured branch to B before starting the compiled runner, then builds and serves A. It also runs public/private controlled TLS Git tests and unavailable-revision rejection. The Git fixture uses a test CA through an operator-owned wrapper; it does not disable production TLS verification. The controller fixture supplies trusted repository routing and seeds its initial account. These are local qualification fixtures, not production provider or ACME evidence.
