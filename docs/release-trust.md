# Release trust

Release publication starts from the next unused annotated `vMAJOR.MINOR.PATCH`
tag on the exact reviewed revision. The release workflow validates the tag and
revision, runs the local evidence inventory, then waits for approval in the
protected GitHub `release` environment before publishing.

After approval, CI builds and publishes an immutable Linux amd64/arm64 image to
GHCR, binds the registry digest to release evidence, signs the digest and trust
manifest with GitHub OIDC, produces GitHub provenance, and verifies the
retained evidence, signatures, attestation, and published platforms. A failed
or existing tag is not corrected in place: use a new version tag.

The workflow's retained artifacts include the release-evidence contract and
release-trust contract. Treat their timestamped CI results as the release
record; a local gate only validates policy and candidate consistency.

Run this local policy check before requesting release approval:

```sh
make ci-release-policy-gate
```

It parses workflows and tests policy mutations without contacting GitHub or
GHCR. It neither creates a tag nor publishes, signs, uploads, or creates a
GitHub Release page. Create the release page separately after CI has succeeded,
using the verified tag, notes, and appropriate downloadable assets.
