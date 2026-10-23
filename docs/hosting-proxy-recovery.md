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

## Administrator workflow

Open **Servers and runners**, select the visible hosting server, and use
**Recover a completed metadata conflict** in its managed proxy section. A held
operation is selected automatically. Recovery requires a system administrator
or a runner administrator with owner access in the immutable control project;
the server rechecks current authority for every read and write.

Read the recovery status and accepted completion receipt. Unsupported or
unfinished work explains why no plan is offered. Missing accepted completion,
uncertain engine outcome, changed predecessor, restore ownership and competing
work remain outside this recovery mechanism.

Request a server recovery plan, then review its exact receipt and hashes, loaded
and desired generations, selected route versions and release-pointer changes,
unchanged enabled/authorized flags, and safe convergence contents. Confirm the
specific metadata repair before applying it. The original database creation and
expiry are displayed. Local clock text is informative; the server decides
whether the original 30-second plan has expired. The UI does not silently renew
or apply a plan.

An uncertain plan or apply response freezes its exact in-memory request and key.
Only an identical retry is offered. Apply replay remains possible after expiry
if that exact request already committed. A conflict requires reading fresh facts,
requesting a new server plan with a new key, reviewing it and confirming again.
Nothing is automatically resubmitted.

The URL retains only the original non-secret operation locator, scoped to the
actor, project and proxy. It retains no review, plan, key or authority. Reload and
select the same visible server to read the original operation's immutable
resolution, even when the proxy's active operation has advanced to convergence.
An advanced exact-ID lookup is also available. Every lookup verifies operation
placement and server authorization; a foreign scope cannot revive an old review.
Identity, permission, project, proxy or operation changes abort outstanding work
and fence late responses.

A recorded resolution displays metadata settlement and the one queued ordinary
convergence separately from the original historical engine result. It does not
claim that convergence completed, the loaded generation advanced, TLS is freshly
healthy, data was rolled back or grants were restored. There is no general unlock
control.

## Local UI qualification

After building the frontend, run
`bash web/app/scripts/hosting-proxy-recovery-fixture.sh` from the repository root.
The script uses two separately labelled disposable Linux Docker daemons. The
first reruns the original compiled metadata-recovery fixture. The second reuses
its genuine failed-B rollback, hostname revocation, accepted completion conflict
and durable lost-completion journal before exposing the registered API and
embedded UI through a random host-loopback port.

The browser creates, reviews and applies real server plans. Elapsed database
expiry is tested without editing clock or plan records. Deliberately lost plan
and apply responses test exact replay, including concurrent identical apply
retries after expiry. The fixture verifies immutable original receipts, unchanged
loaded generation and route flags, one resolution and one added convergence;
the existing compiled-agent journal acknowledgement assertions still run.
Responsive review and keyboard checks cover 320, 390, 430 and 1440 pixels.

Synthetic stale request input and unsupported-status rendering are separate UI
fault tests. They do not fabricate authoritative receipts or runtime recovery.
The private Pebble CA qualifies local fixture TLS only, not production issuance,
renewal or application health after queued convergence. Fixture controls require
the fixed administrator session and exact loopback Origin, and accept only the
fixed verification/completion actions for those fixture identities.
