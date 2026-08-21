#!/usr/bin/env bash
# Verify the signed release-trust artifact's deterministic, non-secret binding
# to the downloaded local evidence and the published immutable image digest.
set -Eeuo pipefail

evidence=${1:?usage: verify-release-trust.sh EVIDENCE TRUST REPOSITORY TAG REVISION IMAGE DIGEST}
trust=${2:?usage: verify-release-trust.sh EVIDENCE TRUST REPOSITORY TAG REVISION IMAGE DIGEST}
repository=${3:?usage: verify-release-trust.sh EVIDENCE TRUST REPOSITORY TAG REVISION IMAGE DIGEST}
tag=${4:?usage: verify-release-trust.sh EVIDENCE TRUST REPOSITORY TAG REVISION IMAGE DIGEST}
revision=${5:?usage: verify-release-trust.sh EVIDENCE TRUST REPOSITORY TAG REVISION IMAGE DIGEST}
image=${6:?usage: verify-release-trust.sh EVIDENCE TRUST REPOSITORY TAG REVISION IMAGE DIGEST}
digest=${7:?usage: verify-release-trust.sh EVIDENCE TRUST REPOSITORY TAG REVISION IMAGE DIGEST}
[[ "$revision" =~ ^[0-9a-f]{40}$ && "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo 'release-trust-verify: invalid revision or digest' >&2; exit 2; }
[[ -d "$evidence" && -d "$trust" ]] || { echo 'release-trust-verify: artifact directory is missing' >&2; exit 1; }
fail() { printf 'release-trust-verify: %s\n' "$*" >&2; exit 1; }
if command -v sha256sum >/dev/null; then
  hash() { sha256sum "$1" | awk '{print $1}'; }
  verify() { sha256sum -c "$1" >/dev/null; }
else
  hash() { shasum -a 256 "$1" | awk '{print $1}'; }
  verify() { shasum -a 256 -c "$1" >/dev/null; }
fi

trust_expected=(github-attestation.json image-digest.txt image.bundle release-trust.bundle release-trust.json trust.sha256)
actual=()
while IFS= read -r file; do actual+=("$file"); done < <(find "$trust" -maxdepth 1 -type f -print | sed "s#^$trust/##" | LC_ALL=C sort)
[[ "${actual[*]}" == "${trust_expected[*]}" ]] || fail 'release-trust file set is not exact'
for file in "${trust_expected[@]}"; do [[ -f "$trust/$file" && ! -L "$trust/$file" ]] || fail "missing or unsafe trust file: $file"; done
for file in github-attestation.json image.bundle image-digest.txt release-trust.bundle release-trust.json; do
  [[ $(awk -v expected="$file" '$2 == expected { count += 1 } END { print count + 0 }' "$trust/trust.sha256") -eq 1 ]] || fail "missing or duplicate trust checksum entry: $file"
done
[[ $(wc -l <"$trust/trust.sha256" | tr -d ' ') -eq 5 ]] || fail 'release-trust checksum record has unexpected entries'
(
  cd "$trust"
  verify trust.sha256
) || fail 'release-trust checksums do not verify'

canonical=$(mktemp)
trap 'rm -f -- "$canonical"' EXIT HUP INT TERM
jq -S . "$trust/release-trust.json" >"$canonical"
cmp -s "$canonical" "$trust/release-trust.json" || fail 'release-trust manifest is not canonical JSON'
jq -e --arg repository "$repository" --arg tag "$tag" --arg revision "$revision" --arg image "$image" --arg digest "$digest" '
  .schema == "nerocd.release-trust/v1" and .repository == $repository and .tag == $tag and
  .revision == $revision and .image == {reference:$image, digest:$digest} and
  (.evidence | type == "object" and length == 8)
' "$trust/release-trust.json" >/dev/null || fail 'release-trust manifest does not bind expected release identity'

for file in checksums.txt evidence.sha256 manifest.json nerocd-linux-amd64 nerocd-linux-arm64 nerocd.oci.tar provenance.intoto.json sbom.cdx.json; do
  expected=$(jq -er --arg file "$file" '.evidence[$file]' "$trust/release-trust.json") || fail "missing evidence hash: $file"
  actual=$(hash "$evidence/$file")
  [[ "$expected" =~ ^[0-9a-f]{64}$ && "$expected" == "$actual" ]] || fail "release-trust evidence hash mismatch: $file"
done
jq -e --arg repository "$repository" --arg revision "$revision" --arg image "$image" --arg digest "$digest" '
  .schema == "nerocd.github-attestation-metadata/v1" and .repository == $repository and
  .revision == $revision and .subject == {name:$image, digest:$digest}
' "$trust/github-attestation.json" >/dev/null || fail 'attestation metadata does not bind exact image digest'
[[ $(<"$trust/image-digest.txt") == "$image@$digest" ]] || fail 'retained image digest record does not bind exact published image'
printf 'release-trust-verify PASS image=%s\n' "$image@$digest"
