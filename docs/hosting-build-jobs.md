# Optional hosting build dispatch jobs

The existing River client supports an optional DB-only build dispatch handler.
This adapter does not wire a controller, register an agent capability, or run
Git, Docker, or a build engine.

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
and bounded `Stop`/`Cancel` lifecycle. Handlers must honor cancellation. This
change adds no migrations and does not automatically enable build dispatch in
the server. The source/build controller must supply the real handler and wire
transactional admission in a later integration slice.
