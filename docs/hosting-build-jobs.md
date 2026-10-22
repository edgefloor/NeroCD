# Hosting build dispatch jobs

The existing River client supports an optional DB-only build dispatch handler.
The server supplies its DB-only build controller only when an explicit trusted
build policy is enabled. The River adapter never runs Git, Docker, or an engine.

The fixed kind is `hosting.prepare_build.v1`. `PrepareBuildArgs` contains only
`build_id`, an opaque identifier using the existing hosting job spelling:
one to 128 ASCII letters, digits, underscores, or hyphens, starting with a
letter or digit. Source bodies, credentials, plans, and host configuration do
not belong in job arguments.

`New` still requires a valid deployment `Handler`. It registers the build worker
only when that same supplied handler also implements
`BuildHandler.PrepareBuild(context.Context, string) error`. There is no default
build handler or second client. `EnqueueBuildTx` returns unavailable without
inserting when support was not supplied.

The adapter inserts into `hosting_control` using the caller's `pgx.Tx`. Its
endpoint, database, and current schema must match the client. The caller owns
commit or rollback; rolled-back jobs cannot execute. The handler resolves the
BuildID to persisted authorized state and performs DB-only task dispatch.
Durable business idempotency and exact-target admission remain its responsibility.

Build jobs share the existing three-attempt limit and retry after one and two
seconds. Handler errors and recovered panics become fixed codes before River
persists them. Invalid operation errors cancel immediately. Active queue states
deduplicate identical arguments; completion permits a later job with the same
BuildID. Queue deduplication does not provide durable business idempotency.

Build work uses the existing client concurrency, job timeout, migration readiness,
and bounded `Stop`/`Cancel` lifecycle. Handlers must honor cancellation. This component adds no migrations. Build admission remains disabled unless
`NEROCD_HOSTING_BUILD_POLICY_FILE` supplies explicit validated operator policy.
See [server build control](hosting-build-control.md) for configuration and limits.

The real controller uses `RequestHostingBuild` with `EnqueueBuildTx` in the same
transaction as snapshot and audit. `PrepareBuild` uses `DispatchHostingBuild`
to insert exactly one canonical task and immutable target relation in one
transaction. No external source or engine effects run on the server.

The existing bounded recovery loop discovers queued builds with no bound run
independently of River retry exhaustion or job cleanup. Each pass rotates its
bounded prefix. Durable task binding prevents a recovered job from creating a
second task. Permanent preflight authority refusal records a fixed failed outcome;
transient database failure leaves the accepted row discoverable. River job
completion means dispatch finished, never that a build succeeded.

Startup without build policy preserves deployment and proxy workers while leaving
build worker registration and public build admission unavailable. Worker recovery
and queue shutdown use the existing cancellation/drain lifecycle before pool close.
