# Agent orchestration protocol v1

NeroCD's first-release protocol is a preflight and durable handoff validator. A human or
Leader still dispatches agents, invokes reviews, integrates serially, and decides whether to
stop or archive a task. The scripts never execute commands stored in records.

## Trust boundary

The validator checks JSON shape, configured routes, explicit runtime attestations, selected
skill hashes, Git base/branch/cleanliness, actual changed paths, review/check pinning, and
checkpoint lineage. A capability or check record is an operator/runtime attestation: parsing
it does not prove OS isolation, tool execution, model availability, or the truth of a
recorded check status. Structural lens gates do not authenticate a packet or review producer.
Codex TOML is a default, not a permission attestation.
The selected-skills helper is an operator-chosen trusted local program, not sandboxed input;
it has a 10-second timeout, but its captured output is size-checked only after process exit.

Live state and record files belong outside the product checkout (for example under a
dedicated temporary run directory); checked-in files under `orchestration/examples/` are
templates only. `.gitignore` and `.dockerignore` exclude `.delta/` only from Git status and
Docker build contexts. They do not exclude it from synthetic release archives: release
evidence/archive generation remains prohibited while `.delta/` exists until the release
archive script is patched separately. There is
no Delta adapter or executor in this release. Checkpoints are atomic single-writer files,
not a multi-controller CAS or ledger.

## Records

All records are JSON objects with `record_type` and integer `schema_version: 1`. Files over
1 MiB are rejected. Unknown fields are rejected so typos do not silently weaken a packet.

### Packet

The checked-in templates are `orchestration/examples/worker-packet.json`,
`orchestration/examples/runtime-capabilities.json`,
`orchestration/examples/result-template.json`, and
`orchestration/examples/review-template.json`. They are editable starting points; replace their
placeholder values, including SHAs. Packet fields are:

- `task_id`, `mode`, and `goal` identify the delegation.
- `repository` pins an absolute `workspace_root`, full lowercase `base_sha`, and expected
  `branch`. It is a required non-empty string for `WORKER` and `INTEGRATOR`.
  `ORCHESTRATOR` and `REVIEWER` may instead use JSON `null`, which requires an actually
  detached HEAD in both preflight and live result or checkpoint validation. The CLI's
  `--repo-root` must identify that exact Git toplevel.
- `ownership.read_paths` is advisory discovery scope. `write_paths` is enforced against Git
  changes using repository-relative exact files or directory prefixes ending in `/`.
- `interfaces`, `constraints`, `validation`, and `handoff` carry the frozen seven-part brief.
  `validation` values are stable check IDs, not shell commands.
- `route` pins role/model/effort and required tool names.
- `selected_skills` maps canonical `.agents/skills/` IDs to raw lowercase SHA-256 hashes
  emitted by `scripts/skills/validate.py --json`. It may be empty when no skill applies.
- `authorization.grants` preserves already-issued grants. Each has `action`, explicit
  `target`, and `granted_by`. Absence of a target is a blocker, not a request to infer one.
- `required_review_lenses` names independent reviews. The only supported values are
  `code_hygiene`, `specification`, `critical_security`, and `cryptographic_correctness`.
  `WORKER` and `INTEGRATOR` packets require both `code_hygiene` and `specification`; either
  critical lens may be added when the changed scope needs it. Read-only packets may require
  no reviews. The validator rejects unknown and advisory lens names in every mode.
- `required_completion_stages` selects from `source`, `artifact`, `published`, and
  `deployed`. Only relevant stages are required; publication and deployment are not assumed.

Modes are `ORCHESTRATOR`, `WORKER`, `REVIEWER`, and `INTEGRATOR`. Role/mode pairs are fixed:
explorer/orchestrator, implementation roles/worker, review roles/reviewer, and integration
steward/integrator.

The advisory reviewer routes, `advisory_reviewer_sol` and `advisory_reviewer_terra`, are
read-only `REVIEWER` routes. For each task, the Leader supplies the complete upstream reviewer
prompt, rubric, and code-quality lens and assesses the resulting advisory findings. An advisory
report is an external report, not a protocol review record: it cannot satisfy a required
`code_hygiene` or `specification` lens, create an optional lens, or authorize automatic changes.
When a change is made, the standard required reviews remain mandatory.

Existing records are preserved as historical evidence. Current v1 validation rejects an older
packet or checkpoint that omits a routine lens or names an unsupported lens. Create a new valid
packet and new reviews before attempting new acceptance.

### Runtime capability attestation

`orchestration/examples/runtime-capabilities.json` records the runtime-observed model to
effort map, tool names, and read/write permissions. `attested: true` and a non-empty `source`
identify the issuer. This is explicit input to preflight, never inferred from static config.

### Result

A result pins `task_id`, `mode`, the packet's canonical `packet_digest`, `base_sha`, and exact
`candidate_sha`. `outcome` is `changed`, `no_change`, `blocked`, or `failed`. `changed_paths`
must exactly match Git; rename source and destination both count. `no_change` requires the
candidate to equal the base and no diff.

`checks` contains `{id,status,summary,location}` items. Status is `passed`, `failed`, or
`not_run`; all packet check IDs must be present. `evidence` contains bounded
`{summary,location}` items rather than raw logs. `completion` contains exactly the four
stages, each set to `complete`, `pending`, `failed`, or `not_applicable`.

Failed, blocked, and `not_run` results remain structurally valid durable records. They cannot
advance a checkpoint. This distinction permits recovery and diagnosis without calling a
failed run acceptable.

#### Construct a result from observed state

Start from the canonical template after the candidate is committed and the packet workspace
is clean. The following recipe loads rather than retypes the schema, derives identity and Git
facts, creates one `not_run` entry for every packet check, and writes a new external result
without overwriting existing evidence. Set the three path values explicitly and run it with
the tool's working directory set to `WORKSPACE`. Invoke the displayed block with
`python3 -B -` and provide it on standard input; running from the workspace makes the local
`orchestration` package importable, while `-B` avoids bytecode artifacts.

```python
import json
import subprocess
from pathlib import Path

from orchestration.protocol import (
    changed_paths,
    digest_record,
    load_json,
    validate_packet,
    validate_result,
)

WORKSPACE = Path("/absolute/path/to/worker-worktree").resolve()
PACKET_PATH = Path("/absolute/path/to/run/packet.json").resolve()
RESULT_PATH = Path("/absolute/path/to/run/new-result.json").resolve()

packet = load_json(PACKET_PATH)
validate_packet(packet)
if Path(packet["repository"]["workspace_root"]).resolve() != WORKSPACE:
    raise ValueError("WORKSPACE does not match packet.repository.workspace_root")
if RESULT_PATH == WORKSPACE or WORKSPACE in RESULT_PATH.parents:
    raise ValueError("RESULT_PATH must be outside the product workspace")

git_toplevel = Path(subprocess.run(
    ["git", "rev-parse", "--show-toplevel"],
    cwd=WORKSPACE,
    check=True,
    capture_output=True,
    text=True,
).stdout.strip()).resolve()
if git_toplevel != WORKSPACE:
    raise ValueError("WORKSPACE must be the canonical Git toplevel")

candidate = subprocess.run(
    ["git", "rev-parse", "HEAD"],
    cwd=WORKSPACE,
    check=True,
    capture_output=True,
    text=True,
).stdout.strip()
base = packet["repository"]["base_sha"]

result = load_json(WORKSPACE / "orchestration/examples/result-template.json")
result.update({
    "task_id": packet["task_id"],
    "mode": packet["mode"],
    "packet_digest": digest_record(packet),
    "base_sha": base,
    "candidate_sha": candidate,
    "changed_paths": changed_paths(WORKSPACE, base, candidate),
    "checks": [
        {
            "id": check_id,
            "status": "not_run",
            "summary": "Not run; replace with the observed result before handoff.",
            "location": "No evidence recorded yet.",
        }
        for check_id in packet["validation"]
    ],
    "evidence": [],
    "completion": {
        stage: (
            "pending"
            if stage in packet["required_completion_stages"]
            else "not_applicable"
        )
        for stage in ("source", "artifact", "published", "deployed")
    },
})

validate_result(result, packet, WORKSPACE)
RESULT_PATH.parent.mkdir(parents=True, exist_ok=True)
with RESULT_PATH.open("x", encoding="utf-8") as handle:
    json.dump(result, handle, indent=2, sort_keys=True)
    handle.write("\n")
```

`changed_paths` includes both rename sources and destinations. The draft deliberately
remains `blocked`, with required completion stages `pending`; it is structurally valid but
cannot advance a checkpoint. The recipe does not execute commands from the packet.

After each delegated check, record `passed` only for observed success, `failed` for an
observed failure, and leave an unexecuted check as `not_run`; update its bounded summary and
evidence location at the same time. Set `outcome` to `changed` or `no_change` only when
the work is ready for advancement, and mark a required completion stage `complete` only
when it has actually completed. Then validate the result in a separate invocation whose
tool working directory is the packet workspace:

```sh
python3 -B scripts/orchestration/validate.py \
  --repo-root /absolute/path/to/worker-worktree \
  result /absolute/path/to/run/new-result.json \
  --packet /absolute/path/to/run/packet.json
```

Inspect that command's exit status before handoff. A structurally valid blocked draft still
cannot pass the checkpoint advancement gates.

### Review and checkpoint

A review pins `task_id`, `packet_digest`, exact `candidate_sha`, `lens`, `status`, and bounded
findings. Every required lens must pass on the same candidate. A stale review always fails.

A checkpoint records stage (`candidate_validated`, `reviewed`, or `accepted`), packet and
candidate identity, and absolute external record locations plus content digests. Advancing
validates all earlier gates before atomically replacing the state file. On any combined
failure, the old checkpoint is unchanged. A different packet or candidate requires a new
checkpoint or an explicit future repair mechanism; it is never silently substituted.
`validate.py checkpoint` replays the stage-appropriate result, review, and completion gates
and detects record edits after acceptance. With intact external records, `checkpoint.py` can
reconstruct an absent state file.

Record content digests, including `packet_digest`, are canonical digests of parsed JSON emitted
by `validate.py digest`; they are not checksums of record-file bytes. Whitespace-equivalent JSON
therefore has the same digest while its raw file checksum may differ. Copy a packet's emitted
`sha256:...` value verbatim into the result, every review, and the checkpoint records.

## Root-relative commands

Run these from the repository root. For one packet, `preflight.py` defaults `--repo-root` to
the current directory; multiple packets require one ordered `--repo-root` per packet.

```sh
python3 -B scripts/orchestration/validate.py --repo-root . config
python3 -B scripts/orchestration/validate.py packet /run/records/packet.json
python3 -B scripts/orchestration/validate.py digest /run/records/packet.json
python3 -B scripts/orchestration/preflight.py \
  --capabilities /run/records/runtime-capabilities.json \
  --packet /run/records/packet.json \
  --repo-root /absolute/path/to/worker-worktree \
  --skills-validator scripts/skills/validate.py
python3 -B scripts/orchestration/validate.py --repo-root /absolute/path/to/worker-worktree \
  result /run/records/result.json --packet /run/records/packet.json
python3 -B scripts/orchestration/validate.py review /run/records/spec-review.json \
  --packet /run/records/packet.json --candidate 0123456789abcdef0123456789abcdef01234567
python3 -B scripts/orchestration/checkpoint.py \
  --repo-root /absolute/path/to/worker-worktree \
  --state /run/state/checkpoint.json --stage accepted \
  --packet /run/records/packet.json --result /run/records/result.json \
  --review /run/records/hygiene-review.json --review /run/records/spec-review.json
python3 -B scripts/orchestration/validate.py --repo-root /absolute/path/to/worker-worktree \
  checkpoint /run/state/checkpoint.json
python3 -B scripts/orchestration/validate.py checkpoint /run/state/checkpoint.json \
  --offline-recovery
```

For a handoff, substitute the six quoted values with the receiving worker's exact workspace,
external packet, result, and review paths, candidate SHA, and recorded canonical packet digest.
Run the snippet with the tool's working directory set to `HANDOFF_WORKSPACE`; it pins every
command to that workspace instead of inferring a repository root. The fail-fast subshell verifies
the captured digest, candidate, and records that pin it.

```sh
(
  set -eu
  HANDOFF_WORKSPACE='/absolute/path/to/worker-worktree'
  HANDOFF_PACKET='/absolute/path/to/run/packet.json'
  HANDOFF_RESULT='/absolute/path/to/run/result.json'
  HANDOFF_REVIEW='/absolute/path/to/run/review.json'
  HANDOFF_CANDIDATE='0123456789abcdef0123456789abcdef01234567'
  HANDOFF_EXPECTED_PACKET_DIGEST='sha256:replace-with-the-recorded-canonical-value'
  HANDOFF_ACTUAL_PACKET_DIGEST="$(python3 -B "$HANDOFF_WORKSPACE/scripts/orchestration/validate.py" digest "$HANDOFF_PACKET")"
  test "$HANDOFF_ACTUAL_PACKET_DIGEST" = "$HANDOFF_EXPECTED_PACKET_DIGEST"
  test "$(git -C "$HANDOFF_WORKSPACE" rev-parse HEAD)" = "$HANDOFF_CANDIDATE"
  python3 -B "$HANDOFF_WORKSPACE/scripts/orchestration/validate.py" --repo-root "$HANDOFF_WORKSPACE" \
    result "$HANDOFF_RESULT" --packet "$HANDOFF_PACKET"
  python3 -B "$HANDOFF_WORKSPACE/scripts/orchestration/validate.py" \
    review "$HANDOFF_REVIEW" --packet "$HANDOFF_PACKET" --candidate "$HANDOFF_CANDIDATE"
)
```

For `validate.py`, `--json` and `--repo-root` are global options and appear before the
subcommand, for example `python3 -B scripts/orchestration/validate.py --json digest
/run/records/packet.json`. `--offline-recovery` belongs only to the `checkpoint` subcommand.
`preflight.py` and `checkpoint.py` have their own ordinary options, including `--json`.

Checkpoint verification normally requires the packet's exact original Git toplevel and
rechecks its branch, candidate, cleanliness, and changed paths. If that workspace has been
retired, `checkpoint ... --offline-recovery` checks record digests and replays result,
required-check, review-quorum, and completion semantics without a checkout. Its output says
that current Git facts were not verified; offline recovery is not live candidate acceptance.

The skills validator is required only when `selected_skills` is non-empty. Its hash algorithm
and canonical tree are owned and documented by the skills subsystem; orchestration compares
the returned hashes and does not duplicate that algorithm.

Use the validator's JSON output when constructing `selected_skills`. Values are canonical
whole-package `sha256-path-content-v1` hashes, not raw `SKILL.md` checksums. The `--json`
command accepts no skill IDs or `id:hash` positional arguments; it emits the complete map.
From the repository root, copy the complete map or select entries in a packet-building
script:

```sh
python3 -B scripts/skills/validate.py --json
```

```python
import json
import subprocess

payload = subprocess.run(
    ["python3", "-B", "scripts/skills/validate.py", "--json"],
    check=True,
    capture_output=True,
    text=True,
).stdout
all_skills = json.loads(payload)["skills"]
selected_skills = {
    skill_id: all_skills[skill_id]
    for skill_id in ("golang-testing", "nerocd-runner-lifecycle")
}
```

Carry the resulting canonical values into the packet's `selected_skills` map and handoffs
so recipients do not recompute raw file checksums or rediscover the recipe.

## Routing and gates

The configured aliases are Sol=`gpt-5.6-sol`, Terra=`gpt-5.6-terra`, and
Luna=`gpt-5.6-luna`. Explorer is Terra/medium. The config caps child threads at three; agents
cannot spawn children. Parallel writers need separate worktrees and non-overlapping
ownership, then serial integration. Shared-host worktrees reduce accidental overlap but do
not enforce it.

A fresh role-specific dispatch uses `fork_turns=none`. A full-history fork inherits its
parent route, so the dispatch record must preserve that fact. Before implementation, verify
the actual launched model and effort and record the runtime evidence. Static TOML and an
available-model list describe defaults or availability; neither proves which route launched.

The normal gates are discovery, interface/ownership freeze, implementation, independent
routine reviews, any applicable critical risk review, then serial integration and combined
checks. Lifecycle language is limited to controls the active host actually offers: interrupt,
stop, wait, and archive where available.

Choose each required check's exact scope and success criterion before implementation. A full
scan that succeeds reports the state it scanned. A baseline comparison that succeeds reports
that it found no new findings. Do not reinterpret a failed required check after it runs.

If a repair changes the candidate after a review, inspect the repair delta and rerun the
affected checks. Reuse evidence only for checks that the repair cannot affect. Cite the
original candidate SHA and the reason for that conclusion. Required reviews must inspect the
final exact candidate SHA. A failed or not-run required check cannot be waived. Record an
unavailable lint check as `not_run`.

For an advisory review, dispatch a fresh role-specific agent with `fork_turns=none`. Do not
reuse a probe context. Record the reviewer's actual model and effort before accepting its
report, and run formal preflight before a real review.

An external installer needs a valid named `WORKER` checkout and packet, an authorization grant
that names the external target, and observed external verification. A packet cannot claim
repository write ownership for an external-only task. Version 1 cannot represent a task with
only external writes, so record that case as blocked. If an installer also has a genuine bounded
repository receipt edit, that edit may provide the packet's write ownership. Do not add a dummy
edit to bypass preflight.

## Bootstrap pilot note

The two-writer orchestration/skills pilot began from the same SHA before protocol v1 existed.
Its separate worktrees, observed Git states, independent reviews, and serial integration are
real evidence; schema preflight was not enforced at dispatch. Unit tests exercise synthetic
malformed, ownership-conflict, stale-review, failed-check, and recovery scenarios. Do not
retroactively describe those synthetic tests as proof that the bootstrap pilot was isolated
or violation-free.

## Developer validation

```sh
python3 -B -m unittest discover -s orchestration -p 'test_*.py'
python3 -B scripts/orchestration/validate.py --repo-root . config
python3 -B scripts/orchestration/validate.py packet orchestration/examples/worker-packet.json
python3 -B - <<'PY'
import pathlib, tomllib
for path in sorted(pathlib.Path('.codex').rglob('*.toml')):
    tomllib.loads(path.read_text())
PY
git diff --check
```
