# NeroCD Permanent Plan

This plan tracks the next durable implementation order for NeroCD. The current
repository has a working control-plane slice: Go API, PostgreSQL-backed demo
data, a Vite WebUI, embedded assets, Docker dev wiring, real route-level
OpenAPI validation, first mutations, runner leases, local process execution,
and a strict dependency policy. That is enough foundation; the next work must
turn it from a happy-path demo into an operable automation control plane.

## Current Coverage Audit

Verified on this tree:

- `go test ./...` passes when run with workspace-local `GOCACHE`/`GOTMPDIR`.
- `cd web/app && bun run test` passes.
- `cd web/app && bun run build` passes.
- `go run ./cmd/nerocd contract` passes for 44 routes.
- `node scripts/supply-chain-policy.mjs` passes for 462 components.
- `.github/workflows/check.yml` runs `make check` plus PostgreSQL integration
  tests against a provisioned Postgres service.
- `go run ./cmd/nerocd smoke` now exercises session login, paginated reads,
  workflow run creation, runner registration, two-step runner claims, logs,
  artifact records, and final run completion against a running server.
- `go test ./... -cover` does not currently work in this local toolchain:
  `go: no such tool "covdata"`.

## Semaphore UI Comparison Roast

Semaphore UI is a useful benchmark because its public product/API shape is not
just "templates and runs". Its OpenAPI/Swagger documentation exposes projects,
project users and roles, events, access keys, repositories, inventories,
variable groups/environments, integrations, schedules, templates, tasks, task
output, and user API tokens. The product surface also makes the operator
promises explicit: project isolation, role control, inventories, scheduling,
centralized logs/audit, runners at scale, and managed secrets.

Against that bar, NeroCD has a cleaner runner boundary than generic web-triggered
automation, but the product model is still thin:

- We have projects, members, repositories, templates, runs, approvals, runner
  leases, logs, and audit events. That covers the skeleton.
- We now have project-scoped inventories and access-key metadata. We do not yet
  have environment/variable groups, integration aliases/webhook matchers,
  schedules, project backup/restore API, or user API token lifecycle.
- Project role introspection now exists, but it is coarse; it reports the
  effective role and broad `can_view`/`can_run`/`can_admin` permissions rather
  than per-resource policy decisions.
- Our runner token split is the right direction, and rotation/revocation now
  exists. Bootstrap admin now receives a persisted `system_admin` role during
  session authentication, and non-admin users are blocked from runner
  registration and token management. First platform API-token bootstrap now
  exists; richer service-account lifecycle is still missing.
- Audit scoping is project-aware, but the event model is smaller than a real
  project event stream and still lacks strong failure-path coverage.
- The WebUI has operator views, but Semaphore's task-template console exposes
  last task, expandable task history, rebuild/build actions, and direct
  inventory/environment/key-store navigation. NeroCD still feels more like an
  API dashboard than an automation console. Manual run requests now open a
  terminal-style log popup, run rows expose the same focused log view, and
  saved templates now have direct Play actions so operators do not have to use
  a separate request-run form.
- The worst gap has moved: access-key and inventory metadata exist, but secret
  values are still intentionally absent until encryption/backend rules exist,
  and runner execution does not consume inventory or key references yet.

The codebase is no longer just a read-only scaffold, but it is still not
production-shaped. Test coverage is concentrated in API happy paths plus
authorization and runner lifecycle regressions, CLI contract checks, memory-store
lease tests, two runner executor tests, and six frontend unit tests. PostgreSQL
behavior, deeper service-layer validation, browser workflows, and failure modes
are still thinly covered or not covered at all.

Blunt status:

- The API surface is ahead of the product guarantees. Routes exist, but many
  behaviors are "first implementation" quality.
- Authentication exists, project-scoped authorization is enforced for
  project/repository/template/run/member reads and mutations, runner work uses
  dedicated runner tokens, and audit visibility is scoped for project-linked
  events. Project role introspection now exposes effective project permissions.
  Broader RBAC boundaries still need more depth, but the first persisted
  `system_admin` slice is real for global runner administration.
- Runner registration can use an admin-minted platform API token for unattended
  bootstrap. API tokens now carry service-account kind, roles, optional expiry,
  and can be scoped to `runner_admin` instead of full `system_admin`.
- PostgreSQL support is implemented but not exercised by the normal unit suite.
  The memory store can hide transaction, constraint, JSON, array, and locking
  bugs.
- List endpoints now return explicit `limit`, `offset`, `count`, and `total`
  metadata. Runs, run logs, artifacts, and audit events use store-level
  pagination on direct API list paths; lower-volume lists still slice after
  loading.
- Workflow now has sequential step execution state and dependency-ready claims.
  Approval steps, parallelism, retries, and per-step operator controls are still
  aspirational.
- Runner primitive plans expose checkout, process, artifact, and secret
  metadata. Process execution, git checkout, and filesystem artifact presence
  checks, and local `env` provider secret injection are real. Access-key and
  inventory metadata now exist behind project RBAC, but inventory use, artifact
  upload/retention, and production secret backends are not operational.
- The WebUI builds, has basic unit coverage, and now has committed Playwright
  smoke coverage for login plus projects/templates/runs/logs/audit navigation.
  Manual run request terminal logging has also been browser-verified.
- Error envelopes now include stable `code` values plus the human `error`
  message. The codes are still coarse, but external clients no longer have to
  parse prose for the common error classes.

## 1. Make Auth Real

Status: implemented for local user sessions, first project-scoped
authorization, and a first persisted global role. Bearer middleware resolves
hashed session tokens, protected routes reject missing/expired/revoked tokens,
local password validation exists, `/api/v1/me` reflects the presented token
including `system_admin`, the CLI can pass tokens, project membership controls
scoped reads and mutations, bootstrap admin sessions now carry the
`system_admin` role from the user record, and system admins can mint/revoke
platform API tokens for unattended bootstrap. Remaining gaps: no full
service-account model, no password policy, no session listing, no audit-event
scoping, and no environment-level RBAC.

The current session endpoint issues real local session tokens. The next auth
work is authorization, not authentication plumbing.

- Add bearer-token middleware that resolves sessions from hashed tokens. Done.
- Reject expired or revoked sessions. Done.
- Keep `/api/v1/health`, `/api/v1/ready`, and session creation explicitly
  unauthenticated. Done.
- Add local login semantics that are not just "email exists". Done.
- Add role/scoped authorization checks before treating project membership as
  anything more than displayed metadata. First project-scope slice done.
- Add persisted global user roles and surface them through session
  authentication before using `system_admin` for runner and platform
  administration. First runner-admin slice done.
- Add service-account/API-token groundwork only after user sessions work. First
  platform API-token slice done.

Definition of done:

- `/api/v1/me` reflects the presented bearer token.
- Authenticated routes return `401` without a valid token.
- Session lookup, expiry, revocation, and error cases are covered by tests.
- Persisted global roles are loaded into principals and covered by memory-store,
  PostgreSQL-store, and API authorization tests.
- The CLI can store or pass a token for authenticated calls.

## 2. Add First Mutations and Audit

Status: mostly implemented for first mutations. Project, repository, template,
run request, approval/rejection/cancel, project-member, session revocation, and
audit mutations exist, including ad-hoc runs that do not require a template.
Audit metadata now carries request IDs. Remaining gaps: validation/error-path
coverage is thin, archive/update coverage needs to be strengthened, mutations
are not consistently transactional with their audit/log side effects, and
runner/audit authorization boundaries still need work.

The product cannot remain list-only. Add narrow, auditable mutations before
building more UI surfaces.

- Create, update, and archive projects. Done.
- Create repositories. Done.
- Create and update task templates. Done.
- Request a task run from either a template or an explicit `RunSpec`. Done.
- Represent approvals as first-class records, not only a run status string.
  Done.
- Write an append-only audit event for every user-visible mutation. Mostly done;
  verify every mutation path and make audit writes transactional where the
  backing store supports transactions.

Definition of done:

- Mutations return the changed resource and use stable error envelopes.
- Audit events include actor, action, target, timestamp, request ID, and metadata.
- API tests cover happy paths, validation, auth failures, and audit writes.
- Runs are not required to reference templates.
- Templates produce `RunSpec` defaults instead of being the execution model.
- The WebUI exposes the new mutations without inventing undocumented routes.

## 3. Strengthen the API Contract

Status: mostly implemented. The contract command parses OpenAPI, validates route
coverage, auth metadata, JSON request bodies, and representative response
shapes, verifies stable coarse error codes, and currently passes for 44 routes.
Remaining gaps: no generated
frontend client, no enforcement that frontend route usage is generated from the
contract, database-level pagination is not implemented in the stores, and
route-specific machine-readable error codes are still too coarse.

The current contract command is good enough to catch obvious drift. Move from
"server and spec mostly agree" to "clients and server cannot drift silently".

- Replace the line-scanner contract check with real OpenAPI parsing/validation.
- Validate request parameters, response schemas, auth requirements, and error
  shapes.
- Add stable machine-readable error codes. First coarse slice done.
- Generate or type-check the frontend API client from the contract.
- Keep list pagination documented in OpenAPI and move slicing into the backing
  stores before any list endpoint can grow unbounded.

Definition of done:

- `make check` fails on schema drift, missing auth declarations, invalid YAML, or
  undocumented frontend/CLI API consumption.
- List endpoints have explicit pagination contracts.
- Contract tests exercise representative live handler responses.

## 4. Build RunSpec, Workflow, and Runner Primitives

Status: partially implemented. `RunSpec`, repository metadata, workflow structs,
workflow execution state, artifact/secret metadata, access-key metadata,
inventory metadata, validation, and runner primitive plan projection exist.
Process execution, git checkout, filesystem artifact presence checks, runner-
recorded artifact metadata, sequential workflow step execution, and local `env`
provider secret injection are operational runner primitives, and shell runs now
build process plans from `run_spec.inputs.command`. Remaining gaps: workflow is
sequential only, artifact binary upload/retention enforcement is not real,
database/vault secret backends are not real, inventory primitives are metadata
only, and approval-as-workflow-step is not operational.

The execution model must not collapse into a template god object. Build the
intermediate layers before adapter-specific behavior.

- Keep `RunSpec` as the immutable execution contract captured on every run.
- Add repository metadata and a checkout primitive shared by every adapter.
  First git checkout slice done.
- Add process execution, cancellation, timeout, and exit-code primitives.
  Cooperative cancellation is implemented for the local runner process path.
- Add artifact capture and retention metadata. Filesystem presence checks and
  durable artifact records are done; binary upload/storage and retention
  enforcement remain.
- Add inventory and access-key metadata. First project-scoped metadata slice
  done.
- Add secret injection boundaries that do not leak values into logs. First local
  runner environment-provider slice done; database/vault providers remain.
- Add a workflow/execution-plan model for ordered and parallel steps. Sequential
  ordered step execution is done; parallelism remains.

Definition of done:

- Ad-hoc runs and template-derived runs use the same `RunSpec` path.
- A workflow can express at least checkout, plan, approval, and apply as separate
  steps.
- Adapter implementations do not each own git, process, artifact, or secret
  handling.
- The API exposes source repositories and a runner primitive plan for a run.

## 5. Build Runner Registration and Leases

Status: mostly implemented for the control-plane loop. Runners can register,
heartbeat, poll continuously, claim matching queued runs by tag/capability,
receive a registry-built primitive plan, append lease-bound logs, and complete
leases through both memory and PostgreSQL stores. Runner registration now uses a
user token only to mint a dedicated runner token; heartbeat, claim, log append,
completion, and lease status checks require the runner token and derive runner
identity from it. System admins can rotate or revoke runner tokens, non-admin
users are forbidden from runner registration and token management, platform API
tokens can perform unattended runner registration, sequential workflow runs can
advance across multiple runner claims, and old tokens stop
authenticating after either operation. Stale-runner behavior has direct
memory-store coverage, expired leases are actively reclaimed back to queued runs
during claim, runner lifecycle audit persistence is covered, and the PostgreSQL
runner status constraint now accepts revoked runners. A gated PostgreSQL
integration test now exercises migrations, seed data, global roles, API-token
auth/revocation, runner registration/revocation, JSON run fields, lease
claim/complete, logs, and audit persistence when `NEROCD_TEST_DATABASE_URL` is
set, and CI now provisions Postgres for that path. Remaining gaps: a richer
service-account lifecycle, a narrower runner-admin role model beyond global
`system_admin`, and failure-path audit coverage.

Runners are the core product boundary. Do not wait for a polished UI before
modeling them.

- Add runner records with tags, capabilities, status, and last heartbeat.
- Add signed or tokenized runner registration. First bearer-token slice done.
- Add heartbeat and lease APIs.
- Add run assignment semantics based on tags and capabilities.
- Require a real persisted `system_admin` role for runner registration, token
  rotation, token revocation, and any future global runner configuration. Done
  for registration, rotation, and revocation.
- Persist runner lifecycle audit events.

Definition of done:

- A local `nerocd runner` process can register, heartbeat, poll, claim queued
  runs, and release or fail them.
- Stale runners stop receiving work. Done for claim-time stale marking.
- Lease behavior is tested without relying on wall-clock sleeps. First
  memory-store slice done.

## 6. Implement Execution Safely

Status: mostly implemented for constrained local process execution. The
runner can claim queued shell/process runs, execute git checkout when a
repository plan is present, append system/stdout/stderr logs, enforce the
process timeout, map exit results to succeeded/failed/canceled, prepare local
environment-backed secrets without logging values, check declared filesystem
artifacts, record artifact metadata, fail when required artifacts are missing,
execute sequential workflow steps across claims, and complete the lease. A user-
facing cancel API exists, cancels the active lease, and the local runner polls
runner-scoped lease status to cancel an in-flight process. Remaining gaps: no
artifact binary upload/retention backend and no implemented database/vault
secret backend. The database/vault backend design is documented in
`docs/secrets.md`.

Only after leases exist should execution adapters appear.

- Start with shell execution in a constrained local/dev runner. Done for
  `run_spec.inputs.command` and explicit `run_spec.process.command`.
- Stream stdout, stderr, and system events into immutable run logs.
- Add cancellation and timeout handling.
- Capture exit codes and transition runs through queued, running, succeeded,
  failed, and canceled states.
- Check declared artifacts after execution and surface missing required
  artifacts in run logs. First local filesystem slice done.
- Inject declared `env` provider secrets from the runner host environment into
  process environment targets without logging references or values. First local
  secret boundary slice done.
- Keep Ansible, OpenTofu/Terraform, PowerShell, and Python adapters as thin
  metadata wrappers over shared runner primitives.

Definition of done:

- A run can be requested from the UI or CLI and executed by a runner.
- Logs stream or refresh incrementally.
- Failed commands produce useful status, logs, and audit records.
- Adapter tests prove state transitions and log ordering.

## 7. Make the UI Operable

Status: mostly implemented. The frontend typecheck, unit tests, Vite
production build are currently green. The WebUI has authenticated API plumbing
and mutation clients for the current backend surface. Manual run request now
starts from saved template Play actions, opens a terminal-style log popup, and
run rows can open focused terminal logs. CLI/API smoke coverage proves the
first API-level operator workflow, and Playwright smoke coverage proves the
first browser navigation workflow. Remaining gaps: more realistic error,
permission, empty, loading, and log-following states.

The current WebUI is past a static dashboard, but it is not yet a trustworthy
operator console. Convert it into a browser-verified workflow surface as backend
capabilities land.

- Add authenticated session handling.
- Add project and template forms.
- Add run request, approval, cancel, and log inspection workflows.
- Add template-row Play actions with terminal-style focused logging for
  requested runs. Done.
- Keep roadmap tabs only when backed by live capability status.
- Add empty, loading, error, and permission-denied states per surface.

Definition of done:

- Operators can complete the first end-to-end workflow from the browser:
  authenticate, create/select a project, create/select a template, request a run,
  approve if required, watch logs, and inspect audit history.
- Frontend tests cover view-model behavior and critical API error states.
- Playwright or equivalent smoke coverage verifies the first workflow.

## 8. Production Hardening

Status: mostly implemented. Request IDs, readiness, lightweight metrics,
runtime configuration validation, tracked SQL migrations with checksums, Docker
startup through the migrator, PostgreSQL backup/restore documentation,
CycloneDX-shaped SBOM output, CI wiring, PostgreSQL integration, browser smoke,
and production secret-backend design exist. Remaining gaps: no full release
pipeline, no external metrics backend integration, no automated restore drill,
and no implemented production secret backend.

Once the first workflow works, harden operational behavior instead of adding
more adapters.

- Add request IDs, structured error mapping, and readiness checks.
- Add database migration tracking instead of reapplying one SQL file.
- Add configuration validation and secret handling rules.
- Add metrics and health/readiness distinction.
- Add backup/restore documentation for PostgreSQL.
- Generate standard SBOM output, not a project-specific pseudo format.

Definition of done:

- Docker and local binaries expose the same runtime behavior.
- Migrations are idempotent, ordered, and tracked.
- Release artifacts include checksums and a standards-based SBOM.
- CI covers unit, contract, frontend, dependency policy, and smoke checks.

## 9. NIS2 Security Baseline

The NIS2 gap analysis is directionally useful: NeroCD has a clean control-plane
shape, contract checks, token hashing, SBOM output, audit events, leases, and
runner primitives, but it is still not a compliant operating model. The sharpest
gaps are not paperwork gaps; they are concrete engineering gaps: unsalted
password hashes, plaintext-by-default transport, no rate limiting, repository
URL SSRF risk, bearer-token-only runners, direct host process execution,
deletable audit tables, weak approval evidence, no external identity/MFA, and no
automated restore drill.

The NIS2 implementation plan should be treated as a security backlog, not as a
status report. Several entries call slices "Done" while retaining critical
exceptions; in this plan those exceptions remain open until implemented and
tested.

Critical baseline before broad enterprise expansion:

1. Replace unsalted SHA-256 password storage with bcrypt or Argon2id, keep a
   legacy verifier only for migration, and rehash on successful legacy login.
2. Add TLS server configuration with a production mode that refuses plaintext.
   Keep reverse-proxy deployment supported, but make insecure local/dev mode
   explicit.
3. Add rate limiting for session creation, runner registration, runner polling,
   and mutation endpoints, with stable `429` error envelopes.
4. Validate repository URLs before persistence and checkout. Reject `file://`,
   link-local, loopback, metadata-service, and private-network targets unless an
   explicit internal-repository policy is configured.
5. Bind runner identity beyond bearer-token possession. Target mTLS plus
   per-runner certificate fingerprinting; keep token auth only as a migration or
   development mode.
6. Add signed execution tokens that bind runner operations to a specific
   `run_id`, `lease_id`, `runner_id`, and workflow step.
7. Add an isolated execution mode for runners. Direct `exec.Command` can remain
   a development mode, but production should have ephemeral container execution
   with resource limits and constrained network/filesystem access.
8. Harden audit storage with database-level append-only protections and a
   forwarding path to SIEM/syslog/OpenTelemetry.
9. Add approval reasons and policy hooks for multi-approver production actions.
10. Automate PostgreSQL restore drills and release signing/checksum
    verification.

Definition of done:

- The insecure development defaults are impossible to confuse with production
  defaults.
- Security controls are covered by unit/integration/contract tests, not only
  documentation.
- Runner identity, execution authorization, audit durability, and repository
  source validation are enforced by the server or runner, not by operator
  convention.

## 10. Idiomatic Go and Data Layer Cleanup

Post-MVP execution todo:

1. Done: make the green baseline real by fixing runner stream test flakiness
   and keeping
   `go test ./...` green locally and in CI.
2. Done: add typed domain state for statuses, roles, token kinds, streams,
   artifact kinds, providers, and workflow states.
3. Done: make run creation transactional across run, initial log, approval, and
   audit writes where the backing store supports transactions.
4. Done: make lease completion transactional across lease, run status, workflow
   advancement, completion log, and audit writes.
5. Done: replace comma-joined PostgreSQL arrays with native typed scanning.
6. Done: migrate PostgreSQL store SQL to native pgx and checked-in sqlc queries.
   Store CRUD, JSON-heavy run/template paths, identity/session/API-token paths,
   runners, leases, approvals, artifacts, audit events, and critical run claim
   maintenance now use generated sqlc query APIs behind the existing
   repository interfaces.
7. Done: make the supply-chain policy green again before broad feature work.
   The direct Go dependencies are reviewed in `docs/dependency-exceptions.md`,
   the policy allowlist matches the current direct module set, and Go MIT
   license detection handles standard MIT text without requiring an exact
   heading.
8. Done: replace runner-side `map[string]any` API request/response handling
   with typed structs for registration, heartbeat, claim, lease status, logs,
   artifacts, and completion. Generic JSON helpers remain for smoke and
   contract assertions that intentionally inspect arbitrary envelopes.
9. Add validation and failure-path coverage for membership, templates, runs,
   approvals, runner tokens, and artifacts.
10. Improve the WebUI run-log operator experience with follow/polling,
   terminal-state clarity, and stronger error/permission states.
11. Automate restore drill and finish release artifact pipeline once the code
    baseline is green.

Immediate next slices:

1. Done: repository source validation rejects unsafe repository URLs before
   persistence, run-spec capture, workflow normalization, and runner checkout.
   Blocked sources include local paths, `file://`, loopback, link-local,
   metadata-service, private-network, and unspecified IP targets. Public
   `https`, `http`, `ssh`, `git`, and scp-style Git hosts remain allowed.
2. Session hardening slice: replace unsalted SHA-256 Local User password hashes
   with bcrypt-only hashes. Update memory and PostgreSQL seed users in the same
   slice, reject legacy password hash prefixes instead of migrating them, and
   document the dependency-policy exception. Existing non-password SHA-256
   fingerprints and checksums are out of scope.
3. Rate-limit slice: add in-process rate limiting for session creation, runner
   registration, runner polling/claiming, and high-impact mutations with stable
   `429` error envelopes and contract coverage.
4. Log-follow UI slice: add run-log polling/follow controls, terminal state
   indicators, and explicit permission/error states around the focused run-log
   dialog, with unit coverage for the view-model behavior.

The current implementation has useful package boundaries, small repository
interfaces, and readable service methods, but the Go code still carries several
MVP-shaped tradeoffs: stringly typed domain states, a constructor with too many
repository parameters, hand-written SQL scan lists, ad hoc PostgreSQL array
conversion, non-atomic multi-repository use cases, and an oversized CLI
entrypoint. Before adding the next wave of enterprise features, tighten the
implementation so future changes fail at compile/test time instead of through
runtime drift.

- Introduce named domain types and constants for roles, statuses, token kinds,
  runner states, log streams, artifact kinds, providers, and workflow states.
- Group service dependencies behind a small `Repositories` struct or a narrower
  application store interface instead of passing every repository individually.
- Move run creation, approval creation, initial log writes, audit writes, lease
  completion, and workflow advancement toward transaction-shaped store methods
  where the backing store supports transactions.
- Replace string-based PostgreSQL array handling with native typed scanning or
  generated code. Avoid comma-joined arrays for roles, tags, and capabilities.
- Split `cmd/nerocd/main.go` into command-focused files or small command
  objects so server boot, runner loop, migration, smoke, contract validation,
  and API client code do not keep growing in one file.
- Replace `map[string]any` API client round-trips in the runner path with typed
  request/response structs.
- Fix and de-flake runner process stream tests before treating `go test ./...`
  as a green baseline again.

Use sqlc as the database-first query generator rather than a wholesale Active
Record rewrite. Checked-in query wrappers under `internal/store/sqlcgen` are
generated from repository migrations and owned SQL with `go tool sqlc generate`.
The PostgreSQL repository uses native pgx pooling and transactions, explicit
domain adapters, and narrowly reviewed handwritten transaction orchestration for
lease authority, bounded claiming, workflow transitions, and shared log
ordering. Regeneration is schema-server independent and deterministic.

Definition of done:

- Domain state strings have named constants and tests cover invalid transitions.
- `go test ./...` is green locally and in CI.
- The store layer no longer relies on comma-joined PostgreSQL arrays.
- PostgreSQL store query wrappers are generated by sqlc. Native pgx transactions
  retain the repository's atomic authority transitions and lock ordering while
  generated queries cover the stable CRUD and pagination surface.

## 11. Expand Enterprise Surface

After the control-plane loop is real, add the corporate features from the
architecture document in dependency-conscious slices.

- First slice implemented: project membership records with `owner`,
  `maintainer`, and `viewer` roles; audited grant/update API; OpenAPI contract;
  seed data; and a Settings workflow for viewing and updating project access.
- First scoped authorization slice implemented: project members can view scoped
  project data; maintainers can operate project resources and runs; owners can
  manage project access.
- Global admin first slice implemented: users have a persisted global role,
  bootstrap admin is seeded as `system_admin`, `/api/v1/me` includes the role,
  and global runner registration/rotation/revocation require it.
- API-token/service-account slice implemented: system admins can mint/revoke
  platform API tokens, tokens carry kind/roles/optional expiry, API tokens
  hydrate as `api_token` principals, revoked/expired tokens stop
  authenticating, and `runner_admin` tokens can register/rotate/revoke runners
  without full `system_admin`.
- PostgreSQL integration slice implemented behind `NEROCD_TEST_DATABASE_URL` for
  roles, API tokens, runner tokens, claims/leases, JSON run fields, logs, and
  audit persistence. CI now provisions Postgres and runs this slice.
- Runner-admin RBAC and first failure-path audit coverage are implemented for
  denied platform-token and runner-token administration attempts.
- Enterprise OIDC first slice implemented: one fixed discovered provider,
  authorization code with S256 PKCE, verified ID tokens, exact explicitly
  provisioned issuer/subject bindings, durable single-use login transactions,
  existing revocable sessions/RBAC, local recovery sign-in, audit coverage,
  and conditional WebUI sign-in. Multiple issuers, provider logout, automatic
  signup, and claim-driven roles remain outside the slice. LDAP/SAML remain
  candidates based on actual deployment needs.
- RBAC enforcement still needs scoped authorization checks on environment,
  runner, audit, and policy paths.
- Variable groups and encrypted credential values.
- Schedules, environment locks, and policy hooks.
- Additional execution adapters.

Definition of done:

- Each enterprise feature has schema, API contract, audit events, UI workflow,
  tests, and dependency-review notes before it is considered complete.
