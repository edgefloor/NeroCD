# Pstack pilot receipt

These pilots install selected upstream pstack skills beside NeroCD's canonical repository
skills. The upstream packages stay unchanged.

## Pinned installations

All packages come from
[`cursor/plugins`](https://github.com/cursor/plugins/tree/93b00b89ef425a9c1bac0d0b317dfc49c930ac99/pstack/skills)
at commit `93b00b89ef425a9c1bac0d0b317dfc49c930ac99`.

| Skill | Installed inventory | File SHA-256 | Package hash |
| --- | --- | --- | --- |
| `tdd` | `SKILL.md` | `adad031f9e79d7f7389fa128f7af8e03344831270e282cf4e40bdacb8418c386` | Not recorded in Stage 3A |
| `technical-writing` | `SKILL.md`, 11,522 bytes | `bd0cb21034f4fe6695cfdf8cd3561026eec943f0bb6e9300bc78a2b3340865a7` | `e5002620c6ce23fbf64f7ff2a1eff998460aa2702501734800070961f9cbdded` |
| `unslop` | `SKILL.md`, 6,626 bytes | `2789ab80477b7e382292e4d7acca1057784df19713fffb74622ff0f83b2f3733` | `d6156ec4fe7c75dc2b66fc3d20fdc459baf59f1ddf1736b511adcdce7cba38e9` |
| `how` | Five files, 18,299 bytes | `SKILL.md`: `3a554ff564d66a4200a07e20164558e820ea7a9795beb98a4a06546146c98f5f` | `f274ac5e5a0d1f10c27704efa1058be511b25993730acf1676220e4bc3484085` |

The package hashes use NeroCD's `sha256-path-content-v1` algorithm. The installed
`technical-writing`, `unslop`, and `how` inventories, bytes, and executable modes match
the pinned source. The packages contain no adapters or generated metadata.

The pinned `interrogate` package may be staged unchanged outside the repository for a later
pilot. It is not installed globally and has not been invoked. The advisory reviewer route
configuration is present in the repository, but current-host callability remains unverified
until the new routes actually launch. Per-task use supplies the upstream reviewer prompt,
rubric, and code-quality lens to native Sol or Terra reviewer routes; no Cursor configuration,
adapter, or unsupported default-route substitution is implied.
`technical-writing` explicitly requires `unslop`.

The staged `interrogate` inventory contains five unchanged files from the pinned commit:
`93b00b89ef425a9c1bac0d0b317dfc49c930ac99`. Its provenance record is
`/Users/bubble/.codex/run-evidence/nerocd-interrogate-93b00b89/provenance.json`.

The task `Review advisory reviewer gate separation` used task ID
`01a0810b-d903-7cf0-a58f-a0d8973154ea`. The actual advisory routes were Sol/medium and
Terra/medium, and both were verified. The pilot found pre-existing arbitrary-lens and
no-producer-identity limits. Those limits did not come from the new reviewer roles.

The earlier pilot reused probe contexts and bypassed formal preflight for an invalid detached,
external-only `WORKER` task. Treat those as limitations, not evidence of full protocol
compliance. No next pilot run is claimed here.

The `how` package contains:

| Path | Bytes | SHA-256 |
| --- | ---: | --- |
| `SKILL.md` | 7,322 | `3a554ff564d66a4200a07e20164558e820ea7a9795beb98a4a06546146c98f5f` |
| `references/critic-prompt.md` | 2,286 | `aa77a15590814c814bd5cb074842029ab7b790c2e8379050525bdbc20739d1bb` |
| `references/critique-rubric.md` | 2,877 | `5774ff2caf51796e54c4a61b63620a165de5801f23c8f47406e527abfbe9addc` |
| `references/explainer-prompt.md` | 3,121 | `04336086e7c415394459ca2f461717d920e15339517dbf5a56ca042f8b4fead3` |
| `references/explorer-prompt.md` | 2,693 | `fd1218b8cf1f467c644d4ce6fa5e54e6089ede65a28fbbae61f6ee7a2d462f85` |

## Closed tasks

The `Fix interface nil evidence checker` task ended at
`dd0d26dfbaa68c572daca7a33d33f1e9abb67a85`. It passed independent review and landed on
`main`. Its history contains two red-green cycles:

1. `fdb234c` added the interface-nil regression, and `632a314` fixed it.
2. `04b0177` added the parenthesized-nil regression, and `dd0d26d` fixed it.

The Stage 3B tasks also finished:

| Task | Final candidate | Landed through |
| --- | --- | --- |
| `Add Go documentation checker regression tests` | `a30004d65a986cb00a7e9d1346bc3bb310007972` | `99f6340d7ec74c74b68e5a12f39e2eb7ffd42389` |
| `Make NeroCD getting started reproducible` | `7e82ffaf4532f61b9b3c2632cd8a79baf9bd1a17` | `3b8ab68498579f2a30bc9f04609af97a2b5a0257` |

The combined source checks passed the 24-skill validator, focused tests, and diff checks. The
writing pair stayed beside the local Go skills. A narrow disposable harness verified the
repaired getting-started failure paths. The final documentation repairs did not rerun the
whole application.

The checker task used the result template and workspace checks from `7c2c72c` successfully.
The later guide worker returned the unsupported result outcome `completed`. The Leader
caught it, and the worker corrected and validated the record. The validator's allowed-outcome
check remained in force. These trials do not establish causality, implicit skill selection,
or efficiency changes.

## Simple explanation compatibility

The `how` skill's `SIMPLE EXPLAIN` mode fits the current orchestration without an
adapter. The Leader freezes one read-only `ORCHESTRATOR` packet for
`read_only_explorer`. That packet can request the upstream overview, key concepts, runtime
flow, file map, and gotchas. NeroCD's handoff also requires absolute paths, symbols,
assumptions, risks, and validation commands.

The `how` skill remains unchanged. A direct `spawn_agent` call with `read_only_explorer` is
the host equivalent of the upstream read-only `Task` call with `generalPurpose`; this mapping
does not claim native discovery of Cursor configuration. Critique remains unpiloted, although
its unchanged reference files are installed.

The earlier `SIMPLE EXPLAIN` pilot, `Explain documentation checker strict behavior`, used one
child in a clean checkout. Its actual route was Sol/high despite a requested Terra/medium route,
so it does not validate the intended route. No formal detached-HEAD preflight was run. Its
explanation also overstated the helper's error behavior by saying all build-environment errors
exit 2; only a missing Go executable is normalized to exit 2. The pilot made no code or skill
changes.

The later complex `how` pilot, `Explain runner crash recovery`, used two read-only explorers
and a third synthesis task. The Leader confirmed the actual Terra/medium route, and all three
formal detached `branch: null` preflights passed. It inspected source only and did not run
runtime recovery tests. The output corrected two points: `JournalEvent.Message` may contain
secrets or paths despite no dedicated fields, and a lost-response retry must occur before a
valid acknowledgement removes the event. Its journal-message and diagram claims remain
explanatory limitations, not proof of controller behavior or a route guarantee.

## Pilot cadence

The Leader launches real tasks in separate worktrees and continues independent stage work
without waiting. The tasks receive their normal engineering goal, not a scoring prompt. The
user returns when an outcome is ready to examine.
