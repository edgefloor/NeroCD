# Live hosting runtime logs

Enable the runner's demand-driven Docker log adapter with
`--hosting-runtime-logs`. Execution must be enabled. The flag rejects `--once`,
`--complete-status`, and an explicit `shell` capability. It removes only the
implicit default shell capability and does not require a build configuration.
The local option must exactly match the persisted `hosting_runtime_logs_v1`
capability before the runner opens or replays its journal.

The controller requires HTTPS with verified TLS. Log claim and append methods
reject redirects, custom transports, insecure TLS verification, and ambiguous
base URLs. Explicit programmatic fixtures may permit literal-loopback HTTP;
there is no insecure transport CLI flag. The dedicated strict log JSON decoder
remains separate from the observation protocol.

## Demand and collection authority

An authorized viewer opens a session against an immutable settled container.
Only that viewer can renew its thirty-second demand. The runner claims collection
leases of at most ten seconds and never renews viewer demand. Collection begins
only after startup replay succeeds and continues independently of deployment
ownership. The runner cancels and joins collection before closing its runtime
and journal.

Each process allocates a random instance ID and runs at most four collectors.
It claims or renews every three seconds with a two-second request timeout. A
local monotonic watchdog uses the remaining server lease conservatively from
the claim request's start, with a one-second reserve. Each renewal rechecks the
immutable target, daemon, container, TTY mode, and driver before extending the
local reader deadline. A failed request does not extend local authority.

Each collector owns one reader, one bounded redacted queue, one immutable pending
canonical batch, and at most one append request. A transient append failure may
retry once with the same batch ID and body under the remaining authority. Batch
sequence is independent of event sequence. An empty following batch advances
only batch sequence. Renewal responses never overwrite local publication counters.

A conflict, stream failure, or ambiguous publication cancels the bounded
collection group. The runner joins every old reader and waits out the latest
possible server lease before reacquiring. This conservative group restart can
interrupt other sessions in that group. The wait includes failed claim attempts
because a lost response may hide a committed renewal, and parent cancellation
interrupts it immediately. A new epoch records a source gap. No old ambiguous
stream is silently reopened.

## Docker and output bounds

The adapter uses only the configured local Unix `ImageDockerEndpoint`. It has no
remote endpoint, proxy, redirect, CLI execution, or container mutation path. It
pins Engine API 1.54 and verifies that the daemon advertises a compatible API
range before use. Docker's [API version table](https://docs.docker.com/reference/api/engine/)
lists 1.54 as the maximum API for Docker 29.4. The fixed GET endpoints are
`/version`, `/v1.54/info`, exact-container `/json`, and exact-container `/logs`.

Inspection is bounded to 256 KiB. The decoder retains only the full container ID,
name, image ID, four authority labels, TTY flag, and driver. It skips environment
values and arbitrary configuration, and clears the bounded raw document.
Daemon ID, namespace, binding, operation, release hash, and plan hash must match
the claimed target. The qualified drivers are `json-file` and `local`; another
driver reports `unsupported_driver`.

The reader requests at most 200 tail lines and then follows. Non-TTY output uses
Docker's bounded stdout/stderr multiplex frames. TTY output is labeled combined.
Frames above 64 KiB and malformed headers close the stream. UTF-8 output chunks
are at most 16 KiB, NUL and invalid UTF-8 are replaced, and clipping is marked.
Source timestamps are parsed separately from messages. Unflushed line, UTF-8,
and redactor tails are discarded on disconnect or authority loss.

The existing streaming redactor removes the known runner bearer and configured
encodings before queue retention or transport, including values split between
callbacks. The adapter does not acquire runtime secrets. It does not promise
universal redaction of application secrets it has never received. Runtime log
text is sensitive application content and never belongs in ordinary logs,
audit metadata, output files, or the attempt journal.

Each redacted queue is bounded to 64 KiB and 64 queued events. A full event
channel blocks cancellably within that byte budget, including the pending event.
Byte-budget overflow closes the source. The sink serializes publication. Batches contain at most 64 events
and remain below the encoded 256 KiB API limit. Disconnect and overflow create
source gaps. Reacquisition requests a bounded tail again and may duplicate
messages. Server event sequence establishes accepted publication order, not
universal exactly-once Docker capture. Source timestamps can differ from that
order. Server retention can evict an earlier prefix, which the session API
exposes as a separate retention gap.

## Qualification

Unit and race tests cover parser bounds, split bearer redaction, protocol trust,
identity, counters, renewal, cancellation, replay, and queue limits. The
`acceptance/runtime-hosting-logs/` fixture qualifies the compiled Linux agent,
registered controller API, PostgreSQL, and pinned Docker 29.4. Fixture setup and
its initial workload provenance are explicitly identified in its README.
Production application ACME, remote or untrusted daemons, arbitrary logging
drivers, universal secret redaction, and browser UI behavior are outside this
adapter's qualification.
