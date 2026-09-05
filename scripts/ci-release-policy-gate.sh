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
makefile="$root/Makefile"
observability_gate="$root/scripts/observability-gate.sh"
fail() { printf 'ci-release-policy: %s\n' "$*" >&2; exit 1; }
[[ -f "$check" && -f "$release" && -f "$makefile" && -f "$observability_gate" ]] || fail 'workflows, Makefile, and observability gate are required'

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
check_triggers=$(sed -n '/^on:/,/^permissions:/p' "$check")
printf '%s\n' "$check_triggers" | rg -q '^  push:$' || fail 'check workflow must run on pushes'
printf '%s\n' "$check_triggers" | rg -q "^      - '\*\*'\$" || fail 'check workflow must explicitly include all branches'
! printf '%s\n' "$check_triggers" | rg -q '^    tags:' || fail 'check workflow must exclude tag pushes'
printf '%s\n' "$check_triggers" | rg -q '^  pull_request:$' || fail 'check workflow must run on pull requests'

cache_action='actions/cache@27d5ce7f107fe9357f9df03efb73ab90386fccae'
expected_cache_paths=$(printf '%s\n' '.cache/go-build' '~/go/pkg/mod' '~/.bun/install/cache' '~/.cache/ms-playwright')
require_safe_cache() {
  local workflow_name=$1
  local workflow=$2
  local cache_block cache_paths
  [[ $(rg -Fc "$cache_action" "$workflow") -eq 1 ]] || fail "$workflow_name must use exactly one pinned actions/cache v5.0.5 action"
  cache_block=$(sed -n "/uses: actions\\/cache@/,/^[[:space:]]*key:/p" "$workflow")
  cache_paths=$(printf '%s\n' "$cache_block" | sed -n '/^[[:space:]]*path: |$/,/^[[:space:]]*key:/p' | sed '1d;$d;s/^[[:space:]]*//')
  [[ "$cache_paths" == "$expected_cache_paths" ]] || fail "$workflow_name cache paths must be the exact safe Go, Bun, and Playwright allowlist"
  printf '%s\n' "$cache_block" | rg -Fq '${{ runner.os }}-${{ runner.arch }}' || fail "$workflow_name cache key must include runner platform"
  printf '%s\n' "$cache_block" | rg -Fq "hashFiles('go.mod', 'go.sum', 'web/app/bun.lock')" || fail "$workflow_name cache key must bind locked Go and Bun inputs"
  rg -Fq "key: nerocd-toolchain-\${{ runner.os }}-\${{ runner.arch }}-go-bun-playwright-\${{ hashFiles('go.mod', 'go.sum', 'web/app/bun.lock') }}" "$workflow" || fail "$workflow_name must share the lock-bound tool cache namespace"
  rg -Fq 'nerocd-toolchain-${{ runner.os }}-${{ runner.arch }}-go-bun-playwright-' "$workflow" || fail "$workflow_name must use the bounded shared tool-cache restore prefix"
  ! printf '%s\n' "$cache_block" | rg -qi 'node_modules|(^|/)(bin|web/dist|artifacts|release-evidence|playwright-report|test-results|reports?|credentials)(/|$)|docker' || fail "$workflow_name cache includes an unsafe generated, credential, report, or Docker path"
  while IFS= read -r install; do
    [[ "$install" == *'bun install --frozen-lockfile'* ]] || fail "$workflow_name contains a non-frozen Bun install"
  done < <(rg -N 'bun install' "$workflow")
}
require_safe_cache check "$check"
require_safe_cache release "$release"

rg -q "github.repository_owner == 'edgefloor'" "$release" || fail 'release must be owner fenced'
rg -q '^      name: release$' "$release" || fail 'publish must require the protected release environment'
rg -q 'make release-evidence-gate' "$release" || fail 'release evidence is not a prerequisite'
evidence_job=$(sed -n '/^  evidence:/,/^  publish:/p' "$release")
publish_job=$(sed -n '/^  publish:/,/^  verify:/p' "$release")
verify_job=$(sed -n '/^  verify:/,$p' "$release")
[[ $(printf '%s\n' "$evidence_job" | rg -Fc "$cache_action") -eq 1 ]] || fail 'release caches must be confined to the pre-approval evidence job'
printf '%s\n' "$evidence_job" | rg -q 'sudo apt-get install --yes ripgrep' || fail 'release evidence must install ripgrep'
evidence_ripgrep_line=$(printf '%s\n' "$evidence_job" | rg -n 'sudo apt-get install --yes ripgrep' | head -n1 | cut -d: -f1)
evidence_gate_line=$(printf '%s\n' "$evidence_job" | rg -n 'make release-evidence-gate' | head -n1 | cut -d: -f1)
(( evidence_ripgrep_line < evidence_gate_line )) || fail 'release evidence must install ripgrep before its gate'
printf '%s\n' "$publish_job" | rg -Fq 'needs: [validate, evidence]' || fail 'publish must depend on pre-approval evidence'
! printf '%s\n' "$evidence_job" | rg -q '^    environment:' || fail 'evidence and Cosign preflight must run before release-environment approval'

cosign_installer='sigstore/cosign-installer@6f9f17788090df1f26f669e9d70d6ae9567deba6'
[[ $(rg -Fc "$cosign_installer" "$release") -eq 3 ]] || fail 'evidence, publish, and verify must use the pinned Cosign installer v4.1.2 commit'
[[ $(rg -c '^[[:space:]]+cosign-release: v3\.0\.6$' "$release") -eq 3 ]] || fail 'every Cosign install must explicitly select v3.0.6'

require_cosign_contract() {
  local job_name=$1
  local job=$2
  [[ $(printf '%s\n' "$job" | rg -Fc "$cosign_installer") -eq 1 ]] || fail "$job_name must contain exactly one pinned Cosign installer"
  [[ $(printf '%s\n' "$job" | rg -c '^[[:space:]]+cosign-release: v3\.0\.6$') -eq 1 ]] || fail "$job_name must explicitly install Cosign v3.0.6"
  printf '%s\n' "$job" | rg -Uq "uses: $cosign_installer[^\\n]*\\n        with:\\n          cosign-release: v3\\.0\\.6" || fail "$job_name must bind Cosign v3.0.6 to the pinned installer block"
  [[ $(printf '%s\n' "$job" | rg -Fc 'Preflight pinned Cosign CLI contract') -eq 1 ]] || fail "$job_name must contain exactly one Cosign CLI preflight"
  printf '%s\n' "$job" | rg -q '^[[:space:]]+cosign_version=\$\(cosign version 2>&1\)$' || fail "$job_name preflight must inspect Cosign version"
  printf '%s\n' "$job" | rg -Fq "printf '%s\\n' \"\$cosign_version\" | grep -Eq '^GitVersion:[[:space:]]+v3\\.0\\.6$'" || fail "$job_name preflight must fail closed on GitVersion v3.0.6"
  local assertion
  for assertion in \
    'require_cosign_flag sign --bundle' \
    'require_cosign_flag sign --new-bundle-format' \
    'require_cosign_flag sign-blob --bundle' \
    'require_cosign_flag sign-blob --new-bundle-format' \
    'require_cosign_flag verify --new-bundle-format' \
    'require_cosign_flag verify-blob --bundle' \
    'require_cosign_flag verify-blob --new-bundle-format'
  do
    printf '%s\n' "$job" | rg -q "^[[:space:]]+$assertion$" || fail "$job_name preflight lacks required flag assertion: $assertion"
  done
}
require_cosign_contract evidence "$evidence_job"
require_cosign_contract publish "$publish_job"
require_cosign_contract verify "$verify_job"

first_job_line() {
  local job=$1
  local marker=$2
  printf '%s\n' "$job" | rg -n -F "$marker" | head -n1 | cut -d: -f1
}
evidence_cosign_line=$(first_job_line "$evidence_job" "$cosign_installer")
evidence_preflight_line=$(first_job_line "$evidence_job" 'Preflight pinned Cosign CLI contract')
(( evidence_cosign_line < evidence_preflight_line && evidence_preflight_line < evidence_gate_line )) || fail 'evidence Cosign preflight must run before the pre-approval evidence gate'

publish_cosign_line=$(first_job_line "$publish_job" "$cosign_installer")
publish_preflight_line=$(first_job_line "$publish_job" 'Preflight pinned Cosign CLI contract')
publish_buildx_line=$(first_job_line "$publish_job" 'docker/setup-buildx-action@')
publish_login_line=$(first_job_line "$publish_job" 'docker/login-action@')
publish_build_line=$(first_job_line "$publish_job" 'Build and publish immutable OCI index')
(( publish_cosign_line < publish_preflight_line && publish_preflight_line < publish_buildx_line && publish_preflight_line < publish_login_line && publish_preflight_line < publish_build_line )) || fail 'publish Cosign preflight must precede Buildx, login, and registry publication'

verify_cosign_line=$(first_job_line "$verify_job" "$cosign_installer")
verify_preflight_line=$(first_job_line "$verify_job" 'Preflight pinned Cosign CLI contract')
verify_image_line=$(first_job_line "$verify_job" 'cosign verify \')
verify_blob_line=$(first_job_line "$verify_job" 'cosign verify-blob release-trust/release-trust.json')
(( verify_cosign_line < verify_preflight_line && verify_preflight_line < verify_image_line && verify_preflight_line < verify_blob_line )) || fail 'verify Cosign preflight must precede image and trust-manifest verification'

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
rg -Fq 'cosign sign --yes --new-bundle-format=true --bundle artifacts/release-trust/image.bundle "$IMAGE@$DIGEST"' "$release" || fail 'release lacks exact-digest keyless signing with the retained v3 bundle format'
rg -Fq 'cosign sign-blob --yes --new-bundle-format=true --bundle artifacts/release-trust/release-trust.bundle artifacts/release-trust/release-trust.json' "$release" || fail 'release-trust manifest lacks a retained v3 bundle over its exact bytes'
rg -q 'sha256sum github-attestation\.json image\.bundle image-digest\.txt release-trust\.bundle release-trust\.json >trust\.sha256' "$release" || fail 'retained image bundle must remain in the checksummed trust artifact'
rg -q 'actions/attest-build-provenance@[0-9a-f]{40}' "$release" || fail 'release lacks pinned provenance attestation'
rg -q 'docker buildx imagetools inspect --raw' "$release" || fail 'release lacks immutable digest platform verification'
rg -q -- '--certificate-identity "https://github.com/edgefloor/NeroCD/.github/workflows/release.yml@refs/tags/\$TAG"' "$release" || fail 'release signature verification is not bound to this workflow identity'
rg -q 'certificate-oidc-issuer https://token.actions.githubusercontent.com' "$release" || fail 'release lacks OIDC signature verification'
rg -q 'gh attestation verify "oci://\$IMAGE@\$DIGEST"' "$release" || fail 'release lacks GitHub OCI provenance verification'
image_verify_command=$(printf '%s\n' "$verify_job" | sed -n '/^[[:space:]]*cosign verify \\$/,/"\$IMAGE@\$DIGEST"/p')
trust_verify_command=$(printf '%s\n' "$verify_job" | sed -n '/^[[:space:]]*cosign verify-blob release-trust\/release-trust.json \\$/,/certificate-oidc-issuer/p')
printf '%s\n' "$image_verify_command" | rg -q '^[[:space:]]+cosign verify \\$' || fail 'verify lacks registry-backed exact-digest image verification'
printf '%s\n' "$image_verify_command" | rg -q '^[[:space:]]+--new-bundle-format=true \\$' || fail 'image verification must explicitly select the v3 bundle format'
printf '%s\n' "$image_verify_command" | rg -Fq '"$IMAGE@$DIGEST"' || fail 'image verification must use the exact immutable registry digest'
printf '%s\n' "$trust_verify_command" | rg -q 'cosign verify-blob release-trust/release-trust.json' || fail 'verify does not validate the retained release-trust bundle bytes'
printf '%s\n' "$trust_verify_command" | rg -q '^[[:space:]]+--bundle release-trust/release-trust\.bundle \\$' || fail 'trust-manifest verification must consume its retained bundle'
printf '%s\n' "$trust_verify_command" | rg -q '^[[:space:]]+--new-bundle-format=true \\$' || fail 'trust-manifest verification must explicitly select the v3 bundle format'
if awk '
  /cosign verify([[:space:]\\]|$)/ { in_verify=1 }
  in_verify && /--bundle([=[:space:]]|$)/ { found=1 }
  in_verify && $0 !~ /\\[[:space:]]*$/ { in_verify=0 }
  END { exit found ? 0 : 1 }
' "$release"; then
  fail 'cosign verify --bundle is unsupported; retained image.bundle is checksummed audit evidence only'
fi
! rg -q 'docker[[:space:]]+push' "$workflows"/*.yml || fail 'direct docker push is forbidden; use pinned build-push-action'
! rg -q 'pull_request_target|permissions:[[:space:]]+write-all|@v[0-9]|@main|@master|:latest' "$workflows"/*.yml || fail 'workflow includes a mutable or privileged policy escape'

accepted_gates=$(sed -n '/^release-evidence-accepted-gates:/,/^release-evidence-gate:/p' "$makefile")
core_line=$(printf '%s\n' "$accepted_gates" | rg -F '$(MAKE) --jobs=1 ci-release-policy-gate' || true)
runtime_line=$(printf '%s\n' "$accepted_gates" | rg -F '$(MAKE) --jobs=1 runtime-fencing-gate' || true)
[[ -n "$core_line" ]] || fail 'accepted core gates must remain explicitly serial'
[[ -n "$runtime_line" ]] || fail 'accepted runtime gates must remain explicitly serial'
for gate in ci-release-policy-gate test web-test build browser-smoke web-policy contract check-generated docker-build identity-artifact-gate production-profile-gate; do
  [[ " $core_line " == *" $gate "* ]] || fail "serial accepted core inventory is missing $gate"
done
[[ $(awk '{ for (field = 1; field <= NF; field++) if ($field == "ci-release-policy-gate") count++ } END { print count + 0 }' <<<"$core_line") -eq 1 ]] || fail 'serial accepted core inventory must contain ci-release-policy-gate exactly once'
for gate in runtime-fencing-gate runtime-spool-gate runtime-enrollment-gate runtime-provenance-gate runtime-compose-gate runtime-web-operator-gate backup-restore-gate observability-gate; do
  [[ " $runtime_line " == *" $gate "* ]] || fail "serial accepted runtime inventory is missing $gate"
  [[ $(awk -v gate="$gate" '{ for (field = 1; field <= NF; field++) if ($field == gate) count++ } END { print count + 0 }' <<<"$runtime_line") -eq 1 ]] || fail "serial accepted runtime inventory must contain $gate exactly once"
done
rg -q '^observability-gate: runtime-compose-gate backup-restore-gate$' "$makefile" || fail 'observability gate must depend on Compose and backup evidence producers'
! rg -q '^[[:space:]]*bash (acceptance/runtime-compose/gate\.sh|scripts/backup-restore-gate\.sh)' "$observability_gate" || fail 'observability gate must reuse prerequisite evidence instead of rerunning runtime gates'
rg -q '^compose_evidence=/tmp/nerocd-compose-runtime\.txt$' "$observability_gate" || fail 'observability gate must reuse Compose evidence'
rg -q '^backup_evidence=/tmp/nerocd-backup-restore\.txt$' "$observability_gate" || fail 'observability gate must reuse backup evidence'
rg -q '^\[\[ -s "\$compose_evidence" && -s "\$backup_evidence" \]\]$' "$observability_gate" || fail 'observability gate must fail closed on missing prerequisite evidence'
clean_target=$(sed -n '/^clean:/,/^run:/p' "$makefile")
! printf '%s\n' "$clean_target" | rg -q 'GOCACHE_DIR|node_modules' || fail 'clean must preserve reusable Go and frozen-install dependency caches'
for generated in '$(BIN_DIR)' 'playwright-report' 'test-results' '$(WEB_DIST_DIR)' 'artifacts/'; do
  printf '%s\n' "$clean_target" | rg -Fq "$generated" || fail "clean must still remove generated output: $generated"
done
policy_target=$(sed -n '/^ci-release-policy-gate:/,/^contract:/p' "$makefile")
[[ $(printf '%s\n' "$policy_target" | rg -Fc 'bash scripts/release-evidence-concurrency-test.sh') -eq 1 ]] || fail 'CI release policy target must run the evidence concurrency mutation exactly once'
[[ $(printf '%s\n' "$policy_target" | rg -Fc 'bash scripts/release-runtime-scheduling-test.sh') -eq 1 ]] || fail 'CI release policy target must run the runtime scheduling test exactly once'
check_target=$(sed -n '/^check:/,/^clean:/p' "$makefile")
[[ $(awk '{ for (field = 1; field <= NF; field++) if ($field == "ci-release-policy-gate") count++ } END { print count + 0 }' <<<"$check_target") -eq 1 ]] || fail 'ordinary make check must run ci-release-policy-gate exactly once'
printf 'ci-release-policy PASS workflows=%s\n' "$workflows"
