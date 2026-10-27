# NeroCD documentation

Start with [Getting started](getting-started.md) for a local server. Before
choosing a deployment profile, read [current support and limits](compatibility.md).
The guides below describe hosting workflows; shared workspaces and coding agents
remain future scope.

## Set up the platform

- [Production deployment](production.md): image, database roles, private secrets and TLS proxy.
- [PostgreSQL operations](postgres-operations.md): control-plane migrations, backups, off-host export and disposable restore.
- [OIDC sign-in](oidc.md): configure a provider and provision identities.
- [Service accounts](service-accounts.md): project automation credentials, rotation and offboarding.
- [Secrets](secrets.md): managed versions, encryption keys and runner-file bindings.
- [Operational metrics](observability.md): protected metrics, alerts and first actions.

## Connect an application host

- [Servers and destinations](hosting-destinations.md): manual enrollment, project grants and placement.
- [SSH onboarding](hosting-ssh-onboarding.md): the API and UI workflow for a prepared host.
- [SSH installer](hosting-ssh-bootstrap.md): installer identity, host requirements and recovery.
- [Server readiness](server-readiness.md): diagnostics, stale observations and the tested profile.

## Build and deploy an application

- [Deploy an image application](hosting-image-deployments.md): review, deployment history and retries.
- [Configure Git and request a build](hosting-git-resources.md): source settings and private grants.
- [Configure a build runner](hosting-build-runtime.md): policy, toolchain and private HTTPS opt-in.
- [Deploy a built artifact](hosting-build-deployments.md): select the exact local build output.
- [Same-host artifact runtime](hosting-local-build-runtime.md): daemon/platform identity and execution limits.
- [Build output](hosting-build-logs.md): retained output and the resource viewer.
- [GitHub build-on-push](hosting-github-push.md): signed events and saved-branch policy.
- [Automatic deployment](hosting-auto-deploy.md): opt-in unprotected deployment after a manual release.
- [Catalog installation](hosting-catalog.md) and [catalog recovery](hosting-catalog-recovery.md): the pinned Linkding workflow.

## Route traffic and observe applications

- [Managed routing](hosting-proxy-ui.md): proxies, hostname grants and routes.
- [Verified route switching](hosting-route-switch.md): move traffic between retained releases.
- [Proxy recovery](hosting-proxy-recovery.md): review eligible metadata conflicts.
- [Runtime status](hosting-runtime-observation.md): collector configuration and current/stale evidence.
- [Runtime log collection](hosting-runtime-logs.md) and [log viewer](hosting-runtime-log-viewer.md): Follow sessions, gaps and access controls.

## Connect and recover data

- [Database connections](hosting-database-connections.md): PostgreSQL or Redis, application bindings and upload volumes.
- [PostgreSQL backup and restore](hosting-database-backups.md), [schedules](hosting-database-backup-schedules.md), [remote copies](hosting-database-remote-backups.md), and [retention](hosting-database-backup-retention.md).
- [Backup failure notifications](hosting-backup-notifications.md): SMTP and HTTPS webhook alerts.
- [Owned-upload archives](hosting-storage-archive.md), [remote copies](hosting-storage-remote-backups.md), [schedules and alerts](hosting-storage-backup-schedules.md), [retention](hosting-storage-backup-retention.md), and [selected-copy deletion](hosting-storage-backup-deletion.md).

## Understand or extend NeroCD

- [Architecture](architecture.md), [domain glossary](../CONTEXT.md), and [HTTP API](../openapi.yaml).
- [Product specification](product-spec.md): full hosting and workspace requirements, R01–R102 and A01–A66.
- [Implementation plan](plan.md) and [workspace capabilities](workspace-capabilities.md): delivery order and deferred scope.
- [Dependency policy](dependency-exceptions.md): reviewed dependencies and license rules.
- [Release trust](release-trust.md) and [local release evidence](release-evidence.md): candidate checks and publication policy.

Hosting implementation references:

| Area | References |
| --- | --- |
| Deployment | [Public image execution](hosting-execution.md), [job infrastructure](hosting-jobs.md) |
| Source and credentials | [Controlled checkout](hosting-source-checkout.md), [private credential delivery](hosting-build-credentials.md), [trigger revision](hosting-build-trigger-revision.md) |
| Build execution | [Engine](hosting-build-engine.md), [controller](hosting-build-control.md), [executor](hosting-build-executor.md), [River adapter](hosting-build-jobs.md) |
| Build transport | [Phase protocol](hosting-build-protocol.md), [journal](hosting-build-journal.md), [client](hosting-build-client.md), [output delivery](hosting-build-log-delivery.md) |
| Proxy execution | [Traefik engine](hosting-proxy-engine.md), [controller](hosting-proxy-control.md), [mutation authority](hosting-proxy-auth.md) |
