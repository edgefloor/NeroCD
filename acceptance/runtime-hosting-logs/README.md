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
on its frozen base. The combined browser gate qualifies the accepted 200-item runtime log page. This fixture does not
change API routes, generated contracts, schema, or browser behavior. It does not
qualify production ACME, remote/untrusted daemons, other logging drivers, or
exactly-once source capture. A restart can duplicate a bounded tail and has a
visible source gap. Preserve full failed and passing logs with the source SHA,
binary identities, owned resource IDs, and cleanup evidence.

## Actual collector and browser

Run the optional combined path from the repository root:

```sh
NEROCD_RUNTIME_LOGS_ACTUAL_BROWSER=1 bash acceptance/runtime-hosting-logs/gate.sh
```

This path builds the unchanged UI before compiling Go, writes the canonical asset
manifest to `/tmp/nerocd-runtime-logs-actual-manifest.json`, prints binary hashes,
and runs the compiled Linux CLI's 173-route OpenAPI contract. The browser checks
all 73 served assets against the 74-entry manifest. The unserved `.gitkeep` is
an embedding placeholder. Set `NEROCD_RUNTIME_LOGS_ACTUAL_MANIFEST` to retain a
manifest at another explicit location.

The existing primary image deployment settles through the registered API and
compiled CLI before the browser fixture becomes available. A seeded project
viewer then explicitly follows the resource. Fixed fixture stages trigger fresh
stdout/stderr from that actual Docker workload and stop/restart the compiled
collector. The browser checks immutable deployment/container identity, increasing
server sequences, source and receipt timestamps, known bearer redaction, Stop
and new Follow, burst-driven retention, reconnect source gap, real elapsed demand
expiry while outside the viewport, and output clearing on membership revocation.
It also checks keyboard interaction and overflow at 320, 390, 430 and 1440 pixels.
No log response or runner event is fabricated in this path.

The browser port is selected by Docker and published only on host loopback.
The test binary listens inside its disposable DinD network namespace. Test-only
stage and completion endpoints require the exact seeded owner session and Origin
and allow only ordered actions for this fixture. They return reference metadata,
not credentials or log output. The runner continues to use verified test-CA TLS.
Browser traces and automatic screenshots are disabled; explicit screenshots show
only the authenticated log view with controlled, redacted probe output.

The browser completion hook releases the bridge even on assertion failure. A
bounded context also cancels waiting handlers before server shutdown. Gate cleanup
checks full IDs and owner labels, asserts that the PostgreSQL schema inventory
returns to `public`, and removes only invocation-owned resources. Retain failing
and passing logs separately. The default command still runs the original full
standalone collector scenarios; optional browser mode keeps its narrower combined
journey separate instead of repeating all driver and signal tests.

Accounts, project membership, placement, enrollment, and the probe image are
fixture setup. This proves the bounded deployed-workload log path, not complete
onboarding, public TLS, arbitrary Docker drivers, universal secret redaction, or
exactly-once source capture. The project viewer role permits legacy Task reads;
this journey needs no Task setup or mutation/operation permission.
