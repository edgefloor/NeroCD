# Build execution in the Go agent

The normal `nerocd runner` can execute public HTTPS Git builds through the production build controller. A successful build persists a local image artifact and its source, OCI and daemon identities. Deployment, domain routing, HTTPS delivery and private Git credential delivery are separate integrations.

Build execution is disabled by default. Enable it with `--build-config-file /absolute/operator/build.json`. The file must be an owned, nonsymlink, regular mode0600 file of at most 64 KiB. Version 1 rejects unknown fields, duplicate keys (including differently cased duplicates), null values and trailing JSON. Validation happens before registration, enrollment, heartbeat or claim effects. This nonsecret file must never contain Git or runner credentials.

The shape below illustrates the fields; replace identifiers, digests, paths and policy with the actual trusted operator configuration. The entire policy must equal the server's frozen policy, including architecture, limits and toolchain identity. Computing a local toolchain identity does not configure the server policy.

```json
{
  "version": 1,
  "source_root": "/var/lib/nerocd/build-sources",
  "engine": {
    "docker_binary": "/usr/bin/docker",
    "socket": "/var/run/docker.sock",
    "buildx_binary": "/opt/nerocd/buildx",
    "buildx_sha256": "sha256:<verified-binary-digest>",
    "planner_image": "sha256:<installed-planner-image-id>",
    "state_dir": "/var/lib/nerocd/build-engine",
    "cgroup_root": "/sys/fs/cgroup/nerocd-builds"
  },
  "target": {
    "runner_id": "runner_build",
    "server_id": "server_build",
    "destination_id": "destination_build"
  },
  "policy": {
    "identity": "operator-policy-v1",
    "toolchain_identity": "sha256:<computed-toolchain-identity>",
    "protocol": "hosting_build_v1",
    "platform": "linux/arm64",
    "limits": {
      "cpu_quota": 100000,
      "memory_bytes": 1073741824,
      "swap_bytes": 0,
      "deadline": 600000000000,
      "context_bytes": 16777216,
      "context_files": 10000,
      "plan_bytes": 1048576,
      "log_bytes": 1048576,
      "export_bytes": 268435456,
      "unpacked_bytes": 536870912,
      "usage_bytes": 1073741824,
      "reserve_bytes": 1073741824,
      "poll_interval": 100000000,
      "disk_mode": "monitored"
    },
    "source_limits": {
      "max_tree_bytes": 16777216,
      "max_workspace_bytes": 67108864,
      "min_free_bytes": 1073741824,
      "max_entries": 10000,
      "max_depth": 32,
      "poll_interval": 100000000
    }
  },
  "status_interval_ns": 100000000,
  "request_timeout_ns": 5000000000
}
```

All duration integers are nanoseconds: `100000000` is 100 ms, `5000000000` is 5 seconds and `600000000000` is 10 minutes. `ComputeBuildToolchainIdentity` computes the canonical inventory identity from fixed pins and the bounded actual Docker binary hash. Local paths are not published as identity data. See [the engine prerequisites](hosting-build-engine.md) for delegated cgroup ownership, trusted privileged BuildKit, fixed toolchain pins and monitored disk overshoot. These settings provide no hard disk quota or hostile-fork isolation.

The runner advertises `hosting_build_v1` only with validated execution configuration. Shell and build execution cannot be combined. Explicit capability flags and persisted enrolled capabilities must match local configuration before heartbeat or claim. Image and configured proxy modes retain their existing behavior. The authenticated runner ID must equal `target.runner_id`; `--id` cannot override it.

An omitted or empty `target.binding_id` permits multiple resource bindings only on the exact configured runner, server and destination. Every authenticated build snapshot must still have a valid nonempty binding, and the complete initial snapshot is pinned for execution and journal replay. A nonempty configured binding restricts execution to that exact binding. Job inputs never override operator placement or policy.

Canonical build tasks branch before generic process, event and completion handling. `--execute=false` and `--complete-status` cannot settle a build as a generic task. The controller atomically settles build task and lease state when it accepts completion. A same-tuple supervisor renews the lease and cancels execution on authority loss. Renewals may extend beyond the initial lease expiry; they never extend the bounded operation deadline beyond the frozen build deadline.

Build journal reconciliation precedes heartbeat and claim. Missing configuration with retained build records fails closed. Configured build/proxy startup checks authenticated capabilities before replay. Historical proof can acknowledge an exact retained request but cannot grant new execution authority. Lost completion responses replay the exact committed body and revision; unresolved intent, cleanup uncertainty and unsupported recovery remain durable and block normal startup rather than silently rebuilding or adopting an image.

Build-enabled CLI execution handles SIGINT and SIGTERM through `RunContext`, cancels owned requests, watchers, renewers and build work, and waits for cleanup before returning. `Run` remains the background-context compatibility wrapper. Parent cancellation does not discard uncertain journal records. A failed or canceled build cannot publish an engine result as success; checkout cleanup must also complete successfully.

`acceptance/runtime-hosting-build-controller/gate.sh` exercises genuine HTTPS Git, the production HTTP API, PostgreSQL, River and an enrolled CLI inside an owned disposable Linux Docker fixture. It checks Dockerfile, Railpack and prepared static HTTP output, default-lease renewal, committed-response loss/replay, failed and canceled builds, SIGTERM cleanup and an independent HTTP workload. It publishes no outer host ports and performs only identity-checked owned cleanup. This gate establishes build-to-artifact behavior, not deployment handoff.
