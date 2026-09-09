# Use the native orchestration experiment

This how-to applies only to the reviewed native-adoption reference supplied in the Leader's
handoff. Main continues to use the v1 record protocol in `docs/AGENT-ORCHESTRATION.md`. Native
mode replaces v1 JSON construction records with short dispatch and handoff messages. It does not
replace Git checks, independent reviews, or Leader acceptance.

## Start a fresh native session

1. Create a fresh checkout from the reviewed native-adoption reference in the Leader's handoff.
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

The brief carries dynamic ownership. A task name is a label, not an immutable digest.

## Gate every current turn before work

Before substantive work on an initial or resumed child turn, the child performs only identity
and checkout verification, sends `HOLD`, and remains active. The Leader then obtains the child
ID from the spawn result. If that result is unavailable, the Leader finds child IDs in the
current SQLite `thread_spawn_edges` rows for the exact parent. The Leader joins each child ID to
`threads`, then uses that row's task path and time fields to select the current child and its
`rollout_path`.

For fresh child discovery, export the values from the exact original dispatch. The dispatch
window applies only to this discovery query. The query accepts one child only. A zero or
multiple-row result blocks the gate.

```sh
export PARENT_ID='parent thread ID'
export TASK_PATH='child task path'
export DISPATCH_START_MS='original dispatch start in milliseconds'
export DISPATCH_END_MS='original dispatch end in milliseconds'
```

```sh
python3 -B - <<'PY'
import os
import sqlite3

parent_id = os.environ["PARENT_ID"]
task_path = os.environ["TASK_PATH"]
start_ms = os.environ["DISPATCH_START_MS"]
end_ms = os.environ["DISPATCH_END_MS"]
database = "/Users/bubble/.codex/state_5.sqlite"
query = """
SELECT child.id, child.rollout_path
FROM thread_spawn_edges AS edge
JOIN threads AS child ON child.id = edge.child_thread_id
WHERE edge.parent_thread_id = ?
  AND child.agent_path = ?
  AND child.created_at_ms BETWEEN ? AND ?
ORDER BY child.created_at_ms, child.id
"""
with sqlite3.connect(f"file:{database}?mode=ro", uri=True) as connection:
    rows = connection.execute(query, (parent_id, task_path, start_ms, end_ms)).fetchall()
if len(rows) != 1:
    raise SystemExit(f"expected one scoped child, found {len(rows)}")
child_id, rollout_path = rows[0]
print(f"child_id={child_id}")
print(f"rollout_path={rollout_path}")
PY
```

For a reused or resumed child with a known ID, do not filter by its creation time. Export the
same parent and task path, then export the known child ID.

```sh
export PARENT_ID='parent thread ID'
export TASK_PATH='child task path'
export CHILD_ID='known child thread ID'
```

```sh
python3 -B - <<'PY'
import os
import sqlite3

database = "/Users/bubble/.codex/state_5.sqlite"
query = """
SELECT child.id, child.rollout_path
FROM thread_spawn_edges AS edge
JOIN threads AS child ON child.id = edge.child_thread_id
WHERE edge.parent_thread_id = ?
  AND child.agent_path = ?
  AND child.id = ?
"""
with sqlite3.connect(f"file:{database}?mode=ro", uri=True) as connection:
    rows = connection.execute(
        query,
        (os.environ["PARENT_ID"], os.environ["TASK_PATH"], os.environ["CHILD_ID"]),
    ).fetchall()
if len(rows) != 1:
    raise SystemExit(f"expected one known child, found {len(rows)}")
child_id, rollout_path = rows[0]
print(f"child_id={child_id}")
print(f"rollout_path={rollout_path}")
PY
```

Next, call the supported `mcp__codex_app__read_thread` tool with `child_id`. Confirm that it
returns that child and its active current turn for this dispatch. If the child or turn differs,
hold the task. Do not replace this call with a repository CLI or a historical transcript.

Export the resolved path, the verified child ID, and the current turn ID from `read_thread`. Use
the path and turn ID to read only route facts. This tested example prints no conversation
content.

```sh
export ROLLOUT_PATH='current threads.rollout_path'
export CHILD_ID='verified child thread ID'
export TURN_ID='verified active current turn ID'
```

```sh
python3 -B - <<'PY'
import json
import os

rollout_path = os.environ["ROLLOUT_PATH"]
child_id = os.environ["CHILD_ID"]
turn_id = os.environ["TURN_ID"]
session_meta = None
turn_context = None
with open(rollout_path) as rollout:
    for line in rollout:
        record = json.loads(line)
        if record["type"] == "session_meta":
            session_meta = record["payload"]
        if record["type"] == "turn_context" and record["payload"].get("turn_id") == turn_id:
            turn_context = record["payload"]
if session_meta is None or turn_context is None:
    raise SystemExit("current session metadata or turn context is unavailable")
if session_meta.get("id") != child_id:
    raise SystemExit("session metadata does not match the child ID")
for label, value in (
    ("role", session_meta.get("agent_role")),
    ("model", turn_context.get("model")),
    ("effort", turn_context.get("effort")),
    ("sandbox", turn_context.get("sandbox_policy")),
    ("approval", turn_context.get("approval_policy")),
):
    if value is None:
        raise SystemExit(f"missing {label}")
    print(f"{label}={value}")
PY
```

The loop retains the latest top-level `turn_context` for the verified turn ID. Compare those
values with the frozen route, then send native `GO` only when they match. On this host,
`/Users/bubble/.codex/state_5.sqlite` and its `threads` and `thread_spawn_edges` tables are
observed internal support, not a stable public API. If the database is missing or its schema has
changed, inspect it read-only and hold the task until the retrieval procedure is verified again.

The same current `threads` row supplies `rollout_path` for an archived child. That path can be
under `/Users/bubble/.codex/archived_sessions`. Do not use an archived path or inactive archived
turn as active authorization. If `read_thread` cannot verify an active current turn, hold the
task. Do not use a separate archive table or API.

Do not substitute static TOML, an earlier turn, or historical SQLite data for this observation.

The Leader sends native `GO` only after the observed route matches the frozen brief. Missing or
mismatched evidence blocks work. If the holding turn ends, the next turn needs a new observation
before `GO`. Full filesystem access is not OS isolation.

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

For rollout and rollback steps, see [Native orchestration rollout](NATIVE-ROLLOUT.md). Keep a
running v1 task on v1. Start native mode only in a fresh session from the native branch.
