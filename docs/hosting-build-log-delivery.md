# Hosting build output delivery

The public and granted private HTTPS Git build executor sends engine output
through the typed `POST /api/v1/runners/builds/events` endpoint. It requires both
`BuildWithOutput` and `BuildEvents` before it acquires source or starts a build.
Local artifact deployment keeps its existing engine call.

The adapter consumes serialized raw chunks of at most 16 KiB. It applies
`runner.Redactor` before UTF-8 framing, journal persistence, or transport.
The runtime copies its authenticated token into memory and redacts its raw,
base64, base64url, and hexadecimal forms. The token is bounded to 4096 bytes.
The adapter accepts at most 16 known materials, each at most 4096 bytes, and
keeps separate streaming state for the nine component and stream combinations.
Typed output transport does not fetch credentials. The private HTTPS runtime fetches attempt-scoped Basic/PAT credentials through a separate protected
endpoint and registers them for redaction before checkout. See
[private HTTPS setup](hosting-build-runtime.md#enable-private-https-git).
The [private runtime fixture](../acceptance/runtime-hosting-local-build/README.md)
covers Dockerfile and Railpack builds through the CLI and browser. Other provider
credential integrations are unsupported.
Unknown secrets printed by source code or tools can appear in output.

Each framer retains at most three redacted UTF-8 tail bytes between callbacks.
Invalid bytes and NUL become U+FFFD. Frames contain at most 16 KiB of valid
UTF-8, without buffering whole lines. Source timestamps remain absent because
the engine supplies no source clock. Only normal completion with an active
context flushes redactor and framer tails. Cancellation never starts a detached
flush.

`build_logs` and `build_log_cursors` require journal format 7 or later. Each log record
contains one canonical typed event, its full attempt identity, and a journal
creation time. Allocation of the event key, requested sequence, cursor, and
append occurs under one journal mutex and one atomic persistence operation.
A persistence failure restores the previous in-memory state. The journal
retains its existing secure storage, 8192-entry and 8 MiB limits. Cursors count
toward the entry bound. Versions 1 through 6 remain readable, but a journal with
log evidence requires at least version 7 so earlier readers reject it.

The adapter sends one retained event per request. This stays below the
64-event and 256 KiB canonical JSON limits, including escaping. Each request
has the configured timeout. Transient failures retry the identical retained
batch with a context-aware pause. Cancellation, permanent transport rejection,
changed authority, and invalid acknowledgments stop delivery and preserve
unresolved intent. The adapter starts no delivery goroutines.

The client and journal independently verify the full authority, ordered receipt
count, event keys, requested sequences, canonical body hashes, and positive
increasing signed 32-bit server sequences. Acknowledging logs removes only
matching events. The durable requested-sequence cursor survives restart and
acknowledgment. An exact completion receipt atomically removes the matching
result, intent, and cursor only after that authority has no pending logs.
Generic event or completion methods cannot settle build log evidence.

`BuildComplete` follows engine cleanup, final framing, and acknowledged delivery.
A sink or journal failure cannot become an ordinary `build_failed` completion.
Startup replay validates typed status and the retained immutable snapshot before
it resends pending log batches, then replays a retained result. Replay does not
acquire source, run the engine, allocate sequences, or recreate output.
With lost execution authority, replay may submit exact retained batches solely
to recover historical receipts. The backend rejects every unknown event, and
that denial leaves its evidence unresolved. Historical receipts authorize no
new frames or execution. A retained result still needs its own matching receipt
or current execution authority.

Builds dispatched before migration 0051 have unknown deletion history. Their
typed log ingestion fails explicitly. The executor retains its unresolved
intent and evidence; it never falls back to generic events. A new build is
required for a complete log history. Public reads and retention cursor semantics
are documented in [Durable hosting build logs](hosting-build-logs.md).
