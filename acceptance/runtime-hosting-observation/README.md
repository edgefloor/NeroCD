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
