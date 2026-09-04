# Agent orchestration protocol v1

NeroCD's first-release protocol is a preflight and durable handoff validator. A human or
Leader still dispatches agents, invokes reviews, integrates serially, and decides whether to
stop or archive a task. The scripts never execute commands stored in records.

## Trust boundary

The validator checks JSON shape, configured routes, explicit runtime attestations, selected
skill hashes, Git base/branch/cleanliness, actual changed paths, review/check pinning, and
checkpoint lineage. A capability or check record is an operator/runtime attestation: parsing
it does not prove OS isolation, tool execution, model availability, or the truth of a
recorded check status. Codex TOML is a default, not a permission attestation.
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

`orchestration/examples/worker-packet.json` is the editable template; the same directory
contains result and review templates. Their placeholder SHAs must be replaced. Packet fields are:

- `task_id`, `mode`, and `goal` identify the delegation.
- `repository` pins an absolute `workspace_root`, full lowercase `base_sha`, and expected
  `branch`. The CLI's `--repo-root` must identify that exact Git toplevel.
- `ownership.read_paths` is advisory discovery scope. `write_paths` is enforced against Git
  changes using repository-relative exact files or directory prefixes ending in `/`.
- `interfaces`, `constraints`, `validation`, and `handoff` carry the frozen seven-part brief.
  `validation` values are stable check IDs, not shell commands.
- `route` pins role/model/effort and required tool names.
- `selected_skills` maps canonical `.agents/skills/` IDs to raw lowercase SHA-256 hashes
  emitted by `scripts/skills/validate.py --json`. It may be empty when no skill applies.
- `authorization.grants` preserves already-issued grants. Each has `action`, explicit
  `target`, and `granted_by`. Absence of a target is a blocker, not a request to infer one.
- `required_review_lenses` names independent reviews. Writable modes require at least one.
- `required_completion_stages` selects from `source`, `artifact`, `published`, and
  `deployed`. Only relevant stages are required; publication and deployment are not assumed.

Modes are `ORCHESTRATOR`, `WORKER`, `REVIEWER`, and `INTEGRATOR`. Role/mode pairs are fixed:
explorer/orchestrator, implementation roles/worker, review roles/reviewer, and integration
steward/integrator.

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

## Root-relative commands

Run these from the repository root. For one packet, `preflight.py` defaults `--repo-root` to
the current directory; multiple packets require one ordered `--repo-root` per packet.

```sh
python3 scripts/orchestration/validate.py --repo-root . config
python3 scripts/orchestration/validate.py packet /run/records/packet.json
python3 scripts/orchestration/validate.py digest /run/records/packet.json
python3 scripts/orchestration/preflight.py \
  --capabilities /run/records/runtime-capabilities.json \
  --packet /run/records/packet.json \
  --repo-root /absolute/path/to/worker-worktree \
  --skills-validator scripts/skills/validate.py
python3 scripts/orchestration/validate.py --repo-root /absolute/path/to/worker-worktree \
  result /run/records/result.json --packet /run/records/packet.json
python3 scripts/orchestration/validate.py review /run/records/spec-review.json \
  --packet /run/records/packet.json --candidate 0123456789abcdef0123456789abcdef01234567
python3 scripts/orchestration/checkpoint.py \
  --repo-root /absolute/path/to/worker-worktree \
  --state /run/state/checkpoint.json --stage accepted \
  --packet /run/records/packet.json --result /run/records/result.json \
  --review /run/records/hygiene-review.json --review /run/records/spec-review.json
python3 scripts/orchestration/validate.py --repo-root /absolute/path/to/worker-worktree \
  checkpoint /run/state/checkpoint.json
python3 scripts/orchestration/validate.py checkpoint /run/state/checkpoint.json \
  --offline-recovery
```

Checkpoint verification normally requires the packet's exact original Git toplevel and
rechecks its branch, candidate, cleanliness, and changed paths. If that workspace has been
retired, `checkpoint ... --offline-recovery` checks record digests and replays result,
required-check, review-quorum, and completion semantics without a checkout. Its output says
that current Git facts were not verified; offline recovery is not live candidate acceptance.

The skills validator is required only when `selected_skills` is non-empty. Its hash algorithm
and canonical tree are owned and documented by the skills subsystem; orchestration compares
the returned hashes and does not duplicate that algorithm.

## Routing and gates

The configured aliases are Sol=`gpt-5.6-sol`, Terra=`gpt-5.6-terra`, and
Luna=`gpt-5.6-luna`. Explorer is Terra/medium. The config caps child threads at three; agents
cannot spawn children. Parallel writers need separate worktrees and non-overlapping
ownership, then serial integration. Shared-host worktrees reduce accidental overlap but do
not enforce it.

The normal gates are discovery, interface/ownership freeze, implementation, independent
routine reviews, any applicable critical risk review, then serial integration and combined
checks. Lifecycle language is limited to controls the active host actually offers: interrupt,
stop, wait, and archive where available.

## Bootstrap pilot note

The two-writer orchestration/skills pilot began from the same SHA before protocol v1 existed.
Its separate worktrees, observed Git states, independent reviews, and serial integration are
real evidence; schema preflight was not enforced at dispatch. Unit tests exercise synthetic
malformed, ownership-conflict, stale-review, failed-check, and recovery scenarios. Do not
retroactively describe those synthetic tests as proof that the bootstrap pilot was isolated
or violation-free.

## Developer validation

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s orchestration -p 'test_*.py'
python3 scripts/orchestration/validate.py --repo-root . config
python3 scripts/orchestration/validate.py packet orchestration/examples/worker-packet.json
python3 - <<'PY'
import pathlib, tomllib
for path in sorted(pathlib.Path('.codex').rglob('*.toml')):
    tomllib.loads(path.read_text())
PY
git diff --check
```
