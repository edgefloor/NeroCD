---
name: nerocd-runner-lifecycle
description: Preserve NeroCD runner lease authority, cancellation, durable journal, replay, and shutdown invariants. Use when changing runner execution, event transport, completion, provenance replay, or journal storage.
---

# NeroCD runner lifecycle

Map the affected path through `cmd/nerocd/runner.go`, `cmd/nerocd/runner_replay.go`, and `internal/runner` before changing it. Preserve these coupled invariants:

- Authority is the full run ID, lease ID, attempt, and fence tuple. Renewals, observations, events, artifacts, provenance, and completion must use the same attempt authority; stale or mismatched authority fails closed.
- The attempt supervisor owns cancellation. Lease expiry, a terminal lease observation, non-transient transport failure, or renewal failure cancels work and prevents later mutations from escaping that boundary.
- Output and terminal completion are appended durably before sending. Acknowledgment removes only server-accepted records; idempotency keys remain stable across retries.
- Replay probes current authority before mutation. Stale attempts are discarded, while authorized attempts replay provenance and events before terminal completion. Startup reconciliation finishes before normal heartbeat and claim work.
- Journal storage remains bounded, owner-only, symlink-resistant, single-writer, and crash-consistent. It may retain fenced attempt identity and non-secret provenance, but not bearer credentials, repository credentials, transport headers, or local workspace paths.
- Shutdown waits for owned watcher, renewer, reporter, and process-group cleanup. Avoid goroutines or descendants that outlive the attempt boundary.

Validate the narrow package first, then the relevant replay and lifecycle tests. Use race detection when shared state or cancellation ordering changes. For outage or replay changes, include a test proving append-before-send, restart replay, acknowledgment ordering, stale-fence rejection, and no duplicate terminal mutation as applicable.

Repository anchors: `cmd/nerocd/main_test.go`, `cmd/nerocd/runner_replay_test.go`, `internal/runner/executor_test.go`, `internal/runner/journal_test.go`, and the runtime acceptance fixtures. This skill defines domain invariants, not task scope, model choice, agent fanout, or worktree policy.
