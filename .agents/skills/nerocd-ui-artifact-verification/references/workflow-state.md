# Workflow state

Apply these prompts to the affected operator flow, not every component. Before
extracting a hook or changing effects, identify which account, resource, request,
and evidence version owns each state value and what invalidates it.

- Distinguish public drafts, secret input, authorization state, pre-attempt consent,
  and an already-attempted mutation request. Their lifetimes differ. Preserve the
  existing public-draft contract while clearing secrets and inaccessible state on
  the relevant account or permission change.
- A late success or denial must affect only the request scope that owns it. Scope
  responses by identity or generation; cancellation alone is insufficient if a
  transport can still complete. Test account/resource changes with responses
  resolving in the opposite order, including a stale denial after a new success.
- Changing the evidence for an unsubmitted destructive action must retire its old
  confirmation. Merely hiding consent while identities differ can revive it on
  A → B → A. Test that sequence, not just A → B. Once attempted, retain the exact
  request and idempotency identity for retries where the API contract requires it;
  do not rebuild a retry from newly edited fields or silently broaden its authority.
- Represent mutation failure honestly. For example, a failed server logout must
  not present confirmed session termination. Distinguish local hiding or clearing
  from a server-confirmed result, and preserve a usable recovery or retry path.
- For hook extraction and React Doctor repairs, preserve effect dependencies and
  request lifetimes rather than deleting dependencies or suppressing diagnostics.
  Test the transition that could fail; a snapshot or a clean scan is not evidence
  that stale responses, authorization clearing, or retry identity still work.

Use controlled promises or request intercepts to prove ordering without sleeps.
Include successful authorized behavior alongside rejection cases. Browser fixtures
must supply the metadata the flow actually reads, including schedules and retention
where relevant; do not weaken production authorization to compensate for a missing
fixture. Use semantic roles when asserting lists and controls after markup changes.
