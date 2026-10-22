# Durable hosting build logs

The server accepts fenced nonsecret build output at
`POST /api/v1/runners/builds/events`. This backend does not install a runner log
adapter, streaming transport, or log viewer. Those are separate integrations.
Existing source/build execution and completion contracts remain unchanged.
Generic runner event and scalar log endpoints now reject build tasks, including
nested build authority fields. A client must use the typed endpoint for build
output; generic event acknowledgments cannot settle a build log journal.

## Ingest and acknowledgment

The request has `authority` containing `build_id`, `run_id`, `lease_id`, `attempt`,
and `fence`, plus `events`. Each event has `event_key`, positive requested
`sequence`, `component`, `stream`, `message`, and optional `source_timestamp`.
Components are `build`, `buildkit`, and `railpack`. Streams are `system`, `stdout`,
and `stderr`. Engine components require the building phase; the build component
can report in queued, sourcing, or building phases. Canceled, cleanup-required,
and terminal builds cannot append new output.

Batches contain 1 through 64 distinct keys and at most 256 KiB of canonical JSON.
Keys contain 1 through 128 bytes and no whitespace or control characters.
Messages contain at most 16 KiB of valid UTF-8. Requested and allocated sequences
are positive signed 32-bit integers. Allocation fails atomically at exhaustion.
Source timestamps must use RFC3339 dates within years 1 through 9999. The server
converts them to UTC and truncates fractional seconds to PostgreSQL microsecond
precision before hashing and storage. Timestamps never control ordering,
authority, or retention.

A successful response contains the full `authority` and ordered `receipts`, each
with `event_key`, `requested_sequence`, allocated server `sequence`, and
`body_hash`. It contains no messages. The hash is SHA-256 of Go canonical JSON
for the object `{"authority": <full tuple>, "event": <canonical event>}` using
the documented field order of the domain DTOs. It includes the component,
normalized optional timestamp, message, and requested identity. A runner adapter
must canonicalize its submitted event identically and compare the full tuple,
every ordered key/requested sequence/hash and the server sequence before
acknowledging its durable journal. Matching only an event key is insufficient.

Exact already-committed receipts can be acknowledged after lease expiry,
cancellation, or creator/grant revocation. The original full tuple and body must
match. This acknowledges persisted output only. A changed body, forged tuple,
or a batch mixing old and new events requires current authority and fails as a
whole when that authority is lost. Receipts retain only identity and hashes after
message retention, never a duplicate plaintext copy.

## Reconnect reads

Current project viewers can call
`GET /api/v1/hosting-builds/{id}/logs?after_sequence=0&limit=200` on every reconnect.
Project scope comes from the persisted build. Runner tuples, internal event keys,
hashes, policy, grants, and target identity are absent from this response.

The page contains `items`, `next_sequence`, `earliest_available_sequence`,
`deleted_through_sequence`, `gap`, `terminal`, `truncated`, and `history_complete`.
Items contain sequence, component, stream, message, server `created_at`, optional
source timestamp, and optional `message_truncated`. Items sort by durable server
sequence. The row limit is 1 through 200; cursors are 0 through 2147483647.
Unknown and repeated query parameters are invalid.

The encoded JSON response is at most 256 KiB, including escaping and metadata.
`truncated` means more retained rows are available. The next cursor never skips
an undelivered retained row when the row or byte budget cuts a page. `gap` means
retention removed requested history or old deletion history is unknown.
`earliest_available_sequence` is zero when no rows remain. An empty page can
advance only through a verified deletion watermark. `terminal` means succeeded,
failed, canceled, or cleanup-required; no new log execution is authorized in
those states.

Legacy messages are projected to at most 16384 Unicode characters in SQL before
loading, then clipped at a valid UTF-8 boundary to 16 KiB. A shortened message has
`message_truncated: true`; this does not set the page's `truncated` or `gap` flags.
New typed messages already fit the byte bound and are preserved in full.

## Sequence history and retention

Migration 0051 adds counters and immutable receipts alongside `run_logs`. New
canonical build dispatch creates a complete empty counter in the same transaction
as its immutable task relation. Counters and receipt identities cannot be deleted
or rewound. Retention removes only an eligible sequence prefix for builds and
advances the deletion watermark in the same transaction as deletion, its audit,
and its replay receipt. Retrying that retention receipt does not delete again.

Builds bound before migration have `history_complete: false`. The migration
records the maximum still-visible sequence but does not invent evidence for
already-deleted rows. Reads report unknown history as a gap; typed ingestion is
unavailable for those builds. Request a new build to obtain a complete counter.
This limitation concerns the new log adapter, not authorization to execute an
existing build through its established phase APIs.

Ingest locks task and exact historical lease, then current placement and build,
then log allocation state. Every first write rechecks current creator, source,
target, enrollment, grant, cancellation and database-time lease authority after
all potentially blocking inserts. Legacy append paths acquire the task before
the log lock, matching completion and retention serialization. PostgreSQL is
required; memory-backed typed log operations return unavailable before writing.
