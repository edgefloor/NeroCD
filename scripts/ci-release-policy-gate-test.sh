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
  mkdir -p "$tmp/$name/scripts"
  cp "$root/.github/workflows/check.yml" "$tmp/$name/.github/workflows/check.yml"
  cp "$root/.github/workflows/release.yml" "$tmp/$name/.github/workflows/release.yml"
  cp "$root/Makefile" "$tmp/$name/Makefile"
  cp "$root/scripts/observability-gate.sh" "$tmp/$name/scripts/observability-gate.sh"
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

candidate tag-push-check
perl -0pi -e "s/  push:\n    branches:\n      - '\*\*'/  push:/" "$tmp/tag-push-check/.github/workflows/check.yml"
expect_reject tag-push-check

candidate unsafe-cache-path
perl -0pi -e 's#            \.cache/go-build#            web/dist#' "$tmp/unsafe-cache-path/.github/workflows/check.yml"
expect_reject unsafe-cache-path

candidate missing-cache-pin
perl -0pi -e 's#actions/cache\@27d5ce7f107fe9357f9df03efb73ab90386fccae#actions/cache\@v5#' "$tmp/missing-cache-pin/.github/workflows/check.yml"
expect_reject missing-cache-pin

candidate isolated-release-cache
perl -0pi -e 's/nerocd-toolchain-/isolated-release-/' "$tmp/isolated-release-cache/.github/workflows/release.yml"
expect_reject isolated-release-cache

candidate non-frozen-install
perl -0pi -e 's/bun install --frozen-lockfile/bun install/' "$tmp/non-frozen-install/.github/workflows/check.yml"
expect_reject non-frozen-install

candidate destructive-clean
perl -0pi -e 's#rm -rf -- "\$\(BIN_DIR\)"#rm -rf -- "$(GOCACHE_DIR)" "$(BIN_DIR)"#' "$tmp/destructive-clean/Makefile"
expect_reject destructive-clean

candidate parallel-runtime-gates-jobs-2
sed -i.bak 's/--jobs=1 runtime-fencing-gate/--jobs=2 runtime-fencing-gate/' "$tmp/parallel-runtime-gates-jobs-2/Makefile"
rm -f "$tmp/parallel-runtime-gates-jobs-2/Makefile.bak"
expect_reject parallel-runtime-gates-jobs-2

candidate parallel-runtime-gates-jobs-4
sed -i.bak 's/--jobs=1 runtime-fencing-gate/--jobs=4 runtime-fencing-gate/' "$tmp/parallel-runtime-gates-jobs-4/Makefile"
rm -f "$tmp/parallel-runtime-gates-jobs-4/Makefile.bak"
expect_reject parallel-runtime-gates-jobs-4

candidate missing-concurrency-policy-test
sed -i.bak '/bash scripts\/release-evidence-concurrency-test.sh/d' "$tmp/missing-concurrency-policy-test/Makefile"
rm -f "$tmp/missing-concurrency-policy-test/Makefile.bak"
expect_reject missing-concurrency-policy-test

candidate missing-runtime-scheduling-test
sed -i.bak '/bash scripts\/release-runtime-scheduling-test.sh/d' "$tmp/missing-runtime-scheduling-test/Makefile"
rm -f "$tmp/missing-runtime-scheduling-test/Makefile.bak"
expect_reject missing-runtime-scheduling-test

candidate missing-check-policy
sed -i.bak '/^check:/,/^clean:/s/ci-release-policy-gate //' "$tmp/missing-check-policy/Makefile"
rm -f "$tmp/missing-check-policy/Makefile.bak"
expect_reject missing-check-policy

candidate duplicate-release-policy
sed -i.bak '/^release-evidence-accepted-gates:/,/^release-evidence-gate:/s/ci-release-policy-gate/ci-release-policy-gate ci-release-policy-gate/' "$tmp/duplicate-release-policy/Makefile"
rm -f "$tmp/duplicate-release-policy/Makefile.bak"
expect_reject duplicate-release-policy

candidate missing-observability-dependency
sed -i.bak 's/^observability-gate: runtime-compose-gate backup-restore-gate$/observability-gate:/' "$tmp/missing-observability-dependency/Makefile"
rm -f "$tmp/missing-observability-dependency/Makefile.bak"
expect_reject missing-observability-dependency

candidate rerun-observability-prerequisite
perl -0pi -e 's#(GOCACHE=.*go test.*\n)#$1bash acceptance/runtime-compose/gate.sh\n#' "$tmp/rerun-observability-prerequisite/scripts/observability-gate.sh"
expect_reject rerun-observability-prerequisite

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
