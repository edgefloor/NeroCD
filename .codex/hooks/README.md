# Product Go advisory hook

After `apply_patch`, this repository-local hook checks up to eight changed Go
files beneath `cmd/` or `internal/`. It reports concise `gofmt` and repository
naming-checker findings without modifying files or blocking the edit.

The adapter accepts at most 2 MiB of hook input and 1 MiB per Go file. Each
child process is limited to 64 KiB of stdout, 16 KiB of stderr, one second of
execution, and the adapter has a four-second total budget. Any malformed input,
unsafe path, overflow, timeout, or tool failure fails open with no output.

## Integrity chain

The trusted command in `.codex/hooks.json` contains inline, isolated Python
that securely opens the adapter beneath the Git root, verifies its pinned
SHA-256 digest, and executes only the verified bytes. It leaves the original
hook stdin untouched for the adapter. If the adapter changes without the hook
definition changing too, the previously trusted command exits silently and the
changed adapter does not run.

The adapter independently opens
`.agents/skills/go-naming/scripts/check-naming.sh` through root-relative
`O_NOFOLLOW` descriptor traversal, verifies its separately pinned SHA-256
digest, decodes the verified bytes as UTF-8, and closes the workspace
descriptor. It invokes the immutable verified text with fixed `/bin/bash -c`;
the repository inode, path, and executable bit are not trusted after
verification. A missing, replaced, modified, oversized, undecodable, or
mismatched naming checker fails open and is never executed. These two pins are
intentionally one-way: the hook definition pins the adapter, and the adapter
pins the naming checker.

## Integration tests

The hook runtime is POSIX-only. It requires the fixed system paths
`/usr/bin/python3` (Python 3.9 or newer), `/usr/bin/git`, and `/bin/bash`, plus
`gofmt`, `O_DIRECTORY`/`O_NOFOLLOW` descriptor-relative opens, and a working
`/dev/fd`. The integration tests have the same prerequisites. They create
temporary Git repositories and do not modify live product sources.
The session `PATH` must resolve `gofmt` and the standard utilities used by the
pinned naming checker (such as `basename` and `sed`) to trusted toolchain
installations.

```sh
PYTHONDONTWRITEBYTECODE=1 python3 .codex/hooks/test_product_go_advisory.py
```

## Review and trust

Repository hooks do not run until their exact definition is reviewed and
trusted. In the Codex CLI, open `/hooks`, inspect the project-local
`.codex/hooks.json` definition and command, then trust it manually if it is the
expected version. Codex hashes the definition; review and trust it again after
any hook change. Never bypass hook trust for this repository.

When the naming checker changes intentionally, first update its digest in the
adapter. Then recompute the adapter digest and update the inline expected digest
in `.codex/hooks.json`. When only the adapter changes, update the hook digest
directly. Run the integration suite and `make verify`, start a new Codex
session, and use `/hooks` to review and trust the new exact definition. Do not
run `/hooks trust` from automation.

Treat an already-running session as retaining the hook definition it loaded.
The old definition will reject a newly changed adapter because its digest no
longer matches; the replacement definition should be reviewed in a new session.
