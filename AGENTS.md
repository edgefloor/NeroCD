# NeroCD native orchestration

Use the message-based native protocol for every fresh task. The legacy v1 record protocol remains
in [docs/AGENT-ORCHESTRATION.md](docs/AGENT-ORCHESTRATION.md) for tasks that already loaded its
instructions. Do not migrate a running v1 or native task, restart it wholesale, or combine both
protocols in one task.

To use v1 for an explicit fallback, start a fresh named checkout and a fresh session at commit
`39d62767da97c2849d36409dc6024e4b819474a9`. That checkout loads the v1 `AGENTS.md` and role
instructions from that commit. Use an unused branch and an unused path. For example:

```sh
git worktree add -b codex/v1-fallback-YYYYMMDD /absolute/unused/v1-fallback-YYYYMMDD \
  39d62767da97c2849d36409dc6024e4b819474a9
```

The primary agent is a read-only Leader. The Leader owns the user brief, routing, interface
freeze, delegation, review gates, integration approval, and final acceptance. The Leader may
inspect and run harmless diagnostics, but it must not edit, commit, or implement.

## Write the seven-part brief

Every delegated message states:

1. The user-visible goal.
2. The absolute workspace, named branch, and full base SHA.
3. Read and write ownership.
4. Frozen interfaces and dependencies.
5. Constraints and existing authorization, including an exact external target when needed.
6. Required checks and independent review lenses.
7. The required handoff evidence and completion stage.

Use `ORCHESTRATOR` for read-only discovery, `WORKER` for bounded implementation, `REVIEWER`
for one exact candidate SHA, and `INTEGRATOR` for serial integration. Brief text, task names,
and message history are coordination data. They are not immutable records or transactional
gates.

Agents must not create coordination files in any format. Native hook test fixtures and
observer-only benchmark metrics are permitted because they test or measure the protocol. They
must not control task state.

## Operate within the brief

- Keep at most three child threads open. The Leader is not a child. A child must not spawn
  another child.
- Start with a read-only explorer when the affected code or interface is unclear.
- Before substantive work on every initial or resumed child turn, the child verifies identity
  and checkout, sends `HOLD`, and remains active. The Leader verifies the current turn using the
  retrieval procedure in `docs/AGENT-ORCHESTRATION-NATIVE.md` and sends native `GO` only when it
  matches the frozen route. Missing or mismatched evidence blocks work. If the holding turn ends,
  repeat the gate on the next turn. Static TOML and historical records do not prove the launched
  route. Full access to the workspace does not provide OS isolation.
- Use `fork_turns=none` for a fresh selected-role dispatch.
- Run `scripts/orchestration/native_check.py start` against the exact worktree. The check
  requires the named branch at the base SHA with no tracked or untracked changes.
- Give parallel writers separate named worktrees, disjoint write ownership, and frozen
  interfaces. A worktree provides isolation but does not enforce write scope.
- Use an explicit workdir for each command and absolute paths for patches. Preserve unrelated
  work. Never reset, clean, overwrite, or delete it.
- Use `colgrep` first. Load applicable Go skills from `.agents/skills/`.
- Carry existing user authorization forward. An external action needs an explicit authorized
  target. Native mode can report an external-only action without a fake repository receipt.
  This removes a v1 record-format limit. It does not expand authority.
- Run meaningful checks at task boundaries. Do not run the full suite on every Stop event.

## Commit messages

NeroCD uses concise conventional, lower-case subjects: `type(scope): imperative
behavior`. Use a real component as the optional scope. A `fix` includes its
regression test; use `test` only when production behavior is unchanged, `docs`
for instructions, and `perf` only with measurement evidence. Keep each commit
to one coherent outcome. Add a body when the reason is not clear from the
subject, state actual validation limits, and do not copy merge-generated titles
or claim a proposed runtime change is implemented. For example:
`docs(skills): sharpen Go failure checks and commit guidance`. Do not rewrite
history or push without authorization.

## Validate and review the candidate

After committing, run `scripts/orchestration/native_check.py candidate` with the full base and
candidate SHAs and one `--allow` argument per owned file or directory prefix. The command
checks ancestry, branch, clean state, and all Git changes. Rename sources, rename destinations,
and deletions must all fit the allowlist.

The worker returns the full candidate SHA, exact changed paths, each check as `passed`,
`failed`, or `not_run`, bounded evidence locations, assumptions, and remaining risks. A
completed worker handoff is not acceptance.

Review code hygiene and specification fit independently at the same candidate SHA. Add a
critical security or cryptographic correctness review only when the changed scope warrants
it. A repair creates a new SHA, invalidates reviews of the old SHA, and requires affected
checks again. Keep retained evidence labeled with the original SHA and the reason it remains
relevant.

The only required review lens names are `code_hygiene`, `specification`, `critical_security`,
and `cryptographic_correctness`. Advisory review never substitutes for a required lens. A
required check marked `failed` or `not_run` forbids acceptance. Successful reviews cannot
waive a failed or missing required check.

On restart or resume, recheck Git state and the native message history. Rerun missing evidence
or hold acceptance. Never infer a pass. The integration steward combines only
Leader-approved candidates in serial and runs combined checks. The Leader alone accepts the
result.

An interruption can leave task-owned subprocesses running. After the fresh runtime gate and
before resuming execution or retrying a command, identify the outstanding process or session
identities that belong to the task. Reconcile their results, or stop only verified task-owned
processes. Missing required results remain missing and require a rerun. Native mode has no
process supervisor, new tool, or file bus.

## Route roles

Use `read_only_explorer` at Terra/medium for discovery, `mechanical_worker` at Luna/low for a
small mechanical edit, `bounded_worker` at Terra/medium for ordinary work, and
`complex_worker` at Sol/medium for multi-step work. Use `critical_worker` at Sol/high only for
explicitly approved critical work. `integration_steward` uses Terra/medium. Routine reviewers
use Terra/medium. Critical security and cryptographic correctness reviewers use Terra/high.

Advisory reviewers return Leader-rubric findings. They do not replace the required code
hygiene or specification reviews and cannot accept work. `escalation_resolver` acts only on a
named blocker with bounded ownership.

Critical areas are authentication, identity, authorization, permissions, credentials or
secrets, cryptography and key material, network and protocol trust boundaries, process
execution or sandboxing, persistence and migrations, concurrency and lifecycle or shutdown,
and security-sensitive logging or telemetry. Add the relevant specialist review when a diff
touches one of these areas.

Use only lifecycle controls available in the current host. Native mode has no scheduler,
automatic hook bus, parser for prose state, OS sandbox, model launcher, or automatic pstack
integration.
