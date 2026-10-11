# Release trust

Start a release by dispatching `release.yml` from `main`, with the intended
version and the full 40-character SHA of `main` at dispatch time. Do not create
a tag first. The requested SHA must match the dispatch SHA so GitHub provenance,
the workflow source, and the artifact source identify the same commit. If `main`
advances before dispatch, validation stops; dispatch again with its new SHA.

```sh
gh workflow run release.yml --ref main \
  -f tag=v0.3.29 -f revision="$(git rev-parse HEAD)"
```

The workflow validates the input and commit ancestry, then runs the complete
release evidence inventory against that exact SHA. Every artifact job checks
out the validated SHA. Evidence uses the requested version rather than deriving
it from an existing tag. Validation failure leaves the version available.

After evidence passes, the protected GitHub `release` environment requires
approval. Publication verifies transferred evidence and the signing CLI, checks
GitHub releases (including draft assets) and GHCR, then creates an annotated tag
on the validated commit. An unpublished candidate tag may be replaced using an
explicit Git push lease; a concurrent tag change causes failure. Repository-wide
release concurrency serializes workflow attempts across versions.

A version becomes immutable when any of these exists:

- A published GitHub Release, including a prerelease.
- Any uploaded asset on a GitHub Release, including a draft.
- A GHCR image under that version.

A bare Git tag or empty draft does not publish a version. If an attempt fails
before publication, fix the commit and dispatch the same version with its new
SHA; the full validation and approval run again. If publication has begun, use a
new version for changed artifacts, even if signing or subsequent verification
failed. Do not delete published records or artifacts to make a version reusable.
An API error, missing access, malformed response, or registry lookup failure
stops publication; it never means the version is unused.

Versions must be `vMAJOR.MINOR.PATCH`, optionally followed by a SemVer prerelease
suffix. Build metadata (`+...`) is excluded because the version is also an OCI
tag. Numeric identifiers must not have leading zeroes.

CI publishes Linux amd64/arm64 images, binds the registry digest to evidence,
signs the digest and trust manifest with GitHub OIDC, produces GitHub provenance,
and verifies retained evidence, signatures, attestation, and published platforms.
New signatures use the exact workflow identity
`https://github.com/edgefloor/NeroCD/.github/workflows/release.yml@refs/heads/main`.
Older releases retain their original tag-based identity and verification record.

Only the protected publish job has repository contents write, package write,
OIDC, and attestation write permissions. Configure the release environment to
allow `main` with required reviewers, and configure tag rules to permit this
workflow's token to create and update unpublished candidate tags. Published
versions must remain protected. If existing tag rules reject the workflow token,
publication stops; this change does not modify repository settings or bypass
rules automatically. At the time of this change, the repository's environment
allows only `v*` tag deployments and its tag rules allow only the owner to
create or update tags. Rollout therefore requires allowing `main` deployments
and configuring an authorized workflow tagging identity before the first run.
Do not run external/manual publishers concurrently with
this workflow: GitHub and GHCR do not provide a shared publication transaction.

Run the local checks before requesting release approval:

```sh
make ci-release-policy-gate
```

These checks cover workflow policy mutations, publication boundaries, failed
lookups, and tag races against temporary local repositories. They do not contact
GitHub or GHCR. Hosted CI is still required to validate permissions, environment
approval, signing, and publication. Retained CI artifacts form the release
record. Create the GitHub Release page separately after CI succeeds, using the
verified tag, notes, and downloadable assets. Complete a missing page from the
original verified artifacts; never rebuild or replace a published version.
