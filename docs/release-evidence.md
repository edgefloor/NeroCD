# Local release evidence

`make release-evidence-gate` is a local, fail-closed consistency check. It
requires a clean worktree and does not tag, push, sign, upload, or publish. It
builds Linux amd64/arm64 artifacts, checks the image and release contents, and
writes unsigned local evidence under `artifacts/release-evidence/`.

Those records are marked `UNTRUSTED_LOCAL_NO_SIGNATURE`. They help review a
candidate but are never release attestations or publication evidence. CI is the
only path that signs, publishes, and verifies a release.

`make release-evidence-synthetic-gate` uses a temporary synthetic commit for
dirty-source experiments. Its output is `synthetic_untrusted` and must never be
published or described as evidence for a repository revision. It is prohibited
when the checkout contains `.delta`; do not create a local synthetic archive in
that case.

The gate runs its accepted local inventory and fails when required Buildx,
emulation, browser, or executable dependencies are missing. It also compares
repeat outputs and detects a tampered binary/checksum mismatch.
