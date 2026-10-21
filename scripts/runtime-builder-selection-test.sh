#!/usr/bin/env bash
# Docker-free behavioral coverage for runtime gate builder selection. The test
# executes the selector functions extracted from both real gate scripts.
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp=$(mktemp -d /tmp/nerocd-runtime-builder-selection.XXXXXXXX)
cleanup() { rm -rf -- "$tmp"; }
trap cleanup EXIT HUP INT TERM

failures=0
fail() {
  printf 'runtime builder selection: %s\n' "$*" >&2
  failures=$((failures + 1))
}

extract_selector() {
  awk '
    /^active_buildx_builder\(\)\{/ { capture = 1 }
    capture { print }
    /^select_engine_builder\(\)\{/ { selector = 1 }
    capture && selector && /^}$/ { exit }
  ' "$1"
}

run_case() {
  local gate=$1 scenario=$2 expected_rc=$3 expected_builder=$4 expected_source=$5 expected_diagnostic=$6
  local case_dir="$tmp/$(basename "$(dirname "$gate")")-$scenario" output rc
  mkdir -p "$case_dir"
  if (
    set +e
    dir=$case_dir
    docker() {
      printf '%s\n' "$*" >>"$case_dir/docker.trace"
      case "$1 $2" in
        'context show')
          [[ $scenario != context-failure ]] || return 19
          if [[ $scenario == default-context ]]; then printf '%s\n' default; else printf '%s\n' orbstack; fi
          ;;
        'buildx inspect')
          case "$3" in
            default)
              printf 'Name: default\nDriver: docker\nStatus: running\n'
              ;;
            orbstack)
              if [[ $scenario == wrong-driver ]]; then
                printf 'Name: orbstack\nDriver: docker-container\nStatus: running\n'
              else
                printf 'Name: orbstack\nDriver: docker\nStatus: running\n'
              fi
              ;;
            *) return 20 ;;
          esac
          ;;
        'buildx ls')
          case "$scenario" in
            context-mismatch) printf '%s\n' '{"Name":"default","Current":true}' ;;
            malformed-listing) printf '%s\n' '{"Name":"orbstack","Current":"true"}' ;;
            ambiguous-active)
              printf '%s\n' '{"Name":"orbstack","Current":true}' '{"Name":"other","Current":true}'
              ;;
            *) printf '%s\n' '{"Name":"orbstack","Current":true}' ;;
          esac
          ;;
        *) return 21 ;;
      esac
    }
    eval "$(extract_selector "$gate")"
    select_engine_builder
    rc=$?
    printf 'rc=%s builder=%s source=%s diagnostic=%s\n' "$rc" "$engine_builder" "$builder_source" "$builder_diagnostic"
    exit 0
  ) >"$case_dir/output"; then
    rc=0
  else
    rc=$?
  fi
  output=$(<"$case_dir/output")
  [[ $rc -eq 0 ]] || { fail "$gate $scenario harness failed rc=$rc"; return; }
  [[ $output == "rc=$expected_rc builder=$expected_builder source=$expected_source diagnostic=$expected_diagnostic" ]] || {
    fail "$gate $scenario expected 'rc=$expected_rc builder=$expected_builder source=$expected_source diagnostic=$expected_diagnostic', got '$output'"
    return
  }
  if [[ $scenario != default-context ]] && grep -q '^buildx inspect default$' "$case_dir/docker.trace"; then
    fail "$gate $scenario inspected the default-context builder"
  fi
}

gates=(
  "$root/acceptance/runtime-provenance/gate.sh"
  "$root/acceptance/runtime-compose/gate.sh"
)
for gate in "${gates[@]}"; do
  run_case "$gate" default-context 0 default default default_selected
  run_case "$gate" nondefault-context 0 orbstack active active_selected
  run_case "$gate" context-mismatch 1 '' '' active_builder_context_mismatch
  run_case "$gate" context-failure 1 '' '' context_unavailable
  run_case "$gate" malformed-listing 1 '' '' active_list_invalid
  run_case "$gate" ambiguous-active 1 '' '' active_builder_ambiguous
  run_case "$gate" wrong-driver 1 '' '' active_builder_wrong_driver
done

(( failures == 0 )) || exit 1
printf 'runtime builder selection PASS gates=%s scenarios=7\n' "${#gates[@]}"
