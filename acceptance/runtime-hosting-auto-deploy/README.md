# Trusted automatic-deployment runtime fixture

Run from the repository root with a clean committed checkout:

```sh
bash acceptance/runtime-hosting-auto-deploy/gate.sh
```

An optional `NEROCD_AUTO_DEPLOY_BUILDX_FILE` supplies a previously downloaded
Buildx binary. The gate copies it into owned evidence and still requires the
exact pinned native architecture checksum before execution. It never changes
the source file.

The gate compiles the actual CLI and API integration test for the Docker host's
native Linux architecture. It records the Git commit/tree/source blobs and
binary hashes under the printed `/tmp/nerocd-auto-deploy-runtime.*` directory.
That evidence remains after cleanup.

The unique labelled privileged Docker 29.4 daemon has no host socket, host PID
namespace, or published ports. Its own disposable PostgreSQL 17.6, build toolchain,
workloads, Traefik, Pebble CA, and DNS server live inside that daemon's namespace.
Cleanup checks exact IDs and labels, joins fixture processes, removes only owned
resources, and verifies the source checkout stayed clean. No shared PostgreSQL,
live GitHub endpoint, public DNS, production CA, or production host is used.

The normal authorized API bootstraps manual healthy A and an unrelated HTTPS
application, leaving the push policy unconfigured. The test reads owner review
options, explicitly saves/verifies/enables automatic mode, delivers signed B,
and requires the actual compiled agent to build and deploy B on a fresh port,
then verify and load the HTTPS switch. It independently verifies fixture-CA TLS,
exact artifact/container identity, and the unrelated route. A lost committed
proxy-completion response and agent restart must preserve immutable receipts and
operation IDs. Source C executes `RUN false` and produces no artifact or deploy;
source D builds a 503 workload and must never switch. Both preserve B's HTTPS. Duplicate B and delayed A deliveries do not replace B.
C's durably acknowledged failure must allow D to execute on the same running
agent PID, with no intervening restart. A final fixed first-apply barrier accepts
a fresh fixed E operation, records a newer signed fixed F desired revision before admission, and
requires HTTP 409, or HTTP 404 backed by the exact canceled operation, task,
lease, attempt, and fence. Both require no mutation receipt/hold, no actual Docker candidate, and
preserved B/unrelated HTTPS.

Git retains its exact `https://github.com/acme/push-fixture.git` origin and normal
end-to-end TLS verification against the owned Git CA. A fixed bounded test-only
CONNECT bridge routes only literal `github.com:443` to the owned loopback Git
listener. No caller selects an upstream, credentials, or forwarded headers.
This routing differs from production; it does not establish live-provider
transport qualification. The compiled CLI's test-only `nerocd_proxy_fixture`
build tag selects the owned Pebble CA; production TLS behavior is unchanged.

The Dockerfile strategy uses the pinned Buildx/BuildKit path. The otherwise
required planner identity is pinned Alpine and is never invoked as Railpack.
This fixture does not claim Railpack, browser HTTPS, UI completion, public ACME,
untrusted-source isolation, or complete host provisioning. Real PostgreSQL
regressions separately cover supersession, manual epochs, authority/capability
loss before first mutation, transactional rollback, expiry, and lost-job recovery.
