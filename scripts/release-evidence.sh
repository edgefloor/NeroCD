#!/usr/bin/env bash
# Local-only release evidence. This intentionally has no --push, registry,
# signer, or upload path. A real invocation refuses a dirty checkout; the
# synthetic mode copies the current source into a disposable committed repo so
# contributors can validate work in progress without mistaking it for release
# evidence from the authoritative repository history.
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
mode=${1:---real}
case "$mode" in --real|--synthetic|--inside-synthetic|--synthetic-post-gate|--post-gate) ;; *) echo 'usage: release-evidence.sh --real|--synthetic' >&2; exit 2;; esac

if [[ "$mode" == --synthetic || "$mode" == --synthetic-post-gate ]]; then
  temp=$(mktemp -d /tmp/nerocd-release-source.XXXXXXXX)
  cleanup(){ rm -rf -- "$temp"; }
  trap cleanup EXIT HUP INT TERM
  repo="$temp/repo"; mkdir -p "$repo"
  # Archive the current worktree, including intentionally untracked source,
  # while excluding local build/cache state and the source control metadata.
  tar -C "$root" --exclude=.git --exclude=.cache --exclude=.cachebro --exclude=bin --exclude=artifacts --exclude=web/app/node_modules --exclude=web/app/playwright-report --exclude=web/app/test-results --exclude=web/dist -cf - . | tar -C "$repo" -xf -
  mkdir -p "$repo/web/dist"; touch "$repo/web/dist/.gitkeep"
  git -C "$repo" init -q
  git -C "$repo" config user.name 'NeroCD local evidence'
  git -C "$repo" config user.email 'local-evidence@invalid'
  git -C "$repo" add -A
  evidence_time=${SOURCE_DATE_EPOCH:-$(date +%s)}
  GIT_AUTHOR_DATE="@$evidence_time" GIT_COMMITTER_DATE="@$evidence_time" git -C "$repo" commit -qm 'synthetic local evidence source'
  inner_mode=--inside-synthetic
  if [[ "$mode" == --synthetic-post-gate ]]; then inner_mode=--post-gate; fi
  NEROCD_RELEASE_SYNTHETIC=1 NEROCD_RELEASE_EVIDENCE_TEST_ONLY=1 bash "$repo/scripts/release-evidence.sh" "$inner_mode"
  printf 'synthetic release evidence: %s/artifacts/release-evidence\n' "$repo"
  exit 0
fi

cd "$root"
fail(){ printf 'release-evidence: %s\n' "$*" >&2; exit 1; }
for tool in git go bun node docker jq tar gzip shasum strings cmp awk sed file; do command -v "$tool" >/dev/null || fail "missing required tool: $tool"; done
docker buildx version >/dev/null 2>&1 || fail 'Docker buildx is unavailable'
docker info >/dev/null 2>&1 || fail 'Docker daemon is unavailable'

if [[ "$mode" == --real ]]; then
  [[ -z $(git status --porcelain=v1 --untracked-files=all) ]] || fail 'real release evidence requires a clean git worktree; use make release-evidence-synthetic-gate for a disposable source harness'
fi
[[ -z $(git status --porcelain=v1 --untracked-files=all) ]] || fail 'evidence source repository is not clean'
if [[ "$mode" == --post-gate && "${NEROCD_RELEASE_EVIDENCE_TEST_ONLY:-}" != 1 ]]; then
  fail 'post-gate mode is test-only and requires NEROCD_RELEASE_EVIDENCE_TEST_ONLY=1'
fi

revision=$(git rev-parse HEAD)
short_revision=${revision:0:12}
source_date_epoch=$(git log -1 --format=%ct)
[[ "$source_date_epoch" =~ ^[0-9]+$ ]] || fail 'git commit time is not a valid SOURCE_DATE_EPOCH'
describe=$(git describe --tags --always --match 'v[0-9]*' 2>/dev/null || true)
[[ -n "$describe" ]] || describe="0.1.0-dev"
release_version="${describe#v}+git.${short_revision}"
[[ "$release_version" =~ ^[0-9A-Za-z._+-]+$ ]] || fail 'derived release version contains unsafe characters'
kind=real
[[ "${NEROCD_RELEASE_SYNTHETIC:-}" == 1 ]] && kind=synthetic_untrusted

out="artifacts/release-evidence"
rm -rf -- "$out"
mkdir -p "$out/a" "$out/b"
cleanup(){
  local status=$?
  # A cleanup success must never convert a failed evidence check into a pass.
  trap - EXIT HUP INT TERM
  rm -rf -- "$out/oci-expanded-a" "$out/oci-expanded-b" "$out/tamper"
  exit "$status"
}
trap cleanup EXIT HUP INT TERM

export SOURCE_DATE_EPOCH="$source_date_epoch"
export NEROCD_RELEASE_VERSION="$release_version"

# The complete accepted release gate inventory is a Make target so CI can run
# the identical set. Any missing/failed command stops this script immediately.
# `--post-gate` is deliberately test-only; it is used to exercise artifact
# parsing after a focused script change and is never the release target.
if [[ "$mode" == --post-gate ]]; then
  printf 'release evidence TEST-ONLY: accepted gates intentionally omitted; artifact phases only\n'
  (cd web/app && bun install --frozen-lockfile >/dev/null)
else
  make release-evidence-accepted-gates
fi

ldflags="-buildid= -X main.version=${release_version}"
for arch in amd64 arm64; do
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 GOCACHE="$root/.cache/go-build" go build -trimpath -buildvcs=false -ldflags="$ldflags" -o "$out/a/nerocd-linux-$arch" ./cmd/nerocd
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 GOCACHE="$root/.cache/go-build" go build -trimpath -buildvcs=false -ldflags="$ldflags" -o "$out/b/nerocd-linux-$arch" ./cmd/nerocd
  cmp "$out/a/nerocd-linux-$arch" "$out/b/nerocd-linux-$arch" || fail "binary is not reproducible for linux/$arch"
  file "$out/a/nerocd-linux-$arch" | rg -q "(x86-64|aarch64|ARM aarch64)" || fail "binary architecture inspection failed for linux/$arch"
done
# Linux artifacts are intentionally not executed on the macOS evidence host.
# Build a same-source native verifier with the identical linker flags instead
# of depending on host binfmt emulation or Go's non-stable string layout.
host_verifier=$(mktemp "$out/host-version.XXXXXX")
go build -trimpath -buildvcs=false -ldflags="$ldflags" -o "$host_verifier" ./cmd/nerocd
"$host_verifier" version | rg -Fqx "$release_version" || fail 'release linker flags did not set the derived version'
rm -f -- "$host_verifier"

# The SBOM is deterministic only under the explicit epoch/version contract.
node scripts/supply-chain-policy.mjs
cp artifacts/sbom.json "$out/a/sbom.json"; cp artifacts/checksums.txt "$out/a/policy-checksums.txt"
node scripts/supply-chain-policy.mjs
cmp artifacts/sbom.json "$out/a/sbom.json" || fail 'CycloneDX SBOM is not reproducible under SOURCE_DATE_EPOCH'

build_oci(){
  local destination=$1
  docker buildx build --platform linux/amd64,linux/arm64 --provenance=false --sbom=false \
    --build-arg SOURCE_DATE_EPOCH="$source_date_epoch" --build-arg NEROCD_VERSION="$release_version" \
    --output "type=oci,dest=$destination" .
}
build_oci "$out/a/nerocd.oci.tar"
build_oci "$out/b/nerocd.oci.tar"
mkdir "$out/oci-expanded-a" "$out/oci-expanded-b"
tar -C "$out/oci-expanded-a" -xf "$out/a/nerocd.oci.tar"
tar -C "$out/oci-expanded-b" -xf "$out/b/nerocd.oci.tar"

inspect_oci(){
  local expanded=$1 binary_dir=$2 nested_digest
  local runnable_index="$expanded/index.json"
  # The Docker buildx OCI exporter may wrap the multi-platform index in the
  # OCI layout's outer index. Resolve exactly one nested image index when the
  # outer index contains no runnable platform descriptor.
  if ! jq -e '[.manifests[] | select(.platform.os == "linux" and (.platform.architecture == "amd64" or .platform.architecture == "arm64"))] | length > 0' "$runnable_index" >/dev/null; then
    nested_digest=$(jq -er '[.manifests[] | select(.mediaType == "application/vnd.oci.image.index.v1+json") | .digest] | if length == 1 then .[0] else error("expected one nested OCI image index") end' "$runnable_index") || fail 'OCI layout has no unambiguous nested image index'
    runnable_index="$expanded/blobs/sha256/${nested_digest#sha256:}"
    [[ -f "$runnable_index" ]] || fail 'OCI nested image index blob is absent'
  fi
  # Buildx may include non-runnable attestation descriptors with an
  # `unknown/unknown` platform. Admission is intentionally about the two
  # runnable image manifests; reject duplicates or any missing target.
  jq -e '[.manifests[] | select(.platform.os == "linux" and (.platform.architecture == "amd64" or .platform.architecture == "arm64")) | "\(.platform.os)/\(.platform.architecture)"] | sort == ["linux/amd64", "linux/arm64"]' "$runnable_index" >/dev/null || fail 'OCI archive lacks exactly linux/amd64 and linux/arm64 runnable manifests'
  jq -er '.manifests[] | select(.platform.os == "linux" and (.platform.architecture == "amd64" or .platform.architecture == "arm64")) | .digest' "$runnable_index" | while read -r manifest_digest; do
    manifest="$expanded/blobs/sha256/${manifest_digest#sha256:}"
    config_digest=$(jq -er '.config.digest' "$manifest")
    config="$expanded/blobs/sha256/${config_digest#sha256:}"
    jq -e '.config.User == "nerocd" and .config.Entrypoint == ["nerocd"] and ([.config.Env[]? | select(startswith("NEROCD_DEV_SEED_FILE="))] | length == 0)' "$config" >/dev/null || fail 'OCI config weakens nonroot entrypoint or carries a development seed'
    layer=$(jq -er '.layers[-1].digest' "$manifest")
    gzip -dc "$expanded/blobs/sha256/${layer#sha256:}" | tar -tf - | rg -q '^usr/local/bin/nerocd$' || fail 'OCI final layer lacks NeroCD binary'
    gzip -dc "$expanded/blobs/sha256/${layer#sha256:}" | tar -xOf - usr/local/bin/nerocd >"$binary_dir/${manifest_digest#sha256:}.nerocd"
  done
  # The final image must not contain sources, source maps, development seed
  # files, or mutable credential defaults. Binaries are checked separately.
  for blob in "$expanded"/blobs/sha256/*; do
    if gzip -t "$blob" >/dev/null 2>&1; then
      if gzip -dc "$blob" | tar -tf - 2>/dev/null | rg -q '(^|/)(\.git|.*\.go|.*\.map|dev\.sql|secrets?)/'; then
        fail 'OCI layer includes source, source map, dev seed, or secret path'
      fi
    fi
  done
  for binary in "$binary_dir"/*.nerocd; do
    ! strings "$binary" | rg -F 'admin@example.local' >/dev/null || fail 'OCI binary carries a default administrator credential'
    ! strings "$binary" | rg -F '8c6976e5b5410415bde908bd4dee15dfb167a9c873fc4bb8a81f6f2ab448a918' >/dev/null || fail 'OCI binary carries known development credential material'
  done
}
mkdir "$out/a/image-binaries" "$out/b/image-binaries"
inspect_oci "$out/oci-expanded-a" "$out/a/image-binaries"
inspect_oci "$out/oci-expanded-b" "$out/b/image-binaries"

# OCI exporters can choose tar member ordering, so compare the canonical index
# and config/manifest digest graph rather than asserting a container archive's
# transport bytes. These inputs are the actual local image identity.
for expanded in "$out/oci-expanded-a" "$out/oci-expanded-b"; do
  jq -cS . "$expanded/index.json" >"$expanded/index.canonical.json"
done
cmp "$out/oci-expanded-a/index.canonical.json" "$out/oci-expanded-b/index.canonical.json" || fail 'OCI manifest graph is not reproducible under SOURCE_DATE_EPOCH'

cp "$out/a/nerocd-linux-amd64" "$out/nerocd-linux-amd64"
cp "$out/a/nerocd-linux-arm64" "$out/nerocd-linux-arm64"
cp "$out/a/sbom.json" "$out/sbom.cdx.json"
cp "$out/a/nerocd.oci.tar" "$out/nerocd.oci.tar"
for item in nerocd-linux-amd64 nerocd-linux-arm64 sbom.cdx.json nerocd.oci.tar; do shasum -a 256 "$out/$item"; done | sed "s#  $out/#  #" >"$out/checksums.txt"

verify_checksums(){
  local directory=$1
  while read -r digest name; do [[ $(shasum -a 256 "$directory/$name" | awk '{print $1}') == "$digest" ]] || return 1; done <"$directory/checksums.txt"
}
verify_checksums "$out" || fail 'generated checksums do not verify'
mkdir "$out/tamper"; cp "$out/nerocd-linux-amd64" "$out/tamper/nerocd-linux-amd64"; cp "$out/checksums.txt" "$out/tamper/checksums.txt"; printf x >>"$out/tamper/nerocd-linux-amd64"
verify_checksums "$out/tamper" && fail 'tampered binary unexpectedly passed checksum verification'

oci_index_digest=$(shasum -a 256 "$out/oci-expanded-a/index.canonical.json" | awk '{print $1}')
binary_amd64_digest=$(shasum -a 256 "$out/nerocd-linux-amd64" | awk '{print $1}')
sbom_digest=$(shasum -a 256 "$out/sbom.cdx.json" | awk '{print $1}')
jq -n --arg kind "$kind" --arg version "$release_version" --arg revision "$revision" --argjson epoch "$source_date_epoch" --arg image "$oci_index_digest" --arg amd64 "$binary_amd64_digest" --arg sbom "$sbom_digest" \
  '{schema:"nerocd.local.release-manifest/v1",trust:"UNTRUSTED_LOCAL_NO_SIGNATURE",kind:$kind,version:$version,revision:$revision,source_date_epoch:$epoch,artifacts:{oci_index_sha256:$image,binary_linux_amd64_sha256:$amd64,sbom_sha256:$sbom}}' | jq -S . >"$out/manifest.json"
jq -n --arg kind "$kind" --arg version "$release_version" --arg revision "$revision" --argjson epoch "$source_date_epoch" --arg amd64 "$binary_amd64_digest" \
  '{_type:"https://in-toto.io/Statement/v1",subject:[{name:"nerocd-linux-amd64",digest:{sha256:$amd64}}],predicateType:"https://slsa.dev/provenance/v1",predicate:{buildDefinition:{buildType:"https://nerocd.dev/local-release-evidence/v1",externalParameters:{version:$version,revision:$revision,source_date_epoch:$epoch,kind:$kind}},runDetails:{builder:{id:"local/unsigned"},metadata:{trust:"UNTRUSTED_LOCAL_NO_SIGNATURE"}}}}' | jq -S . >"$out/provenance.intoto.json"

jq -e '.trust == "UNTRUSTED_LOCAL_NO_SIGNATURE" and .kind == $kind and (.revision|length == 40)' --arg kind "$kind" "$out/manifest.json" >/dev/null || fail 'local manifest is not canonical untrusted evidence'
jq -e '.predicate.runDetails.metadata.trust == "UNTRUSTED_LOCAL_NO_SIGNATURE"' "$out/provenance.intoto.json" >/dev/null || fail 'provenance incorrectly implies signing trust'
# This outer checksum record is the exact cross-job evidence contract. It
# deliberately covers the local checksum record and both local statements in
# addition to the binary, OCI, and CycloneDX artifacts. The file itself is
# subsequently hashed by the trusted-release manifest, avoiding a self-hash.
for item in nerocd-linux-amd64 nerocd-linux-arm64 sbom.cdx.json nerocd.oci.tar checksums.txt manifest.json provenance.intoto.json; do
  shasum -a 256 "$out/$item"
done | sed "s#  $out/#  #" >"$out/evidence.sha256"
(cd "$out" && shasum -a 256 -c evidence.sha256 >/dev/null) || fail 'cross-job evidence checksum record does not verify'
# Keep the transferable artifact contract intentionally small and explicit.
# The duplicate build workspaces were only required for reproducibility checks,
# not for downstream signing or verification.
rm -rf -- "$out/a" "$out/b"
printf 'release evidence PASS kind=%s version=%s revision=%s output=%s\n' "$kind" "$release_version" "$revision" "$out"
