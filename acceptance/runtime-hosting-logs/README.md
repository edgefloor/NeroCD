# Compiled hosting runtime log gate

From the repository root, run:

```sh
bash acceptance/runtime-hosting-logs/gate.sh
```

The gate uses an unused `127.0.0.1:32961`, a newly owned PostgreSQL container,
and a newly owned privileged Docker-in-Docker container. Docker 29.4,
PostgreSQL 17.6, and the inner registry are pinned by digest in the script. It
prints compiled Linux CLI and API-test hashes and records container, image,
volume, and database schema inventories. It preserves shared 32918, browser
fixture 32963, and all unrelated resources. Cleanup verifies full IDs and owner
labels and removes only this invocation's containers, anonymous volumes, image,
and temporary build directory. There is no global prune.

The primary json-file/non-TTY workload deploys through the registered HTTP API
and compiled CLI. Only after deployment succeeds does the fixture enable log
collection, open a viewer session, and trigger fresh stdout/stderr output. A
quiet workload first produces an empty following batch. The test commits that
append and drops its HTTP response, then verifies one retry has the identical
batch ID and body hash. It checks post-settlement source timestamps, accepted
server sequence order, known bearer redaction, renewal without duplicate
readers, actual burst-driven prefix eviction, and real thirty-second viewer
demand expiry.

Additional json-file/TTY, local/non-TTY, local/TTY, and unsupported-driver
workloads use fixture-created containers with actual daemon, image and full
container IDs. The fixture supplies their prepared/apply/complete provenance.
These additional cases qualify the log reader and API, not production deployment
support for selecting TTY or logging drivers. They cover combined versus separate
streams, daemon mismatch, a new epoch's visible gap, revocation, replacement,
explicit viewer stop, and the unsupported driver state. A started-process
handshake then sends SIGTERM while an append is blocked and its fixture-owned
source is paused, requiring both the append and reader to join. The production deployment
options are unchanged.

A fixture-only Unix reverse proxy rejects methods and paths outside the adapter's
fixed GET allowlist. Its trace retains only paths and counters, never inspect
response bodies, headers, or application output. The observer's configured socket
is that proxy; the actual daemon is isolated inside this invocation's DinD.

Controller transport uses a test CA and verified HTTPS. The fixture's fixed
runner credential is intentionally emitted across workload writes to exercise
redaction. It is absent from controlled collector logs and API cached messages.
This does not establish universal application secret redaction or absence from
the deliberately configured fixture credential file and source probe binary.

The standalone adapter fixture reads API pages with `limit=100`, which is valid
on its frozen base. A separately owned middleware fix qualifies the accepted
200-item runtime log page in the combined browser gate. This fixture does not
change API routes, generated contracts, schema, or browser behavior. It does not
qualify production ACME, remote/untrusted daemons, other logging drivers, or
exactly-once source capture. A restart can duplicate a bounded tail and has a
visible source gap. Preserve full failed and passing logs with the source SHA,
binary identities, owned resource IDs, and cleanup evidence.
