# Use the native orchestration experiment

This how-to applies only to `codex/native-orchestration-v2`. Main continues to use the v1
record protocol in `docs/AGENT-ORCHESTRATION.md`. Native mode replaces v1 JSON construction
records with short dispatch and handoff messages. It does not replace Git checks, independent
reviews, or Leader acceptance.

## Start a fresh native session

1. Create a fresh checkout from `codex/native-orchestration-v2`.
2. Start a new Codex session in that checkout.
3. Tell the Leader to follow `AGENTS.md` native mode.

Do not switch a running v1 task to this branch. Existing v1 tasks keep their packets, results,
reviews, and checkpoints. Native instructions do not change pstack or install hooks.

## Dispatch work

Send a seven-part brief in one message:

```text
Mode: WORKER
Goal: Reject invalid POST and PATCH /api/v1/templates requests with HTTP 400 before persistence.
Workspace: /absolute/path/to/worktree
Branch: codex/example-worker
Base: 0123456789abcdef0123456789abcdef01234567
Ownership: read internal/app/ and internal/api/; write internal/app/service.go and internal/api/server_test.go.
Interfaces and dependencies: Preserve the template response envelope and persistence contract.
Constraints and authorization: Preserve unrelated work. No external actions are authorized.
Checks and reviews: required go test ./internal/app ./internal/api; code_hygiene; specification.
Handoff: Full candidate SHA, changed paths, check status and evidence, assumptions, and risks.
```

The brief carries dynamic ownership. A task name is a label, not an immutable digest. Observe
the launched model and effort after dispatch. If the host does not expose actual metadata,
record that gap in the message history.

## Check a writer at start

Run the explicit CLI from the assigned worktree:

```sh
python3 -B scripts/orchestration/native_check.py \
  --repo-root /absolute/path/to/worktree \
  --base 0123456789abcdef0123456789abcdef01234567 \
  --branch codex/example-worker \
  start
```

`--repo-root` must be the canonical absolute Git toplevel. `--base` must be a full lowercase
commit SHA. Writers use an explicit named branch. `start` requires `HEAD` to equal the base and
rejects tracked or untracked changes.

The CLI is an ordinary stateless command. It does not run automatically, parse prompts, write
shared state, or execute check commands supplied as strings.

## Check a candidate

Commit the change, then run:

```sh
python3 -B scripts/orchestration/native_check.py \
  --repo-root /absolute/path/to/worktree \
  --base 0123456789abcdef0123456789abcdef01234567 \
  --branch codex/example-worker \
  candidate \
  --candidate fedcba9876543210fedcba9876543210fedcba98 \
  --allow internal/app/service.go \
  --allow internal/api/server_test.go
```

Repeat `--allow` for each owned file. A value that ends in `/` owns a directory prefix. The
command requires the candidate at `HEAD`, the base as its ancestor, and a clean worktree. It
reads Git's changed-path data and checks additions, modifications, deletions, and both sides of
a rename. It rejects absolute paths, traversal, backslashes, noncanonical paths, and Git
metadata aliases.

The command requires the trusted Git executable at `/usr/bin/git`, which is present on macOS
and Ubuntu GitHub Actions runners. It fails if that path is unavailable or is not executable.
The command does not fall back to `PATH` or accept a configurable Git path.

Each Git invocation has a ten-second timeout. The command rejects Git output above one MiB
after capture. It removes inherited `GIT_*` redirection, disables optional locks and fsmonitor,
ignores replacement objects, and disables external diff and text conversion during path
inspection. Base, candidate, tree, and ancestry checks therefore use raw commit identities
even when the repository has `refs/replace` entries. The command does not modify or delete
those refs. It does not claim to enforce the worktree boundary outside this explicit check.

## Hand off and accept

Return a compact message:

```text
Candidate: fedcba9876543210fedcba9876543210fedcba98
Changed paths: internal/app/service.go; internal/api/server_test.go
Checks: app_api_unit passed, evidence at focused test output; optional full_integration not_run.
Assumptions: The existing template response envelope is frozen.
Risks: The optional full integration suite was not requested.
```

Reviewers pin the exact SHA. A repair invalidates reviews of the prior SHA. On resume, compare
`HEAD`, the base, the branch, and message history before using earlier evidence. If evidence is
missing, rerun it or stop acceptance.

An authorized external-only action can appear directly in a native handoff with its exact
target and observed verification. This differs from v1, whose result schema needs a repository
change to represent a write. The native rule removes that representation limit. It grants no
new action or target.

The integration steward integrates only candidates the Leader approved. Native completion,
successful checks, and successful reviews remain separate facts. The Leader alone accepts the
combined result.

A required check marked `failed` or `not_run` forbids acceptance. A successful review cannot
waive it. Required review lenses use only `code_hygiene`, `specification`,
`critical_security`, and `cryptographic_correctness`. Advisory findings do not replace them.
