# Compare v1 and native orchestration

This plan compares coordination cost without changing product inputs. Do not run the timed
trials while the current long product task is active. The experimental branch provides the
foundation and the frozen plan only. It does not contain measured results or proof of live hook
delivery.

## Freeze the two arms

- Arm A uses exact commit `39d62767da97c2849d36409dc6024e4b819474a9` and the v1 record protocol.
- Arm B uses the accepted native candidate from this branch.
- Each pair starts with product files from
  `39d62767da97c2849d36409dc6024e4b819474a9`. Arm B differs only in orchestration instructions,
  the native validator, its tests, and policy workflow files.
- Both arms use the same role, model, effort, required reviews, product checks, pstack setting,
  and concurrency limit. Start each run with fresh agent contexts.
- Run the arms sequentially after the long task is idle. Alternate `A,B` and `B,A` order with a
  recorded random seed. Warm or clear comparable caches before each pair.

Do not reuse the active migration as a sample. Do not show actors a known fix SHA or diff.

## Use three bounded tasks

Freeze each brief before the pilot. Confirm that its expected behavior is absent on the shared
product base and that both arms receive byte-identical product files.

1. **Small focused regression, ready.** Change `POST /api/v1/templates` and
   `PATCH /api/v1/templates` so invalid template inputs return status 400 and are not
   persisted. Add focused tests in `internal/api/server_test.go` and preserve valid template
   creation, updates, and response fields. The required check is
   `go test ./internal/app ./internal/api`.
2. **Medium specialist task, pending fixture preparation.** Use a runner journal replay defect
   grounded in the durable lease, cancellation, replay, and shutdown invariants. Before the
   pilot, record exact source paths, a fixture commit applied to both arms, and an expected
   failing test. Require `code_hygiene`, `specification`, and `critical_security` reviews and
   select `nerocd-runner-lifecycle`. Do not promise exactly-once behavior beyond the repository's
   actual journal contract.
3. **Multi-slice dependency task, pending fixture preparation.** Use one frozen API-to-WebUI
   contract defect that requires separate API, frontend, and integration slices. Before the
   pilot, record exact source paths, a fixture commit applied to both arms, focused source
   tests, generated artifact verification, and Go embedding verification.

Tasks 2 and 3 are not runnable benchmark fixtures yet. The observer must prepare and validate
them before timed pilots, then apply the same fixture commit to temporary branches for both
arms. Record the fixture commit, product-tree hash, task brief hash, exact paths, and expected
failing test in the experiment manifest. Do not modify the native candidate or control commit
to store the fixtures.

## Freeze failure scenarios

Exercise these scenarios in both arms:

- wrong worktree or branch;
- dirty tracked or untracked state;
- a rename or deletion outside ownership;
- a required check marked `failed` or `not_run`;
- a review pinned to a stale candidate SHA;
- review rejection followed by repair and new reviews;
- integration without Leader approval;
- interruption and resume with missing evidence;
- unrelated changes present in the worktree.

Any prohibited acceptance fails the experiment. This zero-tolerance rule applies even when the
run is faster.

## Measure observer-only data

Store observations as JSON or CSV. Metrics never decide whether a run may continue.

Measure elapsed time from initial dispatch to Leader acceptance, including worktree setup,
checks, reviews, and repairs. Record fixed experiment setup separately. Also record tool calls,
agent turns, user interventions, files changed, repair count, and tokens when the host exposes
them. Score product quality against the frozen behavior and tests before comparing speed.

Run three paired pilots first. Inspect prohibited acceptances, task comparability, metric
capture, and outliers. If the pilot is sound, repeat enough pairs to estimate variation. A 25%
reduction in coordination time is the provisional target. It is not a measured improvement or
an acceptance waiver.

Timing, live hook delivery, and end-to-end native acceptance remain `not_run` for the foundation
candidate.
