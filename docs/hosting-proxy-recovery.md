# Recover a completed proxy metadata conflict

This administrator API repairs selected release pointers after an accepted strict
route switch finished with `rolled_back` or `refused`, but concurrent metadata
changes prevented automatic restoration. It preserves the engine result and its
completion receipt. Historical routing and certificate observations do not prove
current health or TLS readiness.

Recovery requires current server administrator authority and owner authority in
the proxy's immutable control project. A resource destination or hostname grant
can remain revoked. Recovery never enables a route or restores a grant.

1. Read `GET /api/v1/proxy-operations/{id}/recovery`. An eligible response includes
   the original accepted completion receipt, loaded and desired generations,
   selected route versions and release pointers, server-derived prior targets,
   and the authorized convergence routes.
2. Create a review with `POST /api/v1/proxy-operations/{id}/recovery-plans`. Send
   `idempotency_key`, `expected_loaded_generation`, `expected_desired_generation`,
   `expected_complete_receipt_id`, and `expected_complete_receipt_hash` from that
   observation. Check the returned review and `plan_hash`.
3. Apply the exact review with
   `POST /api/v1/proxy-operations/{id}/recovery-plans/{plan_id}/apply`. Send only
   `idempotency_key` and `plan_hash`.

A plan expires 30 seconds after creation using the database clock. There can be
only one unexpired plan for an operation, across all actors. A different key
conflicts until the existing plan expires. Replaying the same actor, key and
request returns the original review and expiry, including after expiry. It does
not renew the plan. Create a new key after expiry to review current facts.

Apply rechecks the whole review, current authority and expiry. Route versions
increase when a selected release pointer is repaired. Disabled routes, revoked
delegations, hostnames, paths and ownership remain unchanged. The transaction
records an immutable resolution, settles only the matching completed hold, and
queues one ordinary authorized convergence derived from the loaded predecessor.
It never derives that generation from pending candidate metadata. Any failure
leaves the hold and metadata unchanged. The loaded generation does not change
until the ordinary runner operation completes.

If an apply response is lost, retry the same operation, plan, actor, key and hash.
The existing resolution is returned without another metadata change or queued
operation, even after plan expiry. Current read authorization still applies.
The original runner journal can replay its original completion receipt and
acknowledge it without applying that strict switch again.

Recovery changes controller metadata only. It does not contact Docker, restart
containers, deliver secrets, delete retained releases, probe routes or issue
certificates. Existing loaded and queued release references retain their cleanup
protection. The original result, receipts, conflict audit and strict predecessor
references remain immutable evidence.

Missing completion receipts, unfinished attempts, expired leases without accepted
completion, uncertain engine outcomes, foreign or alternate authority, changed
loaded predecessors, restore holds and competing admissions remain unsupported.
There is no generic unlock or manual effect-adoption action in this API.
