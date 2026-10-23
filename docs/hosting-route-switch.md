# Verified existing-route switches

`hostingproxy.PrepareVerifiedSwitch(identity, settings, generation, previous,
routes, requiredRouteIDs)` prepares an opt-in engine plan for switching existing
routes to new loopback host ports. This is a reusable engine mechanism for the
hosting R21/R25/R28 and A11/A20 requirements. The controller exposes a bounded
route-switch operation and admits only explicitly configured agents. R17 automatic
HTTPS deployment orchestration remains separate work. Metadata conflict recovery
also remains unsupported, as described below.

The caller must hold the existing fenced proxy lease, authorize the settled
healthy target bindings, and retain its hold when the engine reports `Uncertain`.
The engine does not stop containers, drain connections, change console database
generations, or make a controller lease atomic with Docker operations.

## Request and agent configuration

A current project maintainer can request an existing route switch through
`POST /api/v1/resources/{id}/route-switches`:

```json
{
  "idempotency_key": "release-b",
  "expected_loaded_generation": 12,
  "target_release_operation_id": "hop_b",
  "routes": [
    {
      "route_id": "route_web",
      "expected_version": 4,
      "expected_selected_release_operation_id": "hop_a"
    }
  ]
}
```

The request selects between one and 32 unique routes belonging to that resource.
The controller derives hostnames, paths, grants, placement, daemon identity, and
loopback ports. It accepts the current healthy release or an exact retained
running release on the same binding and daemon. Selecting a retained release does
not restart it, rebuild it, or deliver its historical secrets. The response is
the existing project-safe operation view, including for an administrator.

The runner must advertise `hosting_proxy_verified_switch_v1` and start with
`--proxy-verified-switch` plus the existing explicit proxy configuration. Agents
without that opt-in cannot admit or execute a strict operation. The optional
version 1 `verified_switch` descriptor binds the exact loaded predecessor and
selected prior targets into the ordinary immutable snapshot and compiler hash.
Absent that descriptor, legacy serialization and behavior remain unchanged.
Strict journal entries require format 9; legacy entries retain their existing
format. Restart replay sends durable receipts and never repeats Apply.

Admission locks destination ownership, the proxy, and all route resources in
sorted order. It checks current actor authority, grants, target eligibility,
versions, loaded predecessor, and agent capability. First mutation checks those
conditions again after ownership waits. Unrelated routes remain in the complete
snapshot. Ordinary route edits are refused while a strict operation is pending
or held, so they cannot publish a candidate before its verification completes.

## Revocation, metadata, and unresolved ownership

Disabling the proxy or withdrawing a hostname or destination grant commits the
revocation immediately. Before admission, the same transaction restores only
strict selections whose candidate version and release are still exactly owned,
supersedes that operation, and queues the withdrawal. After admission, it preserves
the strict hold and queues a safe withdrawal derived from the exact loaded
committed generation with every currently disabled or revoked route removed.
That queued withdrawal cannot dispatch through the strict hold. A later verified
outcome atomically settles ownership and replaces the deferred withdrawal from
post-outcome authorized metadata. Repeated receipts do not repeat that transition.

Metadata restoration is a compare-and-swap operation. It increments the route
version and restores its prior selected release only when the current candidate
selection and version still belong to the strict operation. A withdrawal that
increments the route version creates a conflict. The controller preserves the
engine's original receipt, exposes `metadata_conflict` separately, and retains
the mutation hold and both release references. It keeps revoked and disabled
flags effective. Regranting or enabling cannot publish the unresolved candidate.
A lost-response retry returns the original engine outcome with the current hold;
it does not clear the conflict or claim new TLS evidence.

There is currently no authorized API to reconcile a route-switch metadata
conflict. This case requires a future reviewed reconciliation mechanism and
cannot complete the automatic workflow in this slice. Operators must retain the
hold; direct database edits are not a supported recovery procedure. Uncertain
engine outcomes also retain ownership and references without automatic recovery.
The database revocation is immediate, while physical proxy withdrawal can remain
deferred by that uncertainty.

Successful strict completion requires complete unique route observations and
fresh selected HTTP and trusted HTTPS evidence for the candidate or exact prior
generation. Receipt replay validates the original immutable result without
rechecking certificate freshness or repeating metadata changes. Displayed receipt
verification is historical evidence, not a new live TLS probe. Current route
status exposes a conflict hold instead of suggesting delivery or withdrawal has
fully settled.

## Plan and predecessor contract

The plan copies and sorts a nonempty set of unique required route IDs. Every ID
must occur in the desired routes. Its generation must follow a nonzero previous
generation. Version 1 verification intent and the selected IDs contribute to the
plan hash. The static hash and legacy `Prepare` hash serialization stay unchanged.
Callers must persist the constructor choice and typed inputs, including required
IDs, to reconstruct the same plan after restart.

`Install` refuses verified-switch plans. `Apply` requires an installed proxy and
an exact committed predecessor. The route set, IDs, hostnames, and paths must
match that receipt. Only selected routes' host ports may change. All unselected
routes must match their predecessor values. Publication replaces the complete
generation, preserving unrelated routes and references within it.

Before publishing intent, the engine verifies the previous loaded generation,
HTTP and trusted HTTPS for every selected route, and the previous dynamic file
against its receipt. A failed baseline or scope check refuses the operation
without publishing a candidate.

## Verification and recovery

After publication, the engine waits for the complete candidate generation to
load and for selected routes to pass HTTP and trusted HTTPS probes. Each probe
requires the generation hash and route-ID response markers. Selected routes
reject server-error responses; application 401 and 404 responses can still prove
healthy routing, as with the existing marker contract. HTTPS uses ordinary chain,
hostname, and validity verification. Probes connect to host loopback with the
route's Host and SNI and ignore proxy environment variables. They do not require
new public DNS propagation.

Only successful verification permits a committed receipt and `Applied`. Exact
replay and `Observe` repeat selected-route verification. A selected certificate
that no longer verifies yields `Uncertain` and an error, including after a process
restart. Legacy plans continue to permit `Applied` with TLS pending.

On candidate verification failure, the engine restores the saved previous
dynamic file and verifies its loaded generation and selected HTTP and trusted
HTTPS routes. Only then does it restore the exact previous receipt and return
`RolledBack`, with previous route observations and loaded generation. The result's
desired generation and hash still identify the rejected candidate.

Cancellation or an unverified rollback leaves `Uncertain`. Recovery never escapes
the caller's context or starts detached retry goroutines. The engine durably
writes the versioned verification policy with intent before publication. A fresh
process refuses unfinished intent rather than inventing crash rollback or
downgrading it to legacy success. A caller that loses a response must reconcile
the exact plan under its hold.

## Runtime evidence

The package's opt-in Linux runtime fixture uses pinned Traefik and Pebble images
inside disposable Docker 29.4 DinD. Its private test CA proves trusted TLS, Host
and SNI selection, and distinct A/B backend responses. It does not prove
production ACME issuance or renewals against public certificate authorities.
The fixture checks unrelated route preservation and verified rollback on both an
unhealthy B and a candidate that serves an untrusted default certificate. It also
checks refusal of an untrusted baseline and strict replay and observation after
trust loss. Run once with `NEROCD_PROXY_SWITCH_FAULT=rollback` for failed rollback,
and once without that variable for cancellation after publication. Both cases
must retain `Uncertain` and the durable strict policy across fresh-engine calls.

The API fixture `TestHostingVerifiedSwitchCompiledRunner` runs a separately named
`nerocd_proxy_fixture` binary, the real API and PostgreSQL store, and pinned
Traefik/Pebble inside a task-owned Linux DinD daemon. Its constructor selects
`NewFixture` only in that build and with the explicit disposable-fixture marker.
The ordinary binary has no fixture constructor or environment CA override. This
fixture is local test-CA evidence; it does not establish public production ACME
issuance or R17 automatic deployment orchestration.
