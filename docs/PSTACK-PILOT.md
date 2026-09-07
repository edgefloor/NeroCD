# Pstack `tdd` pilot

This is a narrow Stage 3 pilot of one upstream skill. It does not install or adopt the
full pstack plugin, and it is outside NeroCD's canonical 24 repository skills.

## Provenance receipt

- Source: [`cursor/plugins`](https://github.com/cursor/plugins/tree/93b00b89ef425a9c1bac0d0b317dfc49c930ac99/pstack/skills/tdd), commit `93b00b89ef425a9c1bac0d0b317dfc49c930ac99`
- Package path: `pstack/skills/tdd`
- Installed package: `/Users/bubble/.agents/skills/tdd`
- Installed file: `SKILL.md`
- Pinned `SKILL.md` SHA-256: `adad031f9e79d7f7389fa128f7af8e03344831270e282cf4e40bdacb8418c386`

The package was installed unchanged. In particular, its upstream name, description, and
`disable-model-invocation: true` metadata remain intact. The metadata is preserved as source
content; this pilot does not assume any particular runtime will enforce automatic invocation.

Reproduce the installation from a clean destination with:

```sh
python3 -B /Users/bubble/.codex/skills/.system/skill-installer/scripts/install-skill-from-github.py \
  --repo cursor/plugins \
  --ref 93b00b89ef425a9c1bac0d0b317dfc49c930ac99 \
  --path pstack/skills/tdd \
  --dest /Users/bubble/.agents/skills
```

Verify the installed file against the receipt with:

```sh
shasum -a 256 /Users/bubble/.agents/skills/tdd/SKILL.md
```

Installation bytes have been verified. Fresh-task discovery and runtime invocation have not
yet been verified; invoke the pilot explicitly as `$tdd` in the next task.

## Next real task

Give a fresh implementation task this prompt:

> Follow repository orchestration. Use `$tdd` from `/Users/bubble/.agents/skills/tdd/SKILL.md`
> together with NeroCD's `go-interfaces` and `golang-testing` skills to fix the
> interface-compliance checker so `var provider Sink = nil` does not count as evidence of a
> concrete implementation. Typed concrete nil pointers, such as `var provider Sink =
> (*SinkImpl)(nil)`, must continue to count for both named and blank-identifier declarations;
> an uninitialized interface declaration must remain a finding. First add a focused regression
> test that fails for the current behavior, then make the smallest helper change and show the
> test passing afterward. Preserve the helper's CLI options, JSON shape, exit-code semantics,
> scan boundaries, and skill provenance. Limit changes to the helper, focused tests, and the
> required local skill hash update. Do not redesign the analyzer broadly or modify upstream
> pstack skills. Return the candidate commit and the failing-before and passing-after evidence.

This is a product task that produces evidence for later evaluation; it is not an evaluation
rubric or a request to score the pilot.
