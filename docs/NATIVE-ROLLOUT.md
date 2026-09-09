# Roll out native orchestration

Use this how-to to run the opt-in native experiment while v1 remains available. The native
protocol is defined in [Use the native orchestration experiment](AGENT-ORCHESTRATION-NATIVE.md).
That guide defines the current-turn gate, seven-part brief, stateless Git checks, review rules,
and handoff evidence.

## Start a native trial

1. Finish any running v1 task with its v1 records, results, reviews, and checkpoints.
2. Create a fresh named worktree from `codex/native-orchestration-v2`.
3. Start a fresh Codex session in that worktree.
4. Give the Leader a frozen seven-part native brief.
5. Before every initial or resumed child turn, wait for the child to send `HOLD` after identity
   and checkout verification.
6. Have the Leader verify the current-turn runtime evidence and send native `GO` as the native
   guide requires.
7. Require `native_check.py start` before edits and `native_check.py candidate` after the
   candidate commit.
8. Require the checks and independent reviews named in the brief before Leader acceptance.

Do not use native instructions to migrate a running v1 task. Do not treat workspace access as
OS isolation.

## Expand the trial

Start with reversible local worktrees and bounded tasks. Keep the v1 branch, v1 documents,
tests, and source workflow available throughout the trial. Add task types only after the Leader
has receipts for the required checks, candidate reviews, and failure scenarios. A failed check,
a `not_run` required check, missing current-turn evidence, or a stale review SHA stops
acceptance for that candidate.

Use the benchmark plan in [Compare v1 and native orchestration](ORCHESTRATION-BENCHMARK.md) to
compare the two protocols. The observer report records three pairs and a strict multi-audit, but
it does not show causation. Failure-scenario results are `PENDING` until verified receipts exist.

## Roll back a trial

To stop a native trial, stop dispatching new native work and begin new work with the v1 protocol.
Let active native work reach a documented handoff or blocked state. Preserve its messages,
checks, candidate SHA, reviews, and evidence so a later session can reconstruct the decision.

Do not reset, clean, discard, or overwrite a worktree to roll back. A rollback changes the
protocol selected for future work. It does not erase the trial's evidence or modify v1 history.
