# Independent hosting runtime observation

The runner can collect read-only evidence about its current hosting containers
independently of deployment execution. Enable it with `--hosting-observation
--execute`. Updated runners advertise `hosting_observation_v1` and
`hosting_storage_observation_v1` when observation is enabled. The persisted
capabilities must authorize the configured observer. Observation does not require
a build configuration. It rejects `--once`, `--complete-status`, and an explicit `shell`
capability; the CLI removes its implicit default shell capability when enabling
observation.

The controller URL must use HTTPS. Tests may explicitly enable literal-loopback
HTTP through the programmatic configuration. There is no insecure HTTP CLI flag.
The transport rejects redirects and insecure TLS verification. The collector
checks runner identity before replay and starts only after all replay succeeds.
Shutdown cancels and joins it before closing the runtime engines and journal.

## Upgrade compatibility

`hosting_observation_v1` continues to authorize stateless observations. Mounted
observations also require `hosting_storage_observation_v1` in the runner's current
persisted capabilities. The controller skips mounted targets for v1-only runners
and advances the page cursor over them, so stateless targets later in the sweep
remain available. Stateless targets retain their original closed JSON fields.
The controller never sends a mounted target with its storage field omitted.

The controller checks the current capabilities before reusing a pending challenge,
accepting a report, or acknowledging a repeated report. Removing the storage
observation capability blocks mounted evidence immediately, even for a previously
issued challenge or accepted sample. Saved observations remain historical and
current mounted status becomes blocked. Log collection keeps its own capability
and authorization checks.

Upgrade the controller before granting the new capability to an updated observer.
For an existing enrolled runner, refresh the persisted capabilities through the
authorized runner enrollment and capability configuration workflow. Restarting
with new flags or replacing the binary does not update an enrolled runner's
persisted capabilities. Until refreshed, the updated observer can continue
stateless collection with its v1-only identity. An updated runner connected to an
older controller retains the exact mount checks: a target without storage
accepts no mounts and cannot produce verified mounted evidence.

## Collection bounds and freshness

Each cycle requests one page containing at most four targets. The runner validates
the entire page before issuing any Docker command, then samples at most four
targets concurrently. It retains the cursor between cycles and resets it at the
end of a sweep. The target cadence is ten seconds per cycle, not ten seconds per
resource. With more than four eligible resources, a full sweep takes multiple
cycles and a resource can become stale between samples. The controller's stale
classification remains authoritative; observation does not extend freshness when
the runner is absent or evidence collection fails.

Each challenge lasts ten seconds. Sampling, including Docker commands and the
readiness request, is bounded to five seconds and the original challenge deadline.
The runner converts the received remaining challenge lifetime into a local
monotonic deadline. Reports have short timeouts inside that deadline and at most
one transient retry with the identical body. A conflict discards the report.
There is no queued page, resampling on retry, or cross-cycle evidence retention.

## Evidence and authority

The sampler uses the configured Unix Docker daemon and permits only daemon ID,
an exact container inspection projection, and an exact-ID container listing. The
projection excludes environment values and unselected labels. It requires the
daemon, full container and image IDs, runtime namespace, binding, operation,
release hash, plan hash, and single loopback port mapping to match the target.
Command output and lifetime are bounded, and raw command diagnostics are not
reported or logged.

The controller validates the settled healthy receipt's storage evidence before
issuing a target. A target with storage includes the immutable volume name and
mount path. Both configured and actual mounts must match that exact writable
local volume. A target without storage permits no mounts. Container labels cannot
authorize storage. Host UTS and cgroup namespaces fail closed, and the sampler
checks mounts and host policy both before and after the readiness request.

A successful, empty, exact-ID listing on the verified daemon is required to
report a missing container. An inspection error alone does not establish absence.
An exited container must have verified identity, state, and exit code. Paused,
created, restarting, dead, and other indeterminate states remain unknown.

Readiness uses only the target's literal `127.0.0.1` port and validated path. The
HTTP client disables proxies, redirects, and keepalive, and bounds response bytes.
An explicit 200–399 response is ready; another explicit status is not ready. A
request or body-read failure remains unknown. Before reporting readiness, the
sampler rechecks the daemon and exact running container identity.

Observation cannot start, stop, pull, execute in, or replace a container. It does
not use deployment `Observe`, settle an operation, or clear a mutation hold.
Failure to collect evidence cannot grant mutation authority.

## Resource status in the WebUI

Project hosting shows a read-only runtime status section for each resource.
Current container state and readiness, agent heartbeat, saved placement, settled
deployment target, and historical observation have separate labels. A successful
deployment is an earlier operation outcome. It does not establish that a
container is still running or ready. Unknown agent reachability does not
establish that a container stopped.

The UI accepts current evidence only when its resource and target identities
match, its response fields agree, and its age is below thirty seconds. It adds
request duration and local elapsed time to the server-reported age. Local clock
movement can expire evidence early but cannot extend its apparent freshness.
Freshness expires even while a request is disconnected or the tab is hidden.
Old-target evidence remains explicitly historical after replacement. Unbound
and never-observed resources show unknown status.

The section polls every ten seconds only while visible. A failed request pauses
polling until explicit retry and makes retained evidence historical. An access
failure clears protected evidence. Actor, project, resource, and current
membership changes abort requests and clear the previous scope. Resource
viewers can read status; this section has no mutation controls. Its access does
not depend on legacy task or template requests elsewhere on the page.

## Verification scope

The isolated acceptance gate in `acceptance/runtime-hosting-observation/` runs a
compiled runner against the real controller, PostgreSQL, and a Linux Docker
daemon. It covers healthy evidence, a fixture-authorized stop, exited evidence,
agent shutdown and stale unknown after thirty seconds, resume, replacement, and
rejection of the old target's report. Its command trace records only the allowed
action names. A workload environment canary is absent from controlled collector
logs and command traces.

The gate uses a test CA for the controller transport. It does not qualify
production application ACME, arbitrary application log redaction, untrusted
Docker daemons, or scheduling guarantees at fleet scale.
