# CI and registry release trust

The local evidence command is intentionally unsigned and non-publishing. The
only repository source that may publish a NeroCD image is
`.github/workflows/release.yml`; this document describes its required external
controls. It does not constitute evidence that a release has run.

## Required GitHub configuration

Before a release is approved, `edgefloor/NeroCD` needs a protected `release`
environment with required reviewers and an owner-only deployment policy. Its
rulesets must allow only trusted maintainers to create annotated
`vMAJOR.MINOR.PATCH` tags. The workflow verifies tag syntax, safe ref, tag
object, and target revision. Signature verification, maintainer key management,
and ruleset enforcement remain GitHub administration duties.

GitHub Actions must have OIDC, artifact attestations, and the `GITHUB_TOKEN`
package permission available for `ghcr.io/edgefloor/nerocd`. The GHCR package
must not permit anonymous or foreign write access, and its registry policy must
retain released digest references. A pre-existing semantic-version tag is
rejected: a correction always uses a new semantic-version tag instead of
replacing a tag, digest, or trust artifact from an earlier release.

The release workflow accepts a pushed semantic-version tag or a manually
entered existing tag. Its read-only evidence job must pass
`make release-evidence-gate` before the protected publish job can run. Before
any release approval, that job installs the commit-pinned Cosign installer,
selects Cosign v3.0.6, and checks its exact version and required command flags.
The protected publish job repeats the same credential-free preflight before
Buildx setup, registry login, or image publication. The evidence job uploads
only the exact transferable evidence contract: Linux binaries, checksums,
CycloneDX SBOM, local manifest, local in-toto statement, and an outer evidence
checksum record. The publish job downloads and recomputes that contract before
use, builds a Linux amd64/arm64 OCI index, compares Buildx’s result with the
registry’s exact digest, and writes canonical
`release-trust.json` binding repository/tag/commit/registry digest to every
retained evidence-file hash. It signs both the digest and the exact trust
manifest bytes keylessly with GitHub OIDC using Cosign’s v3 bundle format,
retaining bundles with transparency-log material, then writes a GitHub
build-provenance attestation.

The read-only verification job downloads both named artifacts, recomputes both
checksum layers, validates the CycloneDX/local manifest/local in-toto bindings,
checks the canonical trust binding and attestation metadata, verifies the
retained trust-manifest bundle against the exact manifest bytes, verifies the
exact image signature online against the immutable registry digest, verifies
the GitHub attestation, and checks precisely the two published Linux platforms.
`image.bundle` remains in `trust.sha256` as checksummed, retained audit
evidence; Cosign v3.0.6 does not support `cosign verify --bundle` for an image,
so the registry-backed exact-digest verification is authoritative.

## Local policy validation

Run `make ci-release-policy-gate`. It parses workflow source without calling
GitHub or a registry. It rejects mutable action references, unpinned CI
services, missing protected-environment/evidence wiring, missing
provenance/signature/digest verification, an incompatible Cosign
version/command contract, unsupported image-bundle verification, and direct
`docker push`. It also mutates disposable workflow copies to prove those
checks fail.

This local gate never creates tags, signs, publishes, uploads, or contacts
GHCR. The workflow itself is unexecuted locally; CI publication, OIDC identity,
registry policy enforcement, and public release visibility remain external,
auditable GitHub/GHCR operations.
