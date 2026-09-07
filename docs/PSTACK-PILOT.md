# Pstack skill pilots

These pilots install selected upstream pstack skills beside NeroCD's canonical repository
skills. The upstream packages stay unchanged.

## Pinned installations

All three packages come from
[`cursor/plugins`](https://github.com/cursor/plugins/tree/93b00b89ef425a9c1bac0d0b317dfc49c930ac99/pstack/skills)
at commit `93b00b89ef425a9c1bac0d0b317dfc49c930ac99`.

| Skill | Upstream path | Installed inventory | File SHA-256 | Package hash |
| --- | --- | --- | --- | --- |
| `tdd` | `pstack/skills/tdd` | `SKILL.md` | `adad031f9e79d7f7389fa128f7af8e03344831270e282cf4e40bdacb8418c386` | Not recorded in the Stage 3A receipt |
| `technical-writing` | `pstack/skills/technical-writing` | `SKILL.md`, 11,522 bytes | `bd0cb21034f4fe6695cfdf8cd3561026eec943f0bb6e9300bc78a2b3340865a7` | `e5002620c6ce23fbf64f7ff2a1eff998460aa2702501734800070961f9cbdded` |
| `unslop` | `pstack/skills/unslop` | `SKILL.md`, 6,626 bytes | `2789ab80477b7e382292e4d7acca1057784df19713fffb74622ff0f83b2f3733` | `d6156ec4fe7c75dc2b66fc3d20fdc459baf59f1ddf1736b511adcdce7cba38e9` |

The package hashes use NeroCD's `sha256-path-content-v1` algorithm. Detached source and
installed package inventories, file bytes, and executable modes matched. The writing
packages contain no generated adapter or metadata files. `technical-writing` explicitly
requires `unslop`, so Stage 3B installs both under `/Users/bubble/.agents/skills`.

Reproduce the Stage 3B installation only when both destination directories are absent:

```sh
python3 -B /Users/bubble/.codex/skills/.system/skill-installer/scripts/install-skill-from-github.py \
  --repo cursor/plugins \
  --ref 93b00b89ef425a9c1bac0d0b317dfc49c930ac99 \
  --path pstack/skills/technical-writing pstack/skills/unslop \
  --dest /Users/bubble/.agents/skills
```

## Completed TDD task

The Stage 3A task was `Fix interface nil evidence checker`. The final commit
`dd0d26dfbaa68c572daca7a33d33f1e9abb67a85` passed independent review and landed on
`main`. Its history contains two red-green cycles:

1. `fdb234c` added the failing interface-nil regression, and `632a314` fixed it.
2. `04b0177` added the failing parenthesized-nil regression, and `dd0d26d` fixed it.

The task stayed within the checker, its focused tests, and the required skill hash update.
This result shows that the upstream `tdd` instructions could coexist with the local Go
skills for that bounded task. It does not establish causality, token savings, or implicit
skill selection.

The orchestration handoff corrections in `7c2c72ce6b09e8d189dccc2c9d287ed15d90d805`
have also landed. A fresh task has not yet completed runtime validation of those corrections.

## Current tasks and cadence

The Leader launches real tasks in separate worktrees and continues independent stage work
without waiting for them. The tasks do not receive a scoring or evaluation prompt. The user
returns when an outcome is ready to examine.

Two tasks are currently queued:

- `Add Go documentation checker regression tests` starts from `7c2c72c` and is limited to
  the go-documentation checker tests and required local hash.
- `Make NeroCD getting started reproducible` explicitly applies the installed
  `technical-writing` and `unslop` skills to `docs/getting-started.md` and necessary
  `docs/index.md` links. It checks current source and configuration, then verifies the setup
  instructions in an isolated disposable environment. It must preserve every supported mode,
  security requirement, application behavior, and upstream skill package.

Both tasks are awaiting outcomes. Their results can inform later revisions without making
runtime-selection or writing-quality claims in advance.
