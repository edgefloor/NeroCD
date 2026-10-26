# NeroCD documentation

- [Product specification](product-spec.md): full hosting target, R01–R68 and A01–A37.
- [Implementation plan](plan.md): accepted P00–P18 packages and M0–M4 gates.
- [Compatibility ledger](compatibility.md): pinned reference, current limits and planned fixtures.
- [Hosting parity audit](parity-audit.md): one effective atomic checklist, cumulative accepted bounded-profile coverage of 78/933 = 8.36% through `f96d8c6267fcab2273b6f748d09801931660ceaf`, retained historical audit, and reproducible calculator.
- [Accepted hosting foundations](compatibility.md#accepted-foundations-and-evidence):
  dated progress, source tests and the remaining runtime boundaries.
- [Hosting destinations](hosting-destinations.md): manual enrollment, server
  admission, project grants and immutable desired placement through API and WebUI.
- [Hosting job infrastructure](hosting-jobs.md): River transactional enqueue,
  live image consumer, migration checksums and backup identity.
- [Public image execution](hosting-execution.md): API, River, agent and Docker
  path, generic HTTP readiness, cancellation, replay and restore holds.
- [Deploy an image application](hosting-image-deployments.md): WebUI requests,
  operation history, retained-request recovery and role boundaries.
- [Local build engine](hosting-build-engine.md): standalone Dockerfile, Railpack
  and prebuilt-static adapters, fixed-host limits and runtime fixture.
- [Linux proxy engine](hosting-proxy-engine.md): standalone Traefik routing,
  HTTP-01 certificates and state recovery, before control-plane/UI integration.
- [Getting started](getting-started.md): local PostgreSQL, Compose, and memory.
- [Production](production.md): Compose, secrets, proxy, bootstrap, and OIDC.
- [Architecture](architecture.md): current components and the target hosting model.
- [OIDC](oidc.md): provider setup, provisioning, and offboarding.
- [PostgreSQL operations](postgres-operations.md): migration, backup, restore.
- [Secrets](secrets.md): encrypted managed versions, protected UI, key files and
  the separate runner-file execution path.
- [Observability](observability.md).
- [Release trust](release-trust.md) and [release evidence](release-evidence.md).

The public HTTP contract is [openapi.yaml](../openapi.yaml).
