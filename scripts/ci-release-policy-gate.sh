#!/usr/bin/env bash
# Static, fail-closed policy for the repository's unexecuted CI/release trust
# workflows. This command deliberately makes no GitHub, registry, tag, or
# package mutation; it validates the source files supplied as its sole input.
set -Eeuo pipefail

root=${1:-.}
root=$(cd "$root" && pwd)
workflows="$root/.github/workflows"
check="$workflows/check.yml"
release="$workflows/release.yml"
fail() { printf 'ci-release-policy: %s\n' "$*" >&2; exit 1; }
[[ -f "$check" && -f "$release" ]] || fail 'check.yml and release.yml are required'

# GitHub resolves an action reference as source code. A full immutable commit
# is mandatory for every action, including artifact and signing helpers.
while IFS= read -r use; do
  ref=${use#*uses: }
  ref=${ref%%[[:space:]#]*}
  [[ "$ref" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@[0-9a-f]{40}$ ]] || fail "mutable or malformed action reference: $ref"
done < <(rg -N '^[[:space:]]*uses:[[:space:]]+' "$workflows"/*.yml)

rg -q '^permissions:$' "$check" || fail 'check workflow must declare minimal permissions'
rg -q '^[[:space:]]+contents: read$' "$check" || fail 'check workflow must be contents-read only'
! rg -q '(^|[[:space:]])(write-all|id-token: write|packages: write|attestations: write)' "$check" || fail 'check workflow has release privileges'
rg -q 'persist-credentials: false' "$check" || fail 'check checkout must disable persisted credentials'
rg -q 'postgres:17\.6-alpine@sha256:[0-9a-f]{64}' "$check" || fail 'check PostgreSQL service must be digest pinned'

rg -q "github.repository_owner == 'edgefloor'" "$release" || fail 'release must be owner fenced'
rg -q '^      name: release$' "$release" || fail 'publish must require the protected release environment'
rg -q 'make release-evidence-gate' "$release" || fail 'release evidence is not a prerequisite'
[[ $(rg -c 'actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093' "$release") -ge 3 ]] || fail 'publish and verify must download named evidence/trust artifacts with a pinned action'
rg -q 'bash scripts/verify-release-evidence.sh release-evidence' "$release" || fail 'publish/verify do not recompute transferred evidence checksums'
rg -q 'bash scripts/verify-release-trust.sh release-evidence release-trust' "$release" || fail 'verify does not validate release-trust evidence binding'
rg -q 'evidence.sha256' "$release" || fail 'release-trust does not bind every evidence file including its checksum record'
rg -q '^      packages: write$' "$release" || fail 'publish must request package write only in its job'
rg -q '^      id-token: write$' "$release" || fail 'publish must request OIDC only in its job'
rg -q '^      attestations: write$' "$release" || fail 'publish must request attestation write only in its job'
rg -q 'git show-ref --verify --quiet' "$release" || fail 'release tag existence is not verified'
rg -q 'git cat-file -t' "$release" || fail 'release tag must be annotated'
rg -q '\[\[ "\$tag" =~ \^v\(' "$release" || fail 'release tag is not strict semantic version input'
rg -q 'release tag already exists; immutable publication refused' "$release" || fail 'release does not reject an existing registry tag'
rg -q 'platforms: linux/amd64,linux/arm64' "$release" || fail 'release must publish both supported architectures'
rg -q 'push: true' "$release" || fail 'release does not publish its OCI index'
rg -q 'Capture exact registry digest and reject tag/digest divergence' "$release" || fail 'publish does not capture and compare the registry digest'
rg -q 'registry_digest.*==.*BUILDX_DIGEST' "$release" || fail 'publish does not reject registry digest divergence'
rg -q 'cosign sign --yes --bundle' "$release" || fail 'release lacks keyless Cosign bundle signing'
rg -q 'cosign sign-blob --yes --bundle .*release-trust.bundle .*release-trust.json' "$release" || fail 'release-trust manifest has no retained verifiable bundle'
rg -q 'actions/attest-build-provenance@[0-9a-f]{40}' "$release" || fail 'release lacks pinned provenance attestation'
rg -q 'docker buildx imagetools inspect --raw' "$release" || fail 'release lacks immutable digest platform verification'
rg -q -- '--certificate-identity "https://github.com/edgefloor/NeroCD/.github/workflows/release.yml@refs/tags/\$TAG"' "$release" || fail 'release signature verification is not bound to this workflow identity'
rg -q 'certificate-oidc-issuer https://token.actions.githubusercontent.com' "$release" || fail 'release lacks OIDC signature verification'
rg -q 'gh attestation verify "oci://\$IMAGE@\$DIGEST"' "$release" || fail 'release lacks GitHub OCI provenance verification'
rg -q 'cosign verify-blob release-trust/release-trust.json' "$release" || fail 'verify does not validate the retained release-trust bundle bytes'
rg -q 'cosign verify --bundle release-trust/image.bundle' "$release" || fail 'verify does not validate the retained image bundle'
! rg -q 'docker[[:space:]]+push' "$workflows"/*.yml || fail 'direct docker push is forbidden; use pinned build-push-action'
! rg -q 'pull_request_target|permissions:[[:space:]]+write-all|@v[0-9]|@main|@master|:latest' "$workflows"/*.yml || fail 'workflow includes a mutable or privileged policy escape'
printf 'ci-release-policy PASS workflows=%s\n' "$workflows"
