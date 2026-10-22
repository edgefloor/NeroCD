# Shared build-agent phase protocol

`internal/hostingbuildprotocol` defines nonsecret request and response DTOs for
future server and agent wiring. It imports the domain package and standard
library only. This candidate registers no endpoint, controller, executor, client,
or runner capability.

## Proposed HTTP boundary

All six operations are POST requests under `/api/v1/runners/builds/`:

| Suffix | Request | Fields beyond `authority` |
| --- | --- | --- |
| `status` | `StatusRequest` | None |
| `source-begin` | `SourceBeginRequest` | `expected_revision` |
| `source-commit` | `SourceCommitRequest` | `receipt_id`, `receipt` |
| `prepare` | `PrepareRequest` | `expected_revision`, `daemon_id` |
| `complete` | `CompleteRequest` | `receipt_id`, `expected_revision`, `completion` |
| `source-credential` | Existing domain `HostingSourceCredentialRequest` | `delivery_id` |

Every `authority` is the existing `domain.HostingBuildAuthority` with unchanged
`build_id`, `run_id`, `lease_id`, `attempt`, and `fence` JSON fields. Runner ID
comes from the authenticated dedicated bearer credential; no request has a
caller-selected runner field. `source-commit` deliberately has no revision.

Future HTTP decoders must enforce body-size limits, unknown-field rejection,
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
or authorization to execute. The caller must obtain proof through the future
reviewed authenticated transport.

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

The exact historical-status read and revocation policy remains deferred. It
requires a newly reviewed store method and must not relax current mutation
admission or reuse historical replay as live execution authority.
