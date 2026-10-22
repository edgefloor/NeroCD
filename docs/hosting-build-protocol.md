# Shared build-agent phase protocol

`internal/hostingbuildprotocol` defines nonsecret request and response DTOs for
server and agent wiring. It imports the domain package and standard library
only. The server registers the five nonsecret phase endpoints below. Runner
execution and client integration remain separately reviewed components.

## HTTP boundary

The five registered operations are POST requests under `/api/v1/runners/builds/`:

| Suffix | Request | Fields beyond `authority` |
| --- | --- | --- |
| `status` | `StatusRequest` | None |
| `source-begin` | `SourceBeginRequest` | `expected_revision` |
| `source-commit` | `SourceCommitRequest` | `receipt_id`, `receipt` |
| `prepare` | `PrepareRequest` | `expected_revision`, `daemon_id` |
| `complete` | `CompleteRequest` | `receipt_id`, `expected_revision`, `completion` |


Every `authority` is the existing `domain.HostingBuildAuthority` with unchanged
`build_id`, `run_id`, `lease_id`, `attempt`, and `fence` JSON fields. Runner ID
comes from the authenticated dedicated bearer credential; no request has a
caller-selected runner field. `source-commit` deliberately has no revision.

The shared HTTP decoder enforces body-size limits, unknown-field rejection,
and a single complete JSON document without trailing content. The DTOs and
validation helpers do not implement those decoder checks or authenticate requests.
No plaintext credential response belongs in this package. Credential material's
HTTP boundary needs its own review.

## Completion and receipt hashing

`Completion` preserves the store completion body's exact field order and JSON
shape: `state`, optional `failure_code`, then optional `artifact`. The artifact
uses the existing domain type; no snapshot or policy fields are added.

`SourceReceiptHash` hashes only the domain source receipt. `CompletionHash`
hashes only `Completion`. Both use the same domain JSON hashing function as
persisted receipts. Authority, request envelope, receipt ID, and expected revision
are excluded from the body hash. Changing the wrapper's revision must not change
a committed receipt's body identity.

Persisted receipt phases are `source`, `complete`, and `cleanup`.
`cleanup_required` completions use `cleanup`; other valid completion states use
`complete`. These receipt phases are separate from agent journal terminology,
including any journal kind named `result`.

A `ReceiptAcknowledgment` contains the exact authority, phase, receipt ID, and
body hash. `ValidateAcknowledgment` compares it against caller-retained intent
without normalization. This equality check is not authentication, a signature,
or authorization to execute. The caller must obtain proof through the authenticated runner transport.

## Structural validation limits

Build record IDs match the existing store spelling: one to 63 lowercase ASCII
letters, digits, or hyphens, without a leading hyphen. Their run ID must equal
`build_run_` plus that exact build ID. This is not a generic domain-ID rule.
Lease ID and fence remain nonempty opaque strings compared exactly; this package
does not invent additional spelling or length restrictions. Attempts are positive
and fit the store's signed 32-bit representation.

Receipt IDs retain the store's one-to-128-byte bound, no leading or trailing
whitespace, and no control characters. Interior spaces and other valid existing
spellings are preserved. Revisions are positive int64 values; persistence owns
CAS and arithmetic overflow handling. Daemon fields match existing store bounds.
Body digests use canonical lowercase `sha256:` followed by 64 hexadecimal digits.

Completion validation admits the existing state/failure/artifact combinations.
The store must still verify artifact provenance and exact source, input, daemon,
policy, tree, and target bindings against persisted state. Structural validation
never invents a receipt or supplies provenance for missing evidence.

## Historical proof and current execution

`PhaseResponse` carries persisted build metadata and an optional acknowledgment.
`StatusResponse` separates `receipts` from `may_execute` and `cancel_requested`.
A historical acknowledgment can exist while `may_execute` is false. Only a
reviewed current live authorization path may set `may_execute` true; acknowledgment
validation itself cannot do so.

The separate historical-status store method does not relax current mutation
admission or reuse historical replay as live execution authority.


## Historical status and live execution

`status` requires the authenticated, currently active persisted runner identity
and an exact canonical task, historical lease, attempt and fence. This proves
which attempt is asking. Historical acknowledgments remain readable after creator
or grant revocation and lease expiry. Revoked runner credentials cannot authenticate.
At most one acknowledgment exists for each source, complete and cleanup phase.
Only receipts committed under this exact tuple are returned.

`may_execute` is a separate current decision. It requires a queued, sourcing or
building build, current creator maintainer authority, the exact current resource
and source versions, fixed placement, current destination grant, active enrolled
capable runner, no restore admission hold, and a running task with a live lease.
Database time is checked after blocking locks. Lease renewal is read from the
current lease row, never an initial caller timestamp. Cancellation and
cleanup_required always return `may_execute: false`.

Every fresh mutation repeats live authority checks after its blocking work and
writes. An expired tuple can acknowledge a committed receipt, never create one.
Terminal build completion also settles the exact task and lease atomically.
Agents must not follow it with generic run completion. Cleanup uncertainty keeps
only the existing bounded lease for explicit cleanup reporting; it never grants
another engine execution or automatic lease extension.

The `source-credential` HTTP endpoint is deliberately absent. Its existing domain
request alias and separately reviewed delivery component do not establish a
plaintext transport contract.

## Public build requests and history

`POST /api/v1/resources/{id}/builds` accepts only
`expected_resource_generation`, `expected_source_generation` and
`idempotency_key`. The response is `202 {"build": ...}`. Exact request replay
returns the retained build and its original snapshot, without a second task.
Policy, grant, credential and placement fields are rejected. Only configured
public HTTPS Git resources are admitted.

`GET /api/v1/resources/{id}/builds` returns retained history under current resource
project viewer authority, ordered by created_at and ID descending. It uses the
existing bounded limit/offset pagination and items/count/limit/offset/total
response. `GET /api/v1/hosting-builds/{id}` returns `{"build": ...}` under current
persisted project viewer authority. Cancellation requires current maintainer
authority and `POST /api/v1/hosting-builds/{id}/cancel` with
`{"expected_revision": n}`. Its response uses the same build envelope.

Public builds use a separate whitelist projection. They expose state, revision,
timestamps, original nonsecret Git settings and generations, exact placement,
resolved commit/tree identity and optional output image/platform identities.
They never serialize the runner snapshot, operator/source policy, credential
grant, policy fingerprint, input identity or internal request hashes.
