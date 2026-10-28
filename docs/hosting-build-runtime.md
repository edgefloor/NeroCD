# Build execution in the Go agent

The normal `nerocd runner` can execute stored Dockerfile, public HTTPS Git, and explicitly enabled private HTTPS Git builds through the production build controller. A successful build persists a local image artifact and its source, OCI and daemon identities. Same-host artifact deployment and managed routing are separately enabled integrations. Private Basic/PAT delivery is connected to the checkout path under the configuration below.

Build execution is disabled by default. Enable it with `--build-config-file /absolute/operator/build.json`. The file must be an owned, nonsymlink, regular mode-0600 file of at most 64 KiB. Version 1 rejects unknown fields, duplicate keys (including differently cased duplicates), null values and trailing JSON. Validation happens before registration, enrollment, heartbeat or claim effects. This nonsecret file must never contain Git or runner credentials.

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
    "identity": "sha256:<operator-policy-digest>",
    "toolchain_identity": "sha256:<computed-toolchain-identity>",
    "protocol": "local-build.v1",
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

The runner advertises `hosting_build_v1` only with validated execution configuration. It also advertises `hosting_dockerfile_args_v1`, required for nonempty Dockerfile build variables at admission, claim and current execution. Legacy builds without variables and Railpack retain their existing capability requirements. Shell and build execution cannot be combined. Explicit capability flags and persisted enrolled capabilities must match local configuration before heartbeat or claim. Image and configured proxy modes retain their existing behavior. The authenticated runner ID must equal `target.runner_id`; `--id` cannot override it.

## Enable stored Dockerfile builds

Validated build configuration advertises `hosting_stored_dockerfile_v1` alongside
`hosting_build_v1`. Admission, claim, and live execution require both for a
stored source. No private Git configuration or source grant is needed. Review
persisted enrollment capabilities before restarting an upgraded runner, as with
other capability changes.

The runner writes the exact saved content into an owned Dockerfile-only context,
records the acquired content hash and tree digest, then uses the existing build
engine and local artifact lifecycle. It preserves uncertain cleanup and retained
journal evidence. Follow the [stored Dockerfile workflow](hosting-build-deployments.md#build-a-stored-dockerfile)
to save, build, deploy, and route an application.

## Enable managed build-secret delivery

Validated build configuration advertises `hosting_build_secrets_v1`. Dockerfile
and Compose builds selecting `settings.build_secrets` require that capability at
admission, claim and execution. Delivery uses protected controller HTTPS and the
full live build authority. A fresh attempt stages owner-only secret files outside
the checkout, passes their paths through BuildKit `--secret`, and removes them
after execution. Values are excluded from saved configuration, request identities
and durable journals. Authorized metadata replay does not reacquire secret values.

The existing indexed output redactor covers configured raw values, supported
encodings and multiline fragments before durable output. This is not a sandbox
against malicious build instructions. See [selection and qualification](hosting-git-resources.md#select-managed-build-secrets).

## Trust a private build-registry CA

Validated build configuration advertises `hosting_build_registry_v1`. Selected
Dockerfile and Compose build credentials require that capability at admission,
claim and current execution. Existing authorized metadata replay retains its
original identity. Credentials are delivered only for a fresh build into an
isolated exact-host Docker config, then removed; they are not stored in the
engine request or retained journal.

For a registry outside normal public trust, add an operator-controlled map under
`engine` in the runner build configuration:

```json
"registry_ca_certificates": {
  "registry.example.test:5443": "/etc/nerocd/registry-ca.pem"
}
```

Keys are canonical exact registry hosts with optional ports. Up to eight absolute
regular PEM files are accepted, each at most 256 KiB and containing only valid CA
certificates. No path component may be a symlink. BuildKit receives scoped CA
configuration and read-only copies. CA content digests, rather than local paths,
bind the toolchain and build identities. Configure trust before computing the
operator toolchain identity and matching policy. The application API and UI cannot
change this trust configuration; TLS verification remains enabled.

## Enable private HTTPS Git

Start with the validated version 1 build configuration above. Add this field at
its top level to opt into private Basic/PAT checkout. Merge this fragment with
the existing fields; it is not a replacement for the complete file:

```json
{
  "private_git": { "trusted_loopback_http": false }
}
```

Omitting `private_git` keeps the runner public-only. This nonsecret configuration
contains no PAT, password, managed-secret ID or source-grant ID. It does not
replace the server's build policy, repository allowlist or destination grants.

Use a verified HTTPS controller URL, for example `--server https://control.example.com`.
The runner rejects unsafe credential-server configuration before enrollment,
journal access, heartbeat or claim. A trusted TLS-terminating proxy must be
explicitly trusted by the server and strip/replace forwarding headers. Client
forwarding headers alone do not authorize delivery. Keep `trusted_loopback_http`
false in production; its true setting exists only for isolated literal-loopback
fixtures and never permits insecure remote transport or disabled TLS checks.

Upgrade the agent, stop it, and review its persisted capability/enrollment and
placement before restart. Private execution requires exact persisted
`hosting_git_https_basic_v1` together with `hosting_build_v1`; shell cannot share
this runner. Fresh registration selects the configured private profile. Adding
configuration to an enrolled runner does not mutate its persisted identity.
Use the supported enrollment/re-enrollment review when a capability change is
needed, then restart with matching configuration. A mismatch fails before replay,
heartbeat or claim. Add `--local-build-deploy` and its matching capability for
same-host deployment; managed proxy execution needs its own explicit settings.

A project owner creates the exact source grant from an existing managed-secret
version. A maintainer selects that grant for a build. Follow the
[private source workflow](hosting-git-resources.md#request-a-private-https-git-build).
The credential endpoint requires live full attempt authority and rechecks current
source, grant, secret version, runner and expiry. It returns a bounded owned
credential with `Cache-Control: no-store` and no redirect fallback.

The runtime derives one exact 32-hex delivery ID from its already durable build
intent. At most three live attempts reuse that ID and its original server window;
startup reconciliation never refetches credentials, source or engine output.
The attempt redactor includes the password, Basic payload and supported encodings
before private output reaches the journal. The checkout adapter removes askpass
files and Git metadata and clears credential bytes before engine use. Build use
can outlast the cleared transport window, while cancellation and the original
operation deadline still apply. Failed authority, transport or cleanup retains
unresolved evidence and cannot become ordinary successful completion.

The real private CLI fixture qualifies Dockerfile and Railpack on a trusted Linux
build host. The optional private browser gate in
`acceptance/runtime-hosting-local-build/` also exercises UI creation/editing of a
private HTTPS resource, exact-version source-grant replay, a genuine compiled
Railpack static-provider build, typed Railpack output, exact local-artifact
deployment, and managed HTTPS routing. A malformed Railpack configuration creates
a real failed build without an artifact; the old application must keep serving
its original HTTPS bytes. Known credentials are scanned in logs, audits, journal,
typed output and CLI output, and checkout cleanup is checked.

This bounded qualification uses seeded accounts, existing secret metadata,
trusted Linux/Docker placement and a local test CA. It does not establish arbitrary
Railpack language providers, Nixpacks, hostile-input isolation, SSH/provider
adapters, complete onboarding, universal redaction, or production ACME. See the
[fixture instructions](../acceptance/runtime-hosting-local-build/README.md) and
[support limits](compatibility.md).

## Placement, lifecycle and replay

An omitted or empty `target.binding_id` permits multiple resource bindings only on the exact configured runner, server and destination. Every authenticated build snapshot must still have a valid nonempty binding, and the complete initial snapshot is pinned for execution and journal replay. A nonempty configured binding restricts execution to that exact binding. Job inputs never override operator placement or policy.

Canonical build tasks branch before generic process, event and completion handling. `--execute=false` and `--complete-status` cannot settle a build as a generic task. The controller atomically settles build task and lease state when it accepts completion. A same-tuple supervisor renews the lease and cancels execution on authority loss. Renewals may extend beyond the initial lease expiry; they never extend the bounded operation deadline beyond the frozen build deadline.

Build journal reconciliation precedes heartbeat and claim. Missing configuration with retained build records fails closed. Configured build/proxy startup checks authenticated capabilities before replay. Historical proof can acknowledge an exact retained request but cannot grant new execution authority. Lost completion responses replay the exact committed body and revision; unresolved intent, cleanup uncertainty and unsupported recovery remain durable and block normal startup rather than silently rebuilding or adopting an image.

Build-enabled CLI execution handles SIGINT and SIGTERM through `RunContext`, cancels owned requests, watchers, renewers and build work, and waits for cleanup before returning. `Run` remains the background-context compatibility wrapper. Parent cancellation does not discard uncertain journal records. A failed or canceled build cannot publish an engine result as success; checkout cleanup must also complete successfully.

`acceptance/runtime-hosting-build-controller/gate.sh` exercises genuine HTTPS Git, the production HTTP API, PostgreSQL, River and an enrolled CLI inside an owned disposable Linux Docker fixture. It checks Dockerfile, Railpack and prepared static HTTP output, default-lease renewal, committed-response loss/replay, failed and canceled builds, SIGTERM cleanup and an independent HTTP workload. It publishes no outer host ports and performs only identity-checked owned cleanup. This gate establishes build-to-artifact behavior, not deployment handoff.

The fixture waits for a successful TCP query to its intended PostgreSQL database. A Unix socket readiness response from the image's temporary initialization server cannot admit the test. By default PostgreSQL uses the exact pinned registry image. For a previously cached copy, set `NEROCD_BUILD_CONTROLLER_PRELOAD_POSTGRES=1`: the fixture verifies the original RepoDigest, local ImageID and expected platform, transfers that image, and verifies the same ImageID/platform inside its disposable daemon before using the ID. Missing or mismatched cache data fails closed. This option changes only fixture acquisition; it does not retag images, fabricate a digest, change DNS/TLS, or alter production runtime behavior.
