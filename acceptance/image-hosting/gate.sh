#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
source "$root/scripts/local-image-registry.sh"
: "${NEROCD_TEST_DATABASE_URL:?set a task-owned PostgreSQL test database}"
suffix=$(openssl rand -hex 6)
build_dir=$(mktemp -d)
tag_a="nerocd-image-hosting-a:${suffix}"
tag_b="nerocd-image-hosting-b:${suffix}"
cleanup() {
  local_registry_cleanup || true
  docker image rm "$tag_b" >/dev/null 2>&1 || true
  rm -rf "$build_dir"
}
trap cleanup EXIT
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) printf 'unsupported fixture architecture: %s\n' "$arch" >&2; exit 1 ;; esac
cp acceptance/image-hosting/fixture/Dockerfile "$build_dir/Dockerfile"
for version in A B; do
  env CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$build_dir/server" ./acceptance/image-hosting/fixture
  if [[ "$version" == A ]]; then tag=$tag_a; else tag=$tag_b; fi
  docker build --quiet --tag "$tag" "$build_dir"
done
local_registry_publish "$tag_a" "$suffix"
env NEROCD_IMAGE_TEST_TAG="$local_registry_tag" NEROCD_IMAGE_TEST_B="$tag_b" go test -race ./internal/runnerruntime -run '^TestImageDockerIntegration$' -count=1 -v
env NEROCD_IMAGE_TEST_TAG="$local_registry_tag" NEROCD_IMAGE_TEST_A="$local_registry_image_ref" NEROCD_IMAGE_TEST_B="$tag_b" go test -race ./internal/store -run '^TestPostgresHostingDockerAgentAndRiver$' -count=1 -v
