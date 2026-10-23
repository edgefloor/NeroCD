# Verified existing-route switches

`hostingproxy.PrepareVerifiedSwitch(identity, settings, generation, previous,
routes, requiredRouteIDs)` prepares an opt-in engine plan for switching existing
routes to new loopback host ports. This is a reusable engine mechanism for the
hosting R21/R25/R28 and A11/A20 requirements. Controller capability negotiation,
the route-switch API and admission, and automatic HTTPS deploy orchestration
remain separate work. This change does not complete R17 automatic HTTPS deploy.

The caller must hold the existing fenced proxy lease, authorize the settled
healthy target bindings, and retain its hold when the engine reports `Uncertain`.
The engine does not stop containers, drain connections, change console database
generations, or make a controller lease atomic with Docker operations.

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
