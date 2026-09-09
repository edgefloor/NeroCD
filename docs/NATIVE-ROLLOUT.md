# Use native orchestration by default

Use native orchestration for each fresh task. The native guide defines the current-turn gate,
seven-part brief, stateless Git checks, review rules, interruption recovery, and handoff evidence.

1. Create a fresh named worktree from the current main reference.
2. Start a fresh Codex session in that worktree.
3. Give the Leader a frozen seven-part native brief.
4. Wait for every initial or resumed child to send `HOLD` after identity and checkout checks.
5. Have the Leader verify the current-turn route and send native `GO`.
6. Run `native_check.py start` before edits and `native_check.py candidate` after the commit.
7. Run the required checks and independent reviews before Leader acceptance.

Do not migrate an active v1 or native task. Each task keeps its loaded protocol until it reaches
a handoff or blocked state. Workspace access does not provide OS isolation.

## Use the v1 fallback

Use the legacy v1 protocol only when you explicitly need it. Start a fresh named checkout and
fresh session at full commit `39d62767da97c2849d36409dc6024e4b819474a9`. The checkout then loads
the v1 `AGENTS.md` and role instructions from that commit. Choose an unused branch and path:

```sh
git worktree add -b codex/v1-fallback-YYYYMMDD /absolute/unused/v1-fallback-YYYYMMDD \
  39d62767da97c2849d36409dc6024e4b819474a9
```

Do not reset, clean, stash, discard, or overwrite an existing worktree. The fallback changes
the protocol for a new task only. It does not alter existing task evidence or v1 history.

## Read historical benchmark records

[Historical v1 and native benchmark records](ORCHESTRATION-BENCHMARK.md) describe earlier
planning, statuses, and receipts. They do not choose the protocol for current tasks or prove
live lifecycle behavior.
