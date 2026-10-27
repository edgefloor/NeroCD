# Qualify protected hosting environment execution

Run `bash acceptance/runtime-hosting-environment/gate.sh` from the repository root
only while the named PostgreSQL fixture and port 32918 are allocated to this gate.
The script verifies that fixture's container ID and task label. The API test
creates and drops a separate schema. It does not stop PostgreSQL or remove
existing schemas.

The gate compiles the Linux runner and API test binary, prints their SHA-256
identities, and starts an owned Docker 29.4 DinD container. A local registry
serves a scratch image containing the digest probe. Cleanup verifies ownership
before removing the DinD container, its delegated cgroup, and its image.

The compiled runner uses a test-CA TLS controller endpoint. Initial image
execution has no build configuration. Local artifact execution retains the
existing build capability and validates an explicit build configuration with
pinned Buildx. A trusted fixture producer records provenance for the already
built probe image. This qualifies consumption of a persisted Git artifact and
preservation of its image identity; it does not qualify a new Git checkout or
build. No build is dispatched by runtime environment deployment.

The probe returns environment entry names and SHA-256 digests. Tests compare
empty, Unicode, hash, equals, dollar, whitespace, and managed-secret values.
They also exercise response loss with the same delivery ID, readiness failure,
configuration edits, cancellation, lease expiry, revocation, receipt replay, and
startup reconciliation. Rotation alone preserves the selected immutable active
version. Configuration changes and revocation deny fresh delivery before
mutation. Previously running containers retain their original environment.

Controlled runner output, workload logs, journal files, audit metadata, and the
verified workload process command line are scanned for the fixture secret.
Docker daemon administrators can inspect container environment values. The gate
does not claim memory erasure, application log redaction, instant revocation of
values already delivered, production application TLS, or ACME qualification.

## Real browser configuration and deployment

Set `NEROCD_HOSTING_ENVIRONMENT_BROWSER=1` to extend this same gate with the
real embedded WebUI, browser sessions, API, compiled runner and owned Docker
endpoint. Build `web/app` before running it so the Linux binaries embed the UI
under review. The browser port is published on host loopback only. The fixture
validates an exact loopback origin and uses non-Secure cookies only in this
explicit test mode. Its test-only proof and completion endpoints require the
fixture owner's authenticated browser session and exact Origin.

The browser selects an explicitly nonsecret literal and an existing managed
secret's exact version, then saves the real configuration API. It deploys the
registry image and the trusted persisted Git artifact without a new build.
The actual container endpoint returns entry names and digests. The fixture
compares both selected values; the browser compares the literal digest and
image identity. A response-loss test first forwards the request to the actual
server, then drops its response and checks the identical retry recovers the
same receipt. A concurrent API edit advances the runtime generation while an
actual metadata response is held, proving stale admission returns 409.
Readiness failure must preserve the prior container and its environment.

Keyboard interactions and screenshots cover widths 320, 390, 430 and 1440.
The fixture seeds a normal project owner, a viewer, resources, placement, and
trusted artifact provenance. It does not claim full onboarding or a fresh Git
build. Viewer writes and test-only completion are denied, and no reveal is
requested. Traces and automatic screenshots are disabled; explicit screenshots
capture only nonsecret runtime configuration and secret reference metadata.

The gate compares allocated PostgreSQL schema, public-table and existing-session
inventories before and after execution. It preserves pre-existing schemas, creates and drops only its test schema, and removes only its
identity-verified disposable daemon, cgroup and image.

For the failure cleanup check, also set
`NEROCD_HOSTING_ENVIRONMENT_BROWSER_FAIL_AFTER_LOGIN=1`. The browser deliberately
fails after authentication and releases the fixture through its authenticated
completion endpoint. This run must exit nonzero while still reporting that the
allocated PostgreSQL schema, public-table and session inventories were preserved.
The bridge waits for bounded Go cleanup instead of terminating the API test as
soon as a browser assertion fails.
