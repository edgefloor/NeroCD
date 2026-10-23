# Trusted GitHub automatic deployment

This reference describes the optional `automatic_unprotected` deployment policy for an existing application. The default GitHub push policy remains build-only.

Automatic deployment requires a settled manual deployment of an exact local build artifact and existing HTTPS routes. It uses the existing local deployment and verified route-switch admissions. It creates no server, route, hostname grant, certificate authority, or runner capability.

The word `unprotected` refers to deployment approval policy. GitHub branch protection is independent of this mode. Other deployment approval modes are unsupported.

## Owner review

`GET /api/v1/resources/{id}/github-push-policy` returns `owner_automatic_deployment_options` only to a current owner with `can_manage=true`. The options also appear before a push policy exists.

One bounded database snapshot supplies the options. The query has a two-second deadline and takes no mutation locks. The options contain `available`, a closed `reason`, and an optional `baseline`. An unavailable response omits `baseline`.

A baseline contains these reviewed facts:

- The manual healthy operation ID.
- The loaded applied proxy operation ID and loaded generation.
- The current runtime configuration generation.
- The manual release's container port and readiness configuration.
- Between one and 32 eligible owned routes, with IDs, hostnames, path prefixes, versions, and selected release IDs.
- The current manual epoch.

The query reads at most 33 routes. More than 32 makes the options unavailable rather than silently selecting a subset. The private database snapshot does not leave the store. Secret values, artifact contents, daemon identity, and internal authority hashes are absent from the options.

The closed availability reasons are `eligible`, `unbound`, `manual_healthy_required`, `proxy_not_ready`, `runner_unsupported`, `routes_unavailable`, `restore_hold`, and `baseline_changed`.

Configure, verification, enable, and replay responses do not contain fresh options. Every configure request revalidates its selectors under the existing admission locks.

## Configuration

The optional `automatic_deployment` member of `GitHubPushConfiguration` contains:

```json
{
  "mode": "automatic_unprotected",
  "expected_healthy_operation_id": "manual-healthy-operation",
  "expected_loaded_proxy_operation_id": "loaded-proxy-operation",
  "expected_loaded_generation": 3,
  "expected_runtime_configuration_generation": 0,
  "container_port": 80,
  "readiness": {"path": "/", "timeout_seconds": 5},
  "routes": [{
    "route_id": "existing-route",
    "expected_version": 1,
    "expected_selected_release_operation_id": "manual-healthy-operation"
  }]
}
```

Port, readiness, and runtime generation copy the manual healthy baseline. They are not independent deployment defaults. The owner explicitly selects one to 32 existing routes. Saving the configuration leaves the push policy disabled. Fresh provider verification and explicit enable remain required.

An ordinary applied proxy operation qualifies the baseline when its current immutable receipt proves HTTP readiness, trusted HTTPS certificates, and the exact selected route targets. A verified switch can also qualify. A separate manual strict switch is not required solely to enable automation.

## Durable demand and admission

A signed event authorizes only its exact verified `after` commit. The provider proof, pinned build, desired pointer, current automatic demand, audit, and River wakeups commit atomically.

Each resource has one current demand. It freezes the exact build, commit, source and resource generations, runtime generation, placement, policy generation, manual epoch, original deadline, and stable deployment and switch IDs. New desired pushes replace this current row. Immutable automatic origins remain attached to admitted operations after replacement.

The demand expires no later than 24 hours after the original delivery receipt. Automatic retries do not extend this deadline. The 32-attempt budget includes verification attempts already spent by that delivery and subsequent automatic admission attempts. Waiting for an unfinished build or admitted operation does not allocate a new admission attempt.

Only a succeeded build with its exact qualified artifact can deploy. The controller requests `HostPort=0`, so the existing release-port allocator selects a fresh port. Failed, canceled, or incomplete builds do not create deployments.

The controller takes the established lock order: push quota, destination, proxy, sorted resources, then policy and demand. It performs no network IO in a database transaction. It calls the existing transaction helpers to commit the operation, immutable origin, audit, and River work together.

The first workload apply and the first proxy apply each recheck current desired demand and authority before writes and after blocking writes. These checks cover the delegating owner, service account, grants, managed-secret versions, source and resource generations, runtime generation, placement, capabilities, manual epoch, and database-clock expiry. Existing admitted replay preserves the original receipt instead of treating a newer desired push as permission for another mutation.

## Completion and recovery

Build, deployment, and proxy completions enqueue opaque resource IDs. These wakeups carry no source, credentials, plan, or authority. Completion never acquires the push quota or demand locks. A failed optional wakeup cannot discard an authoritative completion receipt.

The existing execution lifecycle runs an independent bounded recovery pass. Persisted demand remains discoverable after River job deletion, retry exhaustion, or a controller restart.

A deployment success is not HTTPS success. Final `succeeded` requires the exact automatic switch's applied receipt and a current loaded predecessor that preserves the exact owned route identities, versions, and targets without a hold. Unrelated resources may have advanced the global proxy generation. The summary records the actual observed loaded generation.

An older admitted switch can advance the accepted route baseline only with matching policy generation, manual epoch, exact origin, and current route evidence. Its receipt cannot mark a newer demand successful.

## Manual changes and uncertainty

A fresh accepted manual local deployment, route switch, route update, or route deletion increments the resource's durable manual epoch once. Exact replay does not increment it. Source, resource, runtime, and placement changes invalidate frozen authority through their existing generations and identities.

Supersession cleanup uses a separate task-first transaction. It checks immutable automatic origin, current mismatch or expiry, and the absence of both admission and hold. It then uses existing task cancellation, claim settlement, and route metadata compare-and-swap restoration.

Cleanup never releases an admitted mutation hold. It cannot overwrite newer manual route metadata. A metadata conflict retains uncertainty for reviewed recovery. Restored route versions can require a new manual review; the controller does not silently rebase route drift.

Rollback, failed readiness, or uncertain proxy state cannot produce automatic HTTPS success. Existing reconciliation remains responsible for admitted effects and immutable receipts, including after demand expiry or supersession.

## Qualification limits

Database fixtures exercise authorization, transactions, replay, expiry after lock waits, supersession, and recovery. Their receipt metadata does not prove real Docker execution or TLS issuance.

Usable automatic hosting additionally requires the compiled Linux agent, registered API, and UI journey to agree on the same reviewed configuration. Local fixture-CA HTTPS does not qualify production ACME, public DNS, or a live GitHub installation. This policy adds no workspace infrastructure or SSH onboarding.
