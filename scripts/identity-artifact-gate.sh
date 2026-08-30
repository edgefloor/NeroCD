#!/usr/bin/env bash
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
dir=$(mktemp -d /tmp/nerocd-identity-artifacts.XXXXXXXX)
tag="nerocd-identity-artifact:${RANDOM}${RANDOM}"
container=""
cleanup() {
  set +e
  [[ -z "$container" ]] || docker rm -f "$container" >/dev/null 2>&1 || true
  docker image rm -f "$tag" >/dev/null 2>&1 || true
  rm -rf -- "$dir"
}
trap cleanup EXIT

for tool in go docker strings rg; do
  command -v "$tool" >/dev/null || { printf 'identity artifact scan: missing required tool: %s\n' "$tool" >&2; exit 1; }
done
go build -trimpath -o "$dir/nerocd" "$root/cmd/nerocd"
docker build -t "$tag" "$root" >/dev/null
container=$(docker create "$tag")
docker cp "$container:/usr/local/bin/nerocd" "$dir/image-nerocd"

for binary in "$dir/nerocd" "$dir/image-nerocd"; do
  ! strings "$binary" | rg -F 'admin@example.local' >/dev/null
  ! strings "$binary" | rg -F '8c6976e5b5410415bde908bd4dee15dfb167a9c873fc4bb8a81f6f2ab448a918' >/dev/null
done
! rg -F 'defaultValue="admin"' "$root/web/app/src" "$root/web/dist" >/dev/null
! rg -F 'admin@example.local' "$root/web/dist" >/dev/null
printf 'identity artifact scan passed\n'
