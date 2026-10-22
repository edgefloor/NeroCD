# Nonsecret build-agent HTTP client

`runnerclient.Client` exposes five typed methods for the accepted
`hostingbuildprotocol` messages. These methods reuse the authenticated client
transport and forward the caller's context. They do not install a server endpoint,
register a capability, claim work, fetch source or invoke a build engine.

| Method | Request / response | POST path |
| --- | --- | --- |
| `BuildStatus` | `StatusRequest` / `StatusResponse` | `/api/v1/runners/builds/status` |
| `BuildSourceBegin` | `SourceBeginRequest` / `PhaseResponse` | `/api/v1/runners/builds/source-begin` |
| `BuildSourceCommit` | `SourceCommitRequest` / `PhaseResponse` | `/api/v1/runners/builds/source-commit` |
| `BuildPrepare` | `PrepareRequest` / `PhaseResponse` | `/api/v1/runners/builds/prepare` |
| `BuildComplete` | `CompleteRequest` / `PhaseResponse` | `/api/v1/runners/builds/complete` |

Every request passes the protocol's structural validator before HTTP. Transport
uses the existing runner bearer token and JSON request body. Responses are limited
to 1 MiB, must contain exactly one JSON document, and reject unknown fields. The
client validates the complete document before publishing its typed result. Error
messages contain no response body; the build methods also remove the shared
transport's retained HTTP diagnostic body from returned `APIError` values.

## Identity and proof

Every response must match the requested build and canonical run, have a positive
revision, and name a known build state. The client permits advanced persisted
state and revision when replaying historical proof. Full snapshot, toolchain,
source and artifact semantics remain the runtime/controller owner's responsibility.

Source and completion methods require an acknowledgment matching the exact
build/run/lease/attempt/fence tuple, receipt ID, phase and body hash. Hashes use the
protocol helpers and cover only the retained receipt or completion body. Expected
revision and request envelopes never enter that hash. `cleanup_required` uses the
distinct `cleanup` receipt phase. Begin and prepare reject unsolicited receipt
acknowledgments; they do not fabricate proof.

Status validates each receipt's exact requested tuple and finite phase, receipt-ID
and digest syntax. Duplicate receipt phases are rejected. Status requests contain
no retained body intent, so returned hashes are proof metadata: the caller must
compare them with its retained body before acknowledging a journal record. A
receipt never restores lease authority or implies execution permission.

The client preserves an explicit wire `MayExecute` value only when it is
structurally consistent: true requires queued, sourcing or building state and no
cancel request. It never derives true from receipts. A future reviewed server
path must establish live authority; this transport does not establish that path.

The methods perform no automatic retries and never rewrite a request or completion
revision. After an uncertain response, callers must retain exact intent and use
the reviewed replay/status contract rather than inventing a new revision or body.

## Startup capabilities

`RuntimeCapabilitiesIdentity(ctx, proxyConfigured, buildConfigured)` compares
persisted exact `proxy_apply` and `hosting_build_v1` names with explicit local
configuration. Both checks must agree. Other opaque names, case variants and
whitespace variants do not grant these capabilities. Duplicate names do not add
authority. Legacy responses without capabilities remain valid only when both
features are disabled.

`RuntimeIdentity(ctx, proxyConfigured)` remains a wrapper with build disabled, as
does the proxy-only startup path. An older runner therefore fails closed if it is
enrolled with build capability but lacks explicit build configuration. Legacy
`Identity` behavior remains unchanged. None of these methods advertises a default
capability or changes heartbeat/registration behavior.

## Limits and remaining integration

This slice carries only nonsecret messages and is ready for public-source wiring.
Plaintext private-source credential transport remains pending. Server handlers,
credential delivery, CLI configuration, agent journal/executor, runtime permission
checks and source/engine wiring remain separate work. There is no production
endpoint or end-to-end deployment acceptance claim.

Loopback HTTP tests cover authentication, exact messages, structural admission,
response bounds and strict decoding, mismatched acknowledgments and status,
cleanup proof after state advancement, cancellation, and all persisted/local
proxy-build capability combinations. The full runnerclient race suite is the
validation boundary for this component.
