# Codex agent orchestration

This project uses a read-only Leader to coordinate bounded subagent work. The project configuration enables at most three concurrent subagent threads; agents never spawn their own subagents.

## Roles and models

| Role | Model / effort | Sandbox | Purpose |
| --- | --- | --- | --- |
| read-only explorer | Luna / low | read-only | Map code paths and evidence before changes |
| mechanical worker | Luna / low | workspace-write | Small, mechanical, bounded edits |
| bounded worker | Terra / medium | workspace-write | Ordinary scoped implementation |
| complex worker | Sol / medium | workspace-write | Multi-step implementation after interface freeze |
| critical worker | Sol / high | workspace-write | High-risk implementation with explicit approval |
| integration steward | Terra / medium | workspace-write | Integrate Leader-approved commits and run combined checks |
| code hygiene reviewer | Terra / medium | read-only | Routine code hygiene review |
| specification reviewer | Terra / medium | read-only | Routine specification-fit review |
| cryptographic correctness reviewer | Terra / high | read-only | Concrete cryptographic correctness review |
| critical security reviewer | Terra / high | read-only | Security review only for critical application areas |
| escalation resolver | Sol / high | workspace-write | Resolve Leader-approved material conflicts or blockers |

Sol means `gpt-5.6`, Terra means `gpt-5.6-terra`, and Luna means `gpt-5.6-luna`. Sol is limited to low/medium/high/extra-high (`xhigh`) effort; Terra to medium/high; Luna may use any supported effort. The default spawned-agent model is Terra at medium effort, and basic roles default to low, medium, or high according to their bounded task. Model and effort may be explicitly overridden by the Leader within those limits.

## Normal turn lifecycle

1. The Leader captures the user goal, current workspace state, constraints, and validation commands.
2. The Leader delegates discovery and receives an evidence-backed map.
3. The Leader freezes interfaces and ownership. Up to three writers may proceed only on disjoint ownership.
4. Writers load applicable Go skills, use `colgrep` first, preserve unrelated changes, self-verify, and return evidence.
5. Read-only reviewers assess code hygiene and specification fit. Cryptographic correctness and critical security review are added only for the relevant critical areas.
6. The Leader approves commits for integration. The integration steward combines only those commits and runs combined checks.
7. The Leader accepts the result or routes unresolved conflicts and risks to the escalation resolver.

## Example delegation prompts

```text
Use read_only_explorer to map the affected Go call paths. Return absolute file paths,
symbols, current behavior, constraints, and proposed interface seams. Do not edit or
spawn subagents.
```

```text
After I freeze interfaces, assign bounded_worker and mechanical_worker disjoint ownership:
<worker A scope> and <worker B scope>. Preserve unrelated changes, load applicable Go
skills, run the listed checks, and return evidence. Do not let either worker spawn agents.
```

```text
Run code_hygiene_reviewer and specification_reviewer in parallel against the approved
diff. Add cryptographic_correctness_reviewer or critical_security_reviewer only if the
changed files touch the critical-area list. All reviewers are read-only; report findings
with file paths and concrete evidence.
```

## Validation

From the repository root, validate the configuration with:

```sh
python3 - <<'PY'
import pathlib, tomllib
for path in sorted(pathlib.Path('.codex').rglob('*.toml')):
    with path.open('rb') as handle:
        tomllib.load(handle)
    print(f'valid TOML: {path}')
PY
TERM=xterm-256color codex --strict-config -C /Users/bubble/Documents/Dev/NeroCD doctor --summary --no-color
```

## Limitations

Custom agent files provide defaults, but a live parent runtime can supersede a custom agent's sandbox or approval settings. Project-local configuration is loaded only for a trusted project. Worktree isolation is a separate concern and still requires a committed baseline; these files do not create or commit one. The Leader protocol is an instruction boundary, not a replacement for code review, tests, or repository permissions.
