#!/usr/bin/env bash
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
source "$root/scripts/local-image-registry.sh"
suffix=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')
source_tag="nerocd-local-registry-fixture:${suffix}"
scratch_dir=$(mktemp -d /tmp/nerocd-local-registry.XXXXXXXX)
registry_id=''
published_tag=''
published_ref=''
pass=false

cleanup() {
  local code=$?
  trap - ERR
  set +e
  registry_id=$local_registry_container_id
  published_tag=$local_registry_tag
  published_ref=$local_registry_image_ref
  local_registry_cleanup
  rm -rf -- "$scratch_dir"
  if [[ -n "$registry_id" ]] && docker inspect "$registry_id" >/dev/null 2>&1; then
    printf '%s\n' 'local image registry gate left its captured container behind' >&2
    code=1
  fi
  if [[ -n "$published_tag" ]] && docker image inspect "$published_tag" >/dev/null 2>&1; then
    printf '%s\n' 'local image registry gate left its captured tag behind' >&2
    code=1
  fi
  if [[ -n "$published_ref" ]] && docker image inspect "$published_ref" >/dev/null 2>&1; then
    printf '%s\n' 'local image registry gate left its captured digest reference behind' >&2
    code=1
  fi
  [[ "$pass" == true && $code -eq 0 ]] && printf '%s\n' 'local image registry gate: PASS'
  exit "$code"
}
trap cleanup EXIT
trap 'exit 1' ERR

for command in docker od; do command -v "$command" >/dev/null; done
docker info >/dev/null
printf '%s\n' 'local-registry-scratch-payload' >"$scratch_dir/payload"
printf '%s\n' 'FROM scratch' 'COPY payload /payload' >"$scratch_dir/Dockerfile"
docker build -t "$source_tag" "$scratch_dir" >/dev/null
local_registry_publish "$source_tag" "$suffix"
[[ "$local_registry_image_ref" =~ ^127\.0\.0\.1:[1-9][0-9]{0,4}/nerocd-gate-${suffix}@sha256:[a-f0-9]{64}$ ]]
docker image inspect "$local_registry_image_ref" >/dev/null
pass=true
