#!/usr/bin/env bash
# Verify the deliberately local evidence artifact after GitHub Actions has
# transferred it between jobs. No registry, signer, or network operation is
# performed here; every value is checked against explicit caller authority.
set -Eeuo pipefail

dir=${1:?usage: verify-release-evidence.sh EVIDENCE_DIR REVISION}
revision=${2:?usage: verify-release-evidence.sh EVIDENCE_DIR REVISION}
[[ "$revision" =~ ^[0-9a-f]{40}$ ]] || { echo 'release-evidence-verify: invalid revision' >&2; exit 2; }
[[ -d "$dir" ]] || { echo 'release-evidence-verify: missing evidence directory' >&2; exit 1; }
cd "$dir"
fail() { printf 'release-evidence-verify: %s\n' "$*" >&2; exit 1; }
if command -v sha256sum >/dev/null; then
  hash() { sha256sum "$1" | awk '{print $1}'; }
  verify() { sha256sum -c "$1" >/dev/null; }
else
  hash() { shasum -a 256 "$1" | awk '{print $1}'; }
  verify() { shasum -a 256 -c "$1" >/dev/null; }
fi

expected=(checksums.txt evidence.sha256 manifest.json nerocd-linux-amd64 nerocd-linux-arm64 nerocd.oci.tar provenance.intoto.json sbom.cdx.json)
actual=()
while IFS= read -r file; do actual+=("$file"); done < <(find . -maxdepth 1 -type f -print | sed 's#^./##' | LC_ALL=C sort)
[[ "${actual[*]}" == "${expected[*]}" ]] || fail 'artifact file set is not exact'
for file in "${expected[@]}"; do
  [[ -f "$file" && ! -L "$file" ]] || fail "missing or unsafe evidence file: $file"
done

validate_checksum_record() {
  local record=$1 expected_names=$2 count=0 hash_value name
  while read -r hash_value name; do
    [[ "$hash_value" =~ ^[0-9a-f]{64}$ && "$name" =~ ^[A-Za-z0-9._-]+$ ]] || fail "malformed checksum entry in $record"
    [[ " $expected_names " == *" $name "* ]] || fail "unexpected checksum entry in $record"
    ((count += 1))
  done <"$record"
  local expected_count
  expected_count=$(wc -w <<<"$expected_names" | tr -d ' ')
  [[ "$count" -eq "$expected_count" ]] || fail "incomplete checksum record: $record"
  for name in $expected_names; do
    [[ $(awk -v expected="$name" '$2 == expected { count += 1 } END { print count + 0 }' "$record") -eq 1 ]] || fail "duplicate or missing checksum entry in $record: $name"
  done
}
validate_checksum_record checksums.txt 'nerocd-linux-amd64 nerocd-linux-arm64 sbom.cdx.json nerocd.oci.tar'
validate_checksum_record evidence.sha256 'nerocd-linux-amd64 nerocd-linux-arm64 sbom.cdx.json nerocd.oci.tar checksums.txt manifest.json provenance.intoto.json'
verify checksums.txt || fail 'artifact checksums do not verify'
verify evidence.sha256 || fail 'cross-job evidence checksums do not verify'

binary_hash=$(hash nerocd-linux-amd64)
sbom_hash=$(hash sbom.cdx.json)
jq -e '
  .bomFormat == "CycloneDX" and .specVersion == "1.5" and
  .metadata.component.type == "application" and .metadata.component.name == "NeroCD" and
  (.components | type == "array")
' sbom.cdx.json >/dev/null || fail 'CycloneDX document is malformed'
jq -e --arg revision "$revision" --arg binary "$binary_hash" --arg sbom "$sbom_hash" '
  .schema == "nerocd.local.release-manifest/v1" and
  .trust == "UNTRUSTED_LOCAL_NO_SIGNATURE" and
  .revision == $revision and
  .artifacts.binary_linux_amd64_sha256 == $binary and
  .artifacts.sbom_sha256 == $sbom
' manifest.json >/dev/null || fail 'local manifest does not bind this evidence and source revision'
jq -e --arg revision "$revision" --arg binary "$binary_hash" '
  ._type == "https://in-toto.io/Statement/v1" and
  .predicateType == "https://slsa.dev/provenance/v1" and
  .predicate.buildDefinition.externalParameters.revision == $revision and
  .subject == [{name:"nerocd-linux-amd64", digest:{sha256:$binary}}]
' provenance.intoto.json >/dev/null || fail 'local in-toto subject does not bind this binary and revision'
printf 'release-evidence-verify PASS revision=%s\n' "$revision"
