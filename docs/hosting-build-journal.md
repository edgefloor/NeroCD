# Durable build-agent journal records

The runner journal can retain build intent and exact phase requests in additive
format v5. This change provides durable storage only. It does not add a build
executor, credential transport, server client, capability, or replay controller.

Existing v1–v4 journals remain readable with their prior entry semantics. Fresh
journals still use v1; existing non-build appends retain their prior version
rules. Appending a build record advances the format to v5, and later proxy or
image appends cannot lower it. Removing all build records leaves v5 in place so
older binaries do not silently reinterpret its history.

## Records and immutable intent

`JournalSnapshot.Build` holds `JournalBuild` entries with a stable ID, complete
`AttemptIdentity`, canonical build ID, phase, and timestamp. The attempt's run ID
must equal `build_run_` plus that build ID. Lease timestamps remain evidence of
the original local attempt; expiry does not authorize deletion or new effects.

Each phase has exactly one payload:

- `attempt_intent` stores only `InputHash`, a canonical snapshot digest. It
  contains no repository URL, credentials, checkout path, or complete snapshot.
- `source` stores the exact shared `SourceCommitRequest`, including its stable
  receipt ID and complete authority. It has no expected revision.
- `result` stores the exact shared `CompleteRequest`, including expected revision,
  completion state, fixed failure code, and any nonsecret artifact identity.

The request receipt ID must equal the journal entry ID, and its full authority
must match the entry attempt. Source commit and digest shapes, completion
combinations, artifact digests, and available build/source bindings are checked.
`InputHash` binds the complete snapshot; it is not assumed to equal the source
receipt's `InputID`.

When source and result records coexist, their source ID, input ID, and source
receipt hash must agree regardless of append order. Results remain individually
validated and replayable after an acknowledged source record has been removed.
This local consistency check supplies no missing provenance or server authority.

## Persistence, ownership, and settlement

`AppendBuild` is append-before-effects or append-before-send storage. Exact
same-ID appends are idempotent; changed content conflicts. A build record ID
cannot collide with any other journal family, in either append order. Mixed
journals preserve this uniqueness during older entry APIs' writes too.

Input requests, returned entries, and snapshots are deeply copied, including
artifact pointers. Records share the journal's 8,192-entry and 8 MiB limits;
each build document is bounded to 64 KiB. Failed append or acknowledgment writes
restore the prior in-memory records and format version using the existing secure
atomic-rewrite persistence boundary. Owner-only paths, symlink resistance,
single-writer locking, and filesystem synchronization are unchanged.

`DiscardAttempt` refuses attempts with unresolved build records, including when
only lease timestamps differ. It cannot infer settlement from expiry, a missing
response, or ordinary event completion. `AckBuild` explicitly removes one record
only after the caller verifies the exact committed receipt or establishes settled
intent and cleanup. It does not automatically acknowledge related records.

A future reviewed build replay path must reconcile these records and current
server authority before enabling normal build execution. This storage slice
performs no network calls and does not claim that private Git builds execute.
