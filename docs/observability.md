# Operational metrics

`GET /metrics` requires a `system_admin` bearer session. Keep it on the private
proxy network in production. The production Compose profile publishes no metrics
host port.

## Metric contents

Metrics use a fixed set of labels for HTTP method, route and status classes.
They include PostgreSQL pool state, queue age/depth, leases, runner heartbeat
age, terminal run duration/status, deployment/health/rollback aggregates,
bounded runner journal/retry telemetry and the latest backup outcome.

Labels never contain IDs, names, paths, URLs, tokens, projects, services,
environments, runs, leases, runners, request bodies or database errors.

Runner `retry_count` and `renew_failures` increase over the process lifetime and
saturate at `100000` before each authenticated latest-state report. Restarting
the runner resets them to zero. Neither counter contains failure details.

A retry counts only after a transient journal, provenance, log, event, terminal,
renewal or heartbeat request has passed bounded backoff and is about to be tried
again. Initial successes, ordinary polling and no-work claims do not count.
Every failed `/renew` attempt increments `renew_failures`, including a transient
failure that recovers and an authority denial that stops the attempt.

## Alerts and first actions

| Observation | First action |
| --- | --- |
| Queue age exceeds the deployment SLO or depth keeps rising | Inspect authenticated runs and runner heartbeats. Repair or add runners before retrying work. |
| Active leases accompany rising expired leases or renewal failures | Check connectivity, clocks and network health. Allow fenced reaper recovery before manual intervention. |
| Oldest runner heartbeat exceeds two lease TTLs | Check enrollment, credentials and host health. Stop the old runner before rotating its credential. |
| Deployment health or rollback fails | Read deployment and audit history through the authenticated API. A rollback failure requires target reconciliation before retry. |
| Backup age exceeds policy or last result is failure | Read the invoking supervisor's bounded command outcome. Verify the private archive root and owner database connectivity before retry. |

Failures before database URL admission cannot be persisted safely. The invoking
supervisor must retain them in its own logs.

## Verify metrics

Run `make observability-gate` for renderer/store/API checks and real Compose
runner-lifecycle and production-shaped backup scrapes. The gate checks anonymous
denial, fixed-label redaction, durable lifecycle aggregates and backup observation
in isolated disposable topologies.
