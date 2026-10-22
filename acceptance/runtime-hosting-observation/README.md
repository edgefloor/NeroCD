# Compiled hosting observation gate

Run from the repository root:

```sh
bash acceptance/runtime-hosting-observation/gate.sh
```

The gate requires Go, Python 3, Docker, and an unused loopback port 32961. It
creates its own PostgreSQL container on `127.0.0.1:32961` and an isolated privileged
Docker-in-Docker container. It does not use the shared PostgreSQL fixture on
32918. The Docker and PostgreSQL base images and inner registry image are pinned
by digest in `gate.sh`.

The gate compiles the CLI and controller integration test for the Docker host's
Linux architecture and prints their SHA-256 hashes. The test uses a test-CA TLS
controller and the existing controller/store implementation, creates an isolated
schema, deploys a small probe, and starts the compiled CLI with observation
enabled. The fixture alone stops, starts, and replaces containers. The collector
wrapper accepts only `info`, projected `container inspect`, and exact-ID
`container ls`, recording action names without arguments or environment values.

Assertions cover fresh healthy evidence, verified exit after a stop, cancellation
and process join, stale unknown after both evidence and heartbeat age past thirty
seconds, resume, replacement, and rejection of a still-live challenge for the old
container. The test also checks that an environment canary does not occur in the
controlled observer logs or action trace. The gate does not inspect arbitrary
application logs and does not qualify production ACME or fleet-scale cadence.

The script records container, image, volume, and PostgreSQL schema inventories.
Cleanup removes only full container IDs and the image whose owner label matches
this invocation. It removes owned anonymous volumes with their containers and
checks removal. The test drops its own schema. Before/after inventories permit
concurrent unrelated fixture changes to be attributed separately. No global
prune or shared-fixture lifecycle action is used.

Preserve the complete command output as evidence, including failures, binary
hashes, fixture identities, schema inventories, and cleanup results. A passing
gate is evidence for its exact source and binaries; it is not production
acceptance.

## Real browser qualification

Build the UI before compiling the gate, then enable its browser mode:

```sh
bun run --cwd web/app build
NEROCD_OBSERVATION_BROWSER=1 bash acceptance/runtime-hosting-observation/gate.sh
```

This mode also requires Bun, Node.js, and the existing Playwright Chromium
installation. It serves the freshly embedded `web.Static` and real API through
an ephemeral host-loopback port. The collector keeps its test-CA TLS transport.
The browser uses seeded owner and viewer accounts and an existing fixture
deployment; it does not qualify account onboarding or fresh source builds.
Only reference identifiers pass through the fixture pipe. Traces and automatic
screenshots are disabled so sign-in credentials do not enter browser artifacts.
The four explicit screenshots contain only the runtime status section.

The browser uses keyboard refresh at widths 320, 390, 430, and 1440 and checks
overflow. It reads actual status responses through healthy, stopped and exited,
disconnected and stale, resumed, and replaced container states. Temporary browser
network disconnection verifies that local freshness expires without a successful
response. No runtime-status response is mocked. It checks viewer reads,
unauthenticated and cross-project denial, and the absence of status writes.

Fixture-only POST endpoints sequence the lifecycle actions and completion. They
require the seeded owner session and exact loopback Origin. Viewer sessions and
another Origin cannot invoke them. They are compiled only in the integration
test. Completion and bounded cancellation release the fixture after browser
failure. To exercise that cleanup path deliberately, add
`NEROCD_OBSERVATION_BROWSER_FAIL_AFTER_LOGIN=1`; the command must fail and still
report removal of its owned containers, image, and schema. The original command
without browser mode remains the collector-only gate.
