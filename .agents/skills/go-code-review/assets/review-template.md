# Code Review: [PR Title]

## Summary
[Brief description of the changes]

## Findings

### Must Fix
- [ ] [file:line] Issue, behavioral consequence or explicit repository rule, and owning skill with applicable exceptions checked

### Should Fix
- [ ] [file:line] Issue, behavioral consequence or explicit repository rule, and owning skill with applicable exceptions checked

### Nits
- [ ] [file:line] Optional suggestion and its reason

## Check evidence

Record actual commands and results. Replace the example rows and retain every
required check, including wider integration checks. Use only `passed`, `failed`,
or `not_run` for status. Explain every `not_run` result.

| Check | Scope/base | Candidate SHA | passed/failed/not_run | Evidence |
| --- | --- | --- | --- | --- |
| `make fmt-check` via pre-review | [product sources] | [full SHA] | [status] | [bounded output or location] |
| Pinned lint via pre-review `--base` | [local package scope; full base SHA] | [full SHA] | [status] | [bounded output or location] |
| Standalone `go vet` | [local package scope] | [full SHA] | `not_run` | Covered by pinned lint's `govet` analyzer; cite its actual result above |
| [Required wider check] | [scope; base if applicable] | [full SHA] | [status] | [bounded output or location] |

For retained evidence, keep the original candidate SHA and explain why the check
is unaffected by later changes. Do not label earlier evidence as a rerun.

## Review lenses

Record independent `code_hygiene` and `specification` results at the same full
candidate SHA. Add `critical_security` or `cryptographic_correctness` when the
scope requires it. These results do not waive failed or missing required checks.

## Skills Applied
[List of go-* skills referenced during review]
