#!/usr/bin/env bash
# Deterministic local negative tests for the cross-job release artifact
# verifiers. They use synthetic bytes only and never invoke a registry, OIDC,
# GitHub, Cosign, Docker, or release workflow.
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
evidence_verify="$root/scripts/verify-release-evidence.sh"
trust_verify="$root/scripts/verify-release-trust.sh"
tmp=$(mktemp -d /tmp/nerocd-release-artifact-verify.XXXXXXXX)
cleanup() { rm -rf -- "$tmp"; }
trap cleanup EXIT HUP INT TERM
revision=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
digest=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
repo=edgefloor/NeroCD
tag=v1.2.3
image=ghcr.io/edgefloor/nerocd

hash() { shasum -a 256 "$1" | awk '{print $1}'; }
evidence=$tmp/evidence
trust=$tmp/trust
write_checksums() {
  (
    cd "$evidence"
    for file in nerocd-linux-amd64 nerocd-linux-arm64 sbom.cdx.json nerocd.oci.tar; do shasum -a 256 "$file"; done >checksums.txt
    for file in nerocd-linux-amd64 nerocd-linux-arm64 sbom.cdx.json nerocd.oci.tar checksums.txt manifest.json provenance.intoto.json; do shasum -a 256 "$file"; done >evidence.sha256
  )
}
write_outer_evidence() {
  (
    cd "$evidence"
    for file in nerocd-linux-amd64 nerocd-linux-arm64 sbom.cdx.json nerocd.oci.tar checksums.txt manifest.json provenance.intoto.json; do shasum -a 256 "$file"; done >evidence.sha256
  )
}
write_trust_checksums() {
  (
    cd "$trust"
    for file in github-attestation.json image.bundle image-digest.txt release-trust.bundle release-trust.json; do shasum -a 256 "$file"; done >trust.sha256
  )
}
setup() {
  rm -rf -- "$evidence" "$trust"
  mkdir -p "$evidence" "$trust"
  printf amd64 >"$evidence/nerocd-linux-amd64"
  printf arm64 >"$evidence/nerocd-linux-arm64"
  printf oci >"$evidence/nerocd.oci.tar"
  jq -n '{bomFormat:"CycloneDX",specVersion:"1.5",metadata:{component:{type:"application",name:"NeroCD"}},components:[]}' >"$evidence/sbom.cdx.json"
  jq -S -n --arg revision "$revision" --arg binary "$(hash "$evidence/nerocd-linux-amd64")" --arg sbom "$(hash "$evidence/sbom.cdx.json")" \
    '{schema:"nerocd.local.release-manifest/v1",trust:"UNTRUSTED_LOCAL_NO_SIGNATURE",revision:$revision,artifacts:{binary_linux_amd64_sha256:$binary,sbom_sha256:$sbom}}' >"$evidence/manifest.json"
  jq -S -n --arg revision "$revision" --arg binary "$(hash "$evidence/nerocd-linux-amd64")" \
    '{_type:"https://in-toto.io/Statement/v1",subject:[{name:"nerocd-linux-amd64",digest:{sha256:$binary}}],predicateType:"https://slsa.dev/provenance/v1",predicate:{buildDefinition:{externalParameters:{revision:$revision}}}}' >"$evidence/provenance.intoto.json"
  write_checksums
  "$evidence_verify" "$evidence" "$revision" >/dev/null
  {
    for file in checksums.txt evidence.sha256 manifest.json nerocd-linux-amd64 nerocd-linux-arm64 nerocd.oci.tar provenance.intoto.json sbom.cdx.json; do
      printf '%s\t%s\n' "$file" "$(hash "$evidence/$file")"
    done
  } | jq -Rn '[inputs | split("\t") | {key:.[0],value:.[1]}] | from_entries' >"$tmp/evidence.json"
  jq -S -n --arg repository "$repo" --arg tag "$tag" --arg revision "$revision" --arg image "$image" --arg digest "$digest" --slurpfile hashes "$tmp/evidence.json" \
    '{schema:"nerocd.release-trust/v1",repository:$repository,tag:$tag,revision:$revision,image:{reference:$image,digest:$digest},evidence:$hashes[0]}' >"$trust/release-trust.json"
  jq -S -n --arg repository "$repo" --arg revision "$revision" --arg image "$image" --arg digest "$digest" \
    '{schema:"nerocd.github-attestation-metadata/v1",repository:$repository,revision:$revision,subject:{name:$image,digest:$digest}}' >"$trust/github-attestation.json"
  printf image-bundle >"$trust/image.bundle"
  printf '%s@%s\n' "$image" "$digest" >"$trust/image-digest.txt"
  printf trust-bundle >"$trust/release-trust.bundle"
  write_trust_checksums
  "$trust_verify" "$evidence" "$trust" "$repo" "$tag" "$revision" "$image" "$digest" >/dev/null
}
expect_reject() {
  local name=$1 command=$2
  setup
  bash -c "$command" -- "$evidence" "$trust"
  if "$evidence_verify" "$evidence" "$revision" >/dev/null 2>&1 && "$trust_verify" "$evidence" "$trust" "$repo" "$tag" "$revision" "$image" "$digest" >/dev/null 2>&1; then
    printf 'release artifact verifier mutation unexpectedly passed: %s\n' "$name" >&2
    exit 1
  fi
}

expect_reject missing-download 'rm -f "$1/nerocd-linux-amd64"'
expect_reject corrupt-checksum 'printf x >>"$1/evidence.sha256"'
expect_reject malformed-sbom 'printf "{}\n" >"$1/sbom.cdx.json"; (cd "$1" && for f in nerocd-linux-amd64 nerocd-linux-arm64 sbom.cdx.json nerocd.oci.tar; do shasum -a 256 "$f"; done >checksums.txt); (cd "$1" && for f in nerocd-linux-amd64 nerocd-linux-arm64 sbom.cdx.json nerocd.oci.tar checksums.txt manifest.json provenance.intoto.json; do shasum -a 256 "$f"; done >evidence.sha256)'
expect_reject changed-manifest 'jq ".revision = \"cccccccccccccccccccccccccccccccccccccccc\"" "$1/manifest.json" >"$1/next" && mv "$1/next" "$1/manifest.json"; (cd "$1" && for f in nerocd-linux-amd64 nerocd-linux-arm64 sbom.cdx.json nerocd.oci.tar checksums.txt manifest.json provenance.intoto.json; do shasum -a 256 "$f"; done >evidence.sha256)'
expect_reject changed-subject 'jq ".subject[0].digest.sha256 = \"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\"" "$1/provenance.intoto.json" >"$1/next" && mv "$1/next" "$1/provenance.intoto.json"; (cd "$1" && for f in nerocd-linux-amd64 nerocd-linux-arm64 sbom.cdx.json nerocd.oci.tar checksums.txt manifest.json provenance.intoto.json; do shasum -a 256 "$f"; done >evidence.sha256)'
expect_reject changed-trust-digest 'jq ".image.digest = \"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\"" "$2/release-trust.json" | jq -S . >"$2/next" && mv "$2/next" "$2/release-trust.json"; (cd "$2" && for f in github-attestation.json image.bundle image-digest.txt release-trust.bundle release-trust.json; do shasum -a 256 "$f"; done >trust.sha256)'
expect_reject missing-bundle 'rm -f "$2/release-trust.bundle"'
expect_reject corrupt-bundle 'printf x >>"$2/release-trust.bundle"'
printf 'release artifact verifier mutation PASS\n'
