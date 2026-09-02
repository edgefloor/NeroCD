#!/usr/bin/env bash
# Mutation tests keep the static policy meaningful without executing CI or
# touching a GitHub/registry endpoint.
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
policy="$root/scripts/ci-release-policy-gate.sh"
tmp=$(mktemp -d /tmp/nerocd-ci-release-policy.XXXXXXXX)
cleanup() { rm -rf -- "$tmp"; }
trap cleanup EXIT HUP INT TERM

candidate() {
  local name=$1
  mkdir -p "$tmp/$name/.github/workflows"
  cp "$root/.github/workflows/check.yml" "$tmp/$name/.github/workflows/check.yml"
  cp "$root/.github/workflows/release.yml" "$tmp/$name/.github/workflows/release.yml"
}
expect_reject() {
  local name=$1
  if "$policy" "$tmp/$name" >/dev/null 2>&1; then
    printf 'ci-release-policy mutation unexpectedly passed: %s\n' "$name" >&2
    exit 1
  fi
}

candidate mutable-action
perl -0pi -e 's#actions/checkout\@11bd71901bbe5b1630ceea73d27597364c9af683#actions/checkout\@v4#' "$tmp/mutable-action/.github/workflows/check.yml"
expect_reject mutable-action

candidate unpinned-service
sed -i.bak 's#postgres:17\.6-alpine@sha256:[0-9a-f]*#postgres:17-alpine#' "$tmp/unpinned-service/.github/workflows/check.yml"
rm -f "$tmp/unpinned-service/.github/workflows/check.yml.bak"
expect_reject unpinned-service

candidate missing-evidence
sed -i.bak '/make release-evidence-gate/d' "$tmp/missing-evidence/.github/workflows/release.yml"
rm -f "$tmp/missing-evidence/.github/workflows/release.yml.bak"
expect_reject missing-evidence

candidate missing-ripgrep
sed -i.bak '/sudo apt-get install --yes ripgrep/d' "$tmp/missing-ripgrep/.github/workflows/release.yml"
rm -f "$tmp/missing-ripgrep/.github/workflows/release.yml.bak"
expect_reject missing-ripgrep

candidate no-environment
perl -0pi -e 's/\n    environment:\n      name: release//' "$tmp/no-environment/.github/workflows/release.yml"
expect_reject no-environment

candidate direct-push
printf '\n      - run: docker push ghcr.io/example/forbidden\n' >>"$tmp/direct-push/.github/workflows/release.yml"
expect_reject direct-push

candidate missing-download
perl -0pi -e 's/^.*actions\/download-artifact.*\n//mg' "$tmp/missing-download/.github/workflows/release.yml"
expect_reject missing-download

candidate missing-evidence-hash
perl -0pi -e 's/evidence\.sha256/evidence-record/g' "$tmp/missing-evidence-hash/.github/workflows/release.yml"
expect_reject missing-evidence-hash

candidate missing-blob-bundle-check
perl -0pi -e 's/^.*cosign verify-blob.*\n//mg' "$tmp/missing-blob-bundle-check/.github/workflows/release.yml"
expect_reject missing-blob-bundle-check

candidate wrong-cosign-version
perl -0pi -e 's/cosign-release: v3\.0\.6/cosign-release: v3.0.5/g' "$tmp/wrong-cosign-version/.github/workflows/release.yml"
expect_reject wrong-cosign-version

candidate missing-cosign-flag-preflight
perl -0pi -e 's/^.*require_cosign_flag verify-blob --new-bundle-format.*\n//mg' "$tmp/missing-cosign-flag-preflight/.github/workflows/release.yml"
expect_reject missing-cosign-flag-preflight

candidate missing-publish-cosign-preflight
perl -0pi -e 's/(^  publish:.*?)(^      - name: Preflight pinned Cosign CLI contract\n.*?)(?=^      - name: Set up Buildx)/$1/ms' "$tmp/missing-publish-cosign-preflight/.github/workflows/release.yml"
expect_reject missing-publish-cosign-preflight

candidate unsupported-image-bundle-verify
printf '\n      - name: Unsupported retained image-bundle verification\n        run: cosign verify --bundle release-trust/image.bundle "$IMAGE@$DIGEST"\n' >>"$tmp/unsupported-image-bundle-verify/.github/workflows/release.yml"
expect_reject unsupported-image-bundle-verify

candidate missing-trust-binding
perl -0pi -e 's/^.*verify-release-trust\.sh.*\n//mg' "$tmp/missing-trust-binding/.github/workflows/release.yml"
expect_reject missing-trust-binding

candidate missing-registry-digest
perl -0pi -e 's/Capture exact registry digest and reject tag\/digest divergence/capture digest/' "$tmp/missing-registry-digest/.github/workflows/release.yml"
expect_reject missing-registry-digest

printf 'ci-release-policy mutation PASS\n'
