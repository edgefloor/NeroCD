# Working on NeroCD

## Discover the source

Use `colgrep` first when the relevant files are unknown. Search source, documentation,
tests, and configuration. Use exact searches for known names or strings.
If `.codegraph/` exists, use CodeGraph to trace discovered symbols and call paths.
Treat documentation as intended behavior and verify it against current source.
Load applicable Go skills from `.agents/skills/`. `.claude/skills/` links to that inventory.

## Make focused changes

Verify the workspace, branch, base revision, and Git status before editing. Preserve
unrelated work. Do not reset, clean, overwrite, or delete another contributor's changes.
Use explicit working directories. Keep ownership clear when several agents work in parallel.
The host manages delegation and threads. Use separate worktrees for concurrent writers.
Do not create repository coordination records or a custom delegation protocol.

Keep changes within the requested scope. External actions need an authorized target.
Do not push, publish, or rewrite history without authorization.

## Validate the affected behavior

Run commands from the repository root:

- `make fmt-check`, `make vet`, and `make test` check Go formatting, analysis, and tests.
- `make lint` uses the pinned linter. `make lint-new LINT_BASE_REV=<commit>` checks new findings.
- `make shell-check` checks shell scripts. `make verify` combines the standard Go and shell checks.
- `make skill-tools-test` checks skill tools and inventory.
- `make web-test` and `make build` test and build the WebUI and embedded application.
- `make check` runs broader integration checks and requires Docker and browser dependencies.

Choose checks from the changed behavior and its dependencies. Use focused checks during
implementation and broader checks at integration boundaries. Report each required check as
passed, failed, or not run, with its revision and any limitation. Never infer a missing pass.

For runner changes, use `nerocd-runner-lifecycle` and preserve lease fencing, cancellation,
durable journaling, replay, and shutdown invariants. For UI changes, use
`nerocd-ui-artifact-verification` to verify source, responsive behavior, generated `web/dist`,
and Go embedding. Do not commit generated artifacts unless the project tracks them.

Review code hygiene and specification fit independently for substantive changes at the same
candidate revision. Add security or cryptographic review when the changed boundaries warrant
it, including authentication, credentials, process execution, persistence, and lifecycle.
A repair requires review of the new revision and rerunning affected checks.

## Commit and hand off

Use concise conventional, lower-case subjects: `type(scope): imperative behavior`.
Keep one coherent outcome per commit, with its regression tests and required generated updates.
Use `fix` for behavior fixes, `test` when production behavior is unchanged, `docs` for
instructions, and `perf` only with measurement evidence. Explain non-obvious reasons and
validation limits in the body. Return commit SHAs, changed paths, checks, and remaining risks.
