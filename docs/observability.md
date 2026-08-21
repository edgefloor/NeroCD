# Operational metrics

`GET /metrics` is available only to a `system_admin` bearer session. In production it remains on the server's private proxy network; the production Compose profile does not publish a metrics port.

The endpoint exposes a fixed, low-cardinality vocabulary: HTTP method/route/status classes, PostgreSQL pool state, queue age/depth, leases, runner heartbeat age, terminal run duration/status, deployment/health/rollback aggregates, bounded runner journal/retry telemetry, and the latest backup outcome. It never labels metrics with an ID, name, path, URL, token, project, service, environment, run, lease, runner, request body, or database error.

Runner `retry_count` and `renew_failures` are monotonically increasing process-lifetime counters, saturated at `100000` before each authenticated latest-state report. A retry is counted only when a transient journal/provenance/log/event/terminal/renewal/heartbeat request has passed its bounded backoff and will actually be attempted again; initial successes, ordinary polling, and no-work claims do not change it. A renewal failure is counted for every failed `/renew` attempt, including a transient failure that later recovers and an authority denial that stops the attempt. Counter values contain no failure detail; runner restart deliberately resets them to zero.

Suggested alerts and first actions:

- Queue age above the deployment SLO or queue depth steadily rising: inspect authenticated runs and runner heartbeats; add/repair runners before retrying work.
- Active leases with growing expired leases or renew failures: inspect runner connectivity and clock/network health; let fenced reaper recovery complete before manual intervention.
- Oldest runner heartbeat beyond two lease TTLs: verify runner enrollment/credential and host health; do not rotate a credential until the old runner is stopped.
- Deployment health failures or rollback failures: inspect the deployment/audit history through the authenticated API. A rollback failure is operator-visible and requires target reconciliation, not a blind retry.
- Backup age above policy or last result `failure`: inspect the bounded command outcome in the invoking supervisor logs. Failures before a database URL can be admitted cannot be persisted safely; they must be retained by that supervisor. Verify the private archive root and owner database connectivity before retrying.

Run `make observability-gate` for the renderer/store/API contract plus real Compose runner lifecycle and production-shaped backup metrics scrapes. It verifies anonymous denial, fixed-label redaction, durable lifecycle aggregates, and backup observation in isolated disposable topologies.
