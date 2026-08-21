# Local release evidence

`make release-evidence-gate` is a local, fail-closed evidence check. It
requires a completely clean Git worktree and performs no registry push, image
load, signing, upload, tag, or publication. It derives the version, revision,
and `SOURCE_DATE_EPOCH` from Git; builds trimpath Linux amd64/arm64 binaries;
exports a local multi-platform OCI archive with Buildx; verifies the final
image's non-root entrypoint, pinned bases, and absence of source maps, source,
development seed, and legacy default credential material; and writes a
CycloneDX SBOM, checksums, canonical local manifest, and explicitly unsigned
in-toto/SLSA-shaped provenance statement under `artifacts/release-evidence/`.

The provenance and manifest are intentionally marked
`UNTRUSTED_LOCAL_NO_SIGNATURE`. They are useful for local consistency and
reproducibility review, never as a release attestation.

For a dirty development checkout, use:

```sh
make release-evidence-synthetic-gate
```

This copies the current source (including untracked source files) into a
temporary repository, creates one disposable local commit, and runs the exact
same gate. Its outputs are labelled `synthetic_untrusted`; they must not be
published or described as evidence for the source repository's release commit.

The gate intentionally runs every listed accepted local check through
`release-evidence-accepted-gates`. Missing Docker Buildx, emulation, browser,
or a required executable is an error rather than a skip. It also builds twice,
compares the binary/SBOM and canonical OCI manifest graph, and proves that a
tampered binary no longer matches the generated checksums.

CI remains responsible for trusted release signing, identity-bound provenance,
registry publication, retention, and any public release workflow. Those steps
are deliberately outside this local slice.
