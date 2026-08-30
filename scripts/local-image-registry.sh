#!/usr/bin/env bash
# Source-only fixture for acceptance gates that need a real repository@sha256
# reference without relying on an engine-specific RepoDigests implementation.

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  printf '%s\n' 'local-image-registry.sh must be sourced' >&2
  exit 64
fi

LOCAL_REGISTRY_IMAGE='registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373'
local_registry_container_id=''
local_registry_tag=''
local_registry_image_ref=''

local_registry_fail() {
  printf 'local registry fixture: %s\n' "$*" >&2
  return 1
}

# local_registry_publish SOURCE_TAG RUN_SUFFIX creates a loopback-only
# registry, publishes SOURCE_TAG into a run-unique repository, and exports
# local_registry_image_ref as a strict repository@sha256 reference.
local_registry_publish() {
  local source_tag=$1 suffix=$2 attempt port_line port repository digest candidate ready=false
  if ! [[ "$source_tag" =~ ^[a-z0-9][a-z0-9._/-]*:[a-z0-9][a-z0-9._-]*$ ]]; then local_registry_fail 'source tag is invalid'; return 1; fi
  if ! [[ "$suffix" =~ ^[a-f0-9]{12}$ ]]; then local_registry_fail 'run suffix is invalid'; return 1; fi
  if ! docker image inspect "$source_tag" >/dev/null 2>&1; then local_registry_fail 'source image is unavailable'; return 1; fi

  if ! local_registry_container_id=$(docker run -d -p 127.0.0.1::5000 "$LOCAL_REGISTRY_IMAGE"); then local_registry_fail 'registry container could not start'; return 1; fi
  if ! [[ "$local_registry_container_id" =~ ^[a-f0-9]{64}$ ]]; then local_registry_fail 'registry container id is invalid'; return 1; fi
  if ! port_line=$(docker port "$local_registry_container_id" 5000/tcp | head -n 1); then local_registry_fail 'registry port is unavailable'; return 1; fi
  if ! [[ "$port_line" =~ ^127\.0\.0\.1:([1-9][0-9]{0,4})$ ]]; then local_registry_fail 'registry is not loopback-bound'; return 1; fi
  port=${BASH_REMATCH[1]}
  if ! ((port <= 65535)); then local_registry_fail 'registry port is invalid'; return 1; fi
  if ! command -v curl >/dev/null 2>&1; then local_registry_fail 'curl is required for registry readiness'; return 1; fi
  for attempt in 1 2 3 4 5; do
    if curl --fail --silent --show-error --max-time 1 "http://127.0.0.1:${port}/v2/" >/dev/null 2>&1; then
      ready=true
      break
    fi
    sleep 1
  done
  if [[ "$ready" != true ]]; then local_registry_fail 'registry did not become ready'; return 1; fi
  repository="127.0.0.1:${port}/nerocd-gate-${suffix}"
  local_registry_tag="${repository}:candidate"
  if ! docker tag "$source_tag" "$local_registry_tag"; then local_registry_fail 'source image could not be tagged for registry'; return 1; fi
  for attempt in 1 2 3 4 5; do
    if docker push "$local_registry_tag" >/dev/null 2>&1; then
      break
    fi
    if [[ "$attempt" == 5 ]]; then local_registry_fail 'registry push did not become ready'; return 1; fi
    sleep 1
  done
  digest=''
  while IFS= read -r candidate; do
    if [[ "$candidate" =~ ^${repository}@sha256:[a-f0-9]{64}$ ]]; then
      digest=$candidate
      break
    fi
  done < <(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$local_registry_tag")
  if [[ -z "$digest" ]]; then local_registry_fail 'push did not produce an exact repository digest'; return 1; fi
  if ! docker image inspect "$digest" >/dev/null 2>&1; then local_registry_fail 'engine did not resolve published repository digest'; return 1; fi
  local_registry_image_ref=$digest
}

# local_registry_cleanup removes only resources whose exact IDs/tags were
# created by local_registry_publish. It never searches or prunes by name.
local_registry_cleanup() {
  if [[ -n "$local_registry_tag" ]]; then
    docker image rm -f "$local_registry_tag" >/dev/null 2>&1 || true
  fi
  if [[ -n "$local_registry_container_id" ]]; then
    docker rm -f "$local_registry_container_id" >/dev/null 2>&1 || true
  fi
  local_registry_container_id=''
  local_registry_tag=''
  local_registry_image_ref=''
}
