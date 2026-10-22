# Independent hosting runtime observation

The runner can collect read-only evidence about its current hosting containers
independently of deployment execution. Enable it with `--hosting-observation
--execute` on a runner enrolled with `hosting_observation_v1`. The configured and
persisted capability sets must agree. Observation does not require a build
configuration. It rejects `--once`, `--complete-status`, and an explicit `shell`
capability; the CLI removes its implicit default shell capability when enabling
observation.

The controller URL must use HTTPS. Tests may explicitly enable literal-loopback
HTTP through the programmatic configuration. There is no insecure HTTP CLI flag.
The transport rejects redirects and insecure TLS verification. The collector
checks runner identity before replay and starts only after all replay succeeds.
Shutdown cancels and joins it before closing the runtime engines and journal.

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
