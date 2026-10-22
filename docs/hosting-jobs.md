# Hosting job infrastructure

This package is infrastructure for a future real hosting preparation consumer.
No production worker is registered or started by the server in this slice.

## Dependency and queue contract

River OSS and `riverdriver/riverpgxv5` are pinned to v0.44.1 for this tested
compatibility baseline, without an upstream long-term-support claim. They require
Go 1.25 language support and pgx v5.10.0; this repository builds with Go 1.26.10. The River modules retain their upstream MPL-2.0 license and are
unmodified. No River Pro module is included.

`PrepareDeploymentArgs` has only `operation_id`. Its kind is
`hosting.prepare_deployment.v1` and queue is `hosting_control`. Opaque operation
IDs contain 1 to 128 ASCII letters, digits, underscores, or hyphens, starting
with a letter or digit. Values, credentials, run plans, and destination
configuration do not belong in job arguments.

Jobs have at most three attempts. Failed attempts retry after one and two
seconds. The worker converts handler errors and recovered panics to fixed
codes before River persists them. Invalid operations cancel immediately.
River's internal logger is discarded to prevent upstream error details from
entering application logs. A future consumer needs safe operational metrics
and audit records in its application operation model.

Uniqueness uses arguments and queue within active job states. It suppresses
simultaneous queued or running duplicates. Completion releases that uniqueness,
and a later insert can run again. Durable business idempotency belongs to the
operation model and handler.

## Consumer integration API

`New(ctx, pool, handler, Config)` requires a real supplied `Handler`. There is
no default handler. `HandlerFunc` adapts an application function. The caller
owns the pool and must keep it open until `Stopped()` closes.

`EnqueueTx(ctx, tx, PrepareDeploymentArgs)` accepts an application `pgx.Tx` and
returns the River job ID. The caller commits or rolls back the transaction.
The transaction must use the same configured endpoint, database, and current
schema as the pool. The adapter rejects mismatches. Both operation changes and
the job become visible after the same commit.

`Start(ctx)` validates the exact migration set before starting work. It does
not migrate. `Stop(ctx)` stops fetching, drains current work, and cancels
handler contexts after `DrainTimeout`. `Cancel(ctx)` cancels immediately.
Both calls are bounded. If a handler ignores cancellation, a call can return
`ErrStopTimeout` while that handler remains active. `Stopped()` closes only
after all services finish. Handlers must honor cancellation.

Defaults are one worker, a one-minute job timeout, and ten seconds to drain.
Configuration permits at most eight workers, a five-minute job timeout, and a
one-minute drain timeout. Cancellation waits at most five further seconds.

The future preparation handler must resolve operation ID to the authorized,
immutable application snapshot and exact destination. It must produce work for
the existing fenced runner protocol. It must not invoke Docker directly.
Runtime worker registration, server readiness for River migrations, and restore
admission holds remain work for that actual consumer slice.

## Owner migration and checksum evidence

The existing `nerocd migrate` owner command applies native migrations, then
River migrations while retaining its advisory lock. Native migration 0044 is
not consumed by this adapter.

`MigrateNextTx` applies exactly one pinned River migration through upstream
`rivermigrate.MigrateTx`, then records its version, name, and checksum in
`nerocd_river_migration_checksums` in that same transaction. The owner CLI
commits each version separately. Upstream deprecates batching migration
versions in a single transaction because some upgrades cannot be batched.
The adapter does not patch vendor SQL.

The first River version and checksum table are atomic. A later migration
failure retains the prior verified prefix. A subsequent owner run validates
that prefix before resuming. Recorded hashes are checked before applying any
new version. The hash covers migration line, integer version, name, and both
up and down SQL. The backup entry also records the pinned River module version.

Missing, reordered, extra, or drifted versions fail validation. Unexpected
migration lines also fail. An existing River schema without adapter checksum
evidence is rejected; the adapter never invents hashes for previously applied
SQL. Migration and validation use the application's current schema, including
non-public schemas.

This table is migration evidence owned by the package adapter. It is not a
second runtime migration service or an orchestration coordination file.

## Backup compatibility

The platform schema identity and backup migration list include deterministic
`river/v0.44.1/main/...` entries with migration names and SQL hashes. Backup
reads require the complete actual River version set and matching checksum
ledger. Restore validates the manifest before execution and the restored
version set and ledger afterward.

A binary containing this adapter rejects an older backup without River
identity entries. Restore an older backup with its matching older image, then
run the newer owner migrations and create a new backup. This preserves the
existing compatible-image discipline. This infrastructure does not yet add
runtime queue registration or a restore admission hold for hosting jobs.

## Upstream references

- [Transactional enqueueing](https://riverqueue.com/docs/transactional-enqueueing)
- [Unique jobs](https://riverqueue.com/docs/unique-jobs)
- [Pinned River v0.44.1 API](https://pkg.go.dev/github.com/riverqueue/river@v0.44.1)
- [Pinned migration API](https://pkg.go.dev/github.com/riverqueue/river@v0.44.1/rivermigrate)
- [Pinned pgx driver](https://pkg.go.dev/github.com/riverqueue/river/riverdriver/riverpgxv5@v0.44.1)
