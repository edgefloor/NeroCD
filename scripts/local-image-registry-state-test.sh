#!/usr/bin/env bash
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
source "$root/scripts/local-image-registry.sh"
scratch_dir=$(mktemp -d /tmp/nerocd-registry-state.XXXXXXXX)
trap 'rm -rf -- "$scratch_dir"' EXIT
trap 'printf "%s\n" unexpected >>"$scratch_dir/err-traps"; exit 91' ERR

# Expected inspection failures must retain their output and status without
# invoking an inherited caller ERR trap inside the command substitution.
docker() {
  case "$inspection" in
    present) printf '%s\n' '[]' ;;
    absent) printf '%s\n' 'Error: No such object: candidate' >&2; return 1 ;;
    error) printf '%s\n' 'Cannot connect to the Docker daemon' >&2; return 1 ;;
  esac
}

for resource in image container; do
  for inspection in present absent error; do
    local_registry_last_query_state=''
    if "local_registry_${resource}_state" candidate; then status=0; else status=$?; fi
    expected_status=1
    if [[ "$inspection" == present ]]; then expected_status=0; fi
    if [[ "$status" != "$expected_status" || "$local_registry_last_query_state" != "$inspection" ]]; then
      printf 'incorrect %s inspection result for %s\n' "$resource" "$inspection" >&2
      exit 1
    fi
    if [[ -e "$scratch_dir/err-traps" ]]; then
      printf 'expected %s inspection failure invoked caller ERR trap\n' "$resource" >&2
      exit 1
    fi
  done
done
printf '%s\n' 'local image registry state tests: PASS'
