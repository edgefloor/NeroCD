# Skill inventory audit

NeroCD's canonical skill inventory lives in `.agents/skills`. `.claude/skills` contains compatibility symlinks to that inventory; `agent/skills` is intentionally absent. The lock and validator cover every canonical package.

## Curation batch

The first normalized upstream batch contains:

- `golang-security`, from `samber/cc-skills-golang` commit `bac46b0bed2677f840837e16be2c790341bda2df`, upstream package hash `ed6b120dae82dff9267b58bb17186c4289643698e4284c51185bdc06b4b8078f` (14 regular files).
- `golang-testing`, from the same commit, upstream package hash `8dc90b6844c55a1bf424fbc86e51e63d22a2c2443950e52e041a62f2bc58ebc8` (9 regular files). It replaces the legacy `go-testing` package.

Both packages were compared byte-for-byte with the pinned Git checkout before normalization. Their referenced files and upstream evaluation fixtures are retained, and each package includes the upstream MIT license and `SOURCE.json`. Normalization version 1 narrows descriptions, removes model, reasoning, agent-fanout, worktree, tool-install, and task-scope choices, and softens generic testing mandates that are repository-dependent. It does not weaken the security package's concrete trust-boundary constraints.

The other `go-*` skills remain from `cxuu/golang-skills`; `research` remains attributed to `mattpocock/skills`. Their exact upstream commits were not recorded when originally installed, so `skills-lock.json` deliberately uses `sourceCommit: null` and `provenanceStatus: upstream-commit-unknown`. Historical `computedHash` values are retained as installer metadata, not reinterpreted as package hashes.

Two repository-local procedures were added:

- `nerocd-ui-artifact-verification` covers source tests, ignored `web/dist` hash manifests, mobile browser checks, Go embedding, and deployed-byte/cache comparison without granting deployment authority.
- `nerocd-runner-lifecycle` covers fenced lease authority, cancellation, append-before-send journaling, replay ordering, acknowledgment, and owned shutdown.

## Duplicate-tree decision

At base commit `71dae39d8f3870a85a14855f3f9a187332e288e6`, the 20 skill directories under `agent/skills` and `.agents/skills` had identical file inventories and modes. Of 83 paired regular files, 63 were byte-identical. The remaining 20 were exactly the `SKILL.md` entrypoints: canonical copies added valid `name` fields, and some added `allowed-tools`; their instruction bodies and supporting assets, references, and scripts matched. There were no mode differences. `.agents/skills` also contained the canonical-only `research` skill.

That evidence supports retaining `.agents/skills` and deleting `agent/skills`. The deleted tree remains recoverable from Git with `git restore --source=71dae39d8f3870a85a14855f3f9a187332e288e6 -- agent/skills` if a historical checkout is needed. Restoring it in normal development would intentionally make validation fail because duplicate writable inventories drift.

## Package hash

`localHash` uses `sha256-path-content-v1`:

1. Walk every regular file in a skill directory and reject any internal symlink or non-regular entry.
2. For each file, record `path` relative to the skill directory with `/` separators, the lowercase SHA-256 of its bytes as `sha256`, and whether any executable bit is set as the Boolean `executable`.
3. Sort entries by `path`.
4. JSON-serialize the entry array with `sort_keys=True`, separators `(',', ':')`, and `ensure_ascii=True`.
5. SHA-256 the UTF-8 JSON bytes without a trailing newline.

This package digest covers instructions, references, scripts, assets, licenses, evaluation fixtures, and metadata. `python3 scripts/skills/validate.py --json` emits exactly `{"skills":{"skill-id":"localHash"}}` on success for orchestration preflight use.

## Selection policy and evaluation limits

Interactive skills remain eligible for implicit selection through narrow, discriminating descriptions. `research` is explicit-only and declares `policy.allow_implicit_invocation: false`. This policy controls automatic context injection; it is not a security sandbox or authorization boundary.

`scripts/skills/selection-scenarios.json` records positive, negative, and overlapping requests with exact expected skill sets. The validator checks that fixture structure and reports the declared choices in human-readable mode. Static descriptions and fixtures cannot prove model behavior, so autonomous exact selection requires independent forward evaluation. Such evaluation should record the prompt, selected skills, observable procedure use, and any unavailable checks as `not_run`; it should not infer permission to install tools, create worktrees, delegate, deploy, or broaden scope.

## Validation

Run from the repository root with bytecode generation disabled:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 scripts/skills/validate.py
PYTHONDONTWRITEBYTECODE=1 python3 scripts/skills/validate.py --json
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts/skills -p 'test_*.py'
PYTHONDONTWRITEBYTECODE=1 python3 .codex/hooks/test_product_go_advisory.py
```

The validator is read-only and uses only the Python 3.11+ standard library. Its tests mutate temporary copies to prove that digest mismatch, broken compatibility links, duplicate trees, and package-internal symlinks are rejected.
