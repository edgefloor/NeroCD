# Multi-Factor Authorization Decision Under Role Dominance

Source: `docs/plan.md` §1, §11; `CONTEXT.md`
Skeleton: `Aggregation under dependence`

## Problem

A system decides whether to allow a requested action. Each action belongs to
exactly one of six classes:

- **A1 govern-tokens** — mint or revoke platform-wide bearer credentials.
- **A2 govern-workers** — register, rotate, or revoke long-running workers.
- **A3 domain-read** — read items in one named domain.
- **A4 domain-operate** — change items in one named domain.
- **A5 domain-admin** — change who is a member of one named domain.
- **A6 worker** — a worker heartbeat, claim, log append, or completion for one
  specific worker.

A **principal** has:

1. A **credential kind** in {`session`, `platform-token`, `worker-token`}.
2. A **global role** in {`platform-admin`, none}. Only `session` principals
   carry a global role.
3. A set of **domain memberships**, each `(domain, role)` with role in
   {`read`, `operate`, `admin`}. Only `session` principals hold domain
   memberships. Roles are hierarchical: `admin` implies `operate` and `read`
   in that domain; `operate` implies `read`.
4. If the credential kind is `platform-token`, a **token scope** in
   {`full-admin`, `worker-admin`, none}. If the kind is `worker-token`, a
   single **bound worker** id.

The decision rule (the claim to check):

- **A6**: allow iff credential = `worker-token` AND bound worker = target
  worker. No other attribute authorizes A6.
- **A1**: allow iff global role = `platform-admin` OR (credential =
  `platform-token` AND scope = `full-admin`).
- **A2**: allow iff global role = `platform-admin` OR (credential =
  `platform-token` AND scope in {`full-admin`, `worker-admin`}).
- **A3**: allow iff global role = `platform-admin` OR (credential =
  `platform-token` AND scope = `full-admin`) OR (credential = `session` AND
  the principal holds any domain role in the target domain).
- **A4**: allow iff global role = `platform-admin` OR (credential =
  `platform-token` AND scope = `full-admin`) OR (credential = `session` AND
  domain role in {`operate`, `admin`} in the target domain).
- **A5**: allow iff global role = `platform-admin` OR (credential =
  `platform-token` AND scope = `full-admin`) OR (credential = `session` AND
  domain role = `admin` in the target domain).

A `platform-token` principal carries no domain memberships and no global role;
it acts only through its token scope. A `worker-token` principal carries no
domain memberships, no global role, and no scope beyond its bound worker.

## Checks

1. [case] A principal has credential = `platform-token`, scope =
   `worker-admin`, no global role, no domain memberships. Enumerate exactly
   which of A1–A6 are allowed, and for A6 state whether the bound-worker
   condition can ever hold.
2. [dominance] Is a `session` principal with global role `platform-admin`
   never less authorized than a `session` principal with domain role `admin`
   in a single domain D, across all six action classes? Separately, is
   `platform-token` with scope `full-admin` never weaker than with scope
   `worker-admin`? Answer yes/no for each, with a one-paragraph proof or a
   concrete action class where dominance fails.
3. [reduction] Suppose global roles can no longer be assigned (every principal
   has global role = none) and `platform-token` can no longer carry scope
   `full-admin`. Which action classes become impossible to authorize, and does
   any surviving rule still grant A1 or A2?
4. [counterexample] Using only the rule above, construct one concrete principal
   (a full attribute set) and one target action that the rule mis-licenses —
   either granting an action that no role or domination should permit, or
   denying one that the hierarchy should permit. If no such case exists, say so
   and prove it by exhausting the credential-kind splits.
5. [calibration] Name the minimum distinguishing evidence the system must
   record to decide whether to grant a principal `platform-admin` (global)
   versus only `admin` in one domain. State the smallest set of records that
   separates the two.
6. [open] Is maintaining a distinct `worker-token` credential kind (rather than
   one token kind whose scope includes worker actions) necessary for the A6
   isolation guarantee, or could scopes alone enforce it? Defend briefly.

## Answer Format

Return numbered answers matching the checks above. For yes/no checks include a
one-paragraph proof or a concrete counterexample. Do not use project-specific
terms.
