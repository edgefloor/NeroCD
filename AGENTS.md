# NeroCD Codex orchestration

The primary agent is a read-only Leader. It owns the user brief, turn control, routing,
interface freeze, delegation, review gates, integration approval, and final acceptance. It
may inspect and run harmless diagnostics, but it must not edit, commit, or implement.

## Packet modes

Every delegated turn has one explicit mode:

- `ORCHESTRATOR`: read-only discovery or coordination.
- `WORKER`: bounded implementation in an assigned worktree.
- `REVIEWER`: read-only review of one exact candidate SHA.
- `INTEGRATOR`: serial integration of only Leader-approved candidates.

The versioned packet contract is documented in `docs/AGENT-ORCHESTRATION.md`. A packet must
name the absolute workspace root and exact Git base/branch in the dispatch message, a
user-visible goal, read/write ownership, frozen interfaces, constraints, validation IDs,
handoff evidence, route, selected skill hashes, required review lenses, completion stages,
and any recorded authorization grants. Scope text is a proposed boundary; actual runtime
permissions are separately attested and enforced by the host, not by a prompt or TOML file.

## Operating protocol

- Keep at most three child threads open. The primary Leader is not a child. No child may
  spawn another child.
- Start with the read-only explorer when affected code or interfaces are not understood.
- Before any edit, preflight the exact worktree: absolute Git toplevel, branch, base, clean
  state, runtime model/effort/tools/permissions, and selected skill hashes.
- Parallel writers require separate worktrees, frozen interfaces, and non-overlapping write
  ownership. Shared-host worktrees do not enforce scope. Integration is serial.
- Every command uses the packet's absolute workspace as its explicit workdir. Patches use
  absolute paths. A writer verifies Git toplevel, branch, and base before its first edit.
- Preserve pre-existing and unrelated work. Never reset, clean, overwrite, or delete it.
- Workers use `colgrep` first, load selected skills from `.agents/skills/`, self-verify, and
  return the exact candidate SHA plus bounded evidence locations. They do not ingest or copy
  arbitrary logs that may contain secrets.
- Existing user authorization travels in the packet and is not requested again. A scoped
  external action needs a recorded grant with an explicit target; a missing target is a
  separate blocker. Harmless diagnostics need neither an exact read set nor extra approval.
- Review routine correctness/code hygiene and specification fit independently. Add a risk
  lens only when the changed scope warrants it. Lint/formatting is a mechanical check, not a
  substitute for review. All required reviews pin the same candidate SHA.
- The integration steward combines only Leader-approved candidates and runs combined checks.
  The Leader alone accepts or escalates the result.

## Routing

Use `read_only_explorer` (Terra/medium) for discovery; `mechanical_worker` (Luna/low) for
small mechanical changes; `bounded_worker` (Terra/medium) for ordinary changes;
`complex_worker` (Sol/medium) for multi-step work; and `critical_worker` (Sol/high) only for
explicitly approved critical work. `integration_steward` is Terra/medium. Routine reviewers
are Terra/medium; cryptographic correctness and critical security reviewers are Terra/high.
`escalation_resolver` is Sol/high and acts only on a named blocker and bounded ownership.

Sol maps to `gpt-5.6-sol`, Terra to `gpt-5.6-terra`, and Luna to `gpt-5.6-luna`. Static
configuration provides defaults only; preflight must consume a current runtime capability
attestation and fails when a route, effort, tool, or needed permission is unavailable.

Critical areas are authentication, identity, authorization, permissions, credentials or
secrets, cryptography/key material, network/protocol trust boundaries, process execution or
sandboxing, persistence/migrations, concurrency/lifecycle/shutdown, and security-sensitive
logging or telemetry. Add the relevant specialist review only when the diff touches one.

## Durable handoff and limits

Packet, result, review, and checkpoint records are compact versioned JSON. Git is the source
of truth for changed paths, including rename sources and deletions. Required checks have
`passed`, `failed`, or `not_run`; only `passed` advances. An accepting review never overrides
a failed or missing check. External checkpoint state is single-writer and manual.

This first release is validation and handoff plumbing, not an autonomous controller. It has
no scheduler, CAS, Delta executor, OS sandbox, or model launcher. It verifies record shape,
Git facts, and cross-record consistency; attestations remain claims by the operator/runtime.
Use only the stop, interrupt, and archive controls actually available in the active host;
never promise stronger lifecycle guarantees.
