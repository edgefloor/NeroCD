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
