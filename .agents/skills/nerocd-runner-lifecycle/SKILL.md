---
name: nerocd-runner-lifecycle
description: Preserve NeroCD runner lease authority, cancellation, durable journal, replay, and shutdown invariants. Use when changing runner execution, event transport, completion, provenance replay, or journal storage.
---

# NeroCD runner lifecycle

Map the affected path through `internal/runnerruntime/runtime.go`, `internal/runnerruntime/replay.go`, and `internal/runner` before changing it. Preserve these coupled invariants:

- Authority is the full run ID, lease ID, attempt, and fence tuple. Renewals, observations, events, artifacts, provenance, and completion must use the same attempt authority; stale or mismatched authority fails closed.
- Typed deployment claims must match their deployment-plan authority before preparation. Check generic admission, registry dispatch, and runtime dispatch together so a generic adapter cannot bypass the typed path or ignore required approval.
- The attempt supervisor owns cancellation. Lease expiry, a terminal lease observation, non-transient transport failure, or renewal failure cancels work and prevents later mutations from escaping that boundary.
- Output and terminal completion are appended durably before sending. Acknowledgment removes only server-accepted records; idempotency keys remain stable across retries.
- Mask exact process-output bytes before normalization, journaling, or delivery. Cover CR/LF, overlapping secrets, and read boundaries; frame masked text without splitting incomplete UTF-8. Assert both durable records and delivered events in output-path regressions.
- Replay probes current authority before mutation. Stale attempts are discarded, while authorized attempts replay provenance and events before terminal completion. Startup reconciliation finishes before normal heartbeat and claim work.
- Journal storage remains bounded, owner-only, symlink-resistant, single-writer, and crash-consistent. It may retain fenced attempt identity and non-secret provenance, but not bearer credentials, repository credentials, transport headers, or local workspace paths.
- Shutdown waits for owned watcher, renewer, reporter, and process-group cleanup. Avoid goroutines or descendants that outlive the attempt boundary.

Validate the narrow package first, then the relevant replay and lifecycle tests. Use race detection when shared state or cancellation ordering changes. For outage or replay changes, include a test proving append-before-send, restart replay, acknowledgment ordering, stale-fence rejection, and no duplicate terminal mutation as applicable.

Repository anchors: `internal/runnerruntime/runtime_from_command_test.go`, `internal/runnerruntime/replay_test.go`, `internal/runner/executor_test.go`, `internal/runner/journal_test.go`, and the runtime acceptance fixtures. This skill defines domain invariants, not task scope, model choice, agent fanout, or worktree policy.

Use `python3 -B scripts/skills/runner_checks.py CONCERN` from the repository
root to print focused commands. `CONCERN` is `lease`, `replay`, `journal`, or
`shutdown`. Add `--run` to execute them and `--race` when shared state or
cancellation ordering changes, for example:
`python3 -B scripts/skills/runner_checks.py shutdown --race --run`.
The runner checks that every selected group matches tests before execution.
These package tests do not replace runtime acceptance fixtures for transport,
container, or outage behavior.
