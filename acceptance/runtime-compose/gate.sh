#!/usr/bin/env bash
# Real typed Compose A/B/C gate. It uses only the HTTP control-plane APIs to
# create deployment objects; psql below is read-only acceptance evidence.
set -Eeuo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
source "$root/scripts/local-image-registry.sh"
file="$root/acceptance/runtime-compose/compose.yaml" evidence=/tmp/nerocd-compose-runtime.txt
runtime_profile=${NEROCD_RUNTIME_PROFILE:-development}
case "$runtime_profile" in development|production) ;; *) printf 'runtime-compose: invalid NEROCD_RUNTIME_PROFILE\n' >&2; exit 2 ;; esac
if [[ "$runtime_profile" == production ]]; then file+=" $root/acceptance/runtime-compose/compose.production-dogfood.yaml"; fi
dir=$(mktemp -d /tmp/nerocd-runtime-compose.XXXXXXXX)
suffix=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')
project="nerocd-compose-$suffix" compose_project="target_$suffix" image="nerocd-compose-runtime:$suffix" runner_image="nerocd-compose-runner:$suffix" git_image="nerocd-compose-git:$suffix"
fixture_a="nerocd-compose-fixture-a:$suffix" fixture_b="nerocd-compose-fixture-b:$suffix" fixture_c="nerocd-compose-fixture-c:$suffix" pass=false
health_network="${compose_project}_health" health_host="runtime-health-$suffix" health_url="http://${health_host}:8080/cgi-bin/health"
fixture_a_ref=$fixture_a fixture_b_ref=$fixture_b fixture_c_ref=$fixture_c socket_gid=0
fixture_registry_container_id='' fixture_registry_port='' fixture_registry_diagnostic=not_started
fixture_a_registry_tag='' fixture_b_registry_tag='' fixture_c_registry_tag=''
cleanup_helper_image='alpine:3.22.2@sha256:4b7ce07002c69e8f3d704a9c5d6fd3053be500b7f1c69fc0d80990c2ad8dd412'
runner_workdir="$dir/runner-workspace"; runner_secret_root="$dir/runner-secrets"; mkdir -m 0700 "$runner_workdir" "$runner_secret_root"; export NEROCD_RUNTIME_WORKDIR="$runner_workdir" NEROCD_RUNTIME_SECRET_ROOT="$runner_secret_root"
: >"$evidence"; record(){ printf '%s\n' "$*" >>"$evidence"; }; fail(){ trap - ERR; record "FAIL: $*"; printf 'runtime-compose: %s\n' "$*" >&2; exit 1; }
# Docker Desktop can remap the host socket group inside containers. Derive the
# effective mount GID in the same mount namespace the non-root runner uses.
docker_gid(){ docker run --rm -v /var/run/docker.sock:/var/run/docker.sock alpine:3.22.2@sha256:4b7ce07002c69e8f3d704a9c5d6fd3053be500b7f1c69fc0d80990c2ad8dd412 sh -ec 'stat -c %g /var/run/docker.sock'; }
# Docker Compose needs a locally-addressable repository to apply a fixture.
# The adapter retains its complete repository@sha256 reference so the runner
# can inspect and, when enabled, pull the same immutable artifact. Assertions
# below reject mutable tags, bare digests, and malformed provenance values.
compose(){
  local -a files=(-f "$root/acceptance/runtime-compose/compose.yaml")
  [[ "$runtime_profile" != production ]] || files+=(-f "$root/acceptance/runtime-compose/compose.production-dogfood.yaml")
  NEROCD_RUNTIME_IMAGE="$image" NEROCD_RUNNER_IMAGE="$runner_image" NEROCD_GIT_IMAGE="$git_image" NEROCD_FIXTURE_A="$fixture_a_ref" NEROCD_FIXTURE_B="$fixture_b_ref" NEROCD_FIXTURE_C="$fixture_c_ref" NEROCD_DOCKER_GID="$socket_gid" NEROCD_HEALTH_NETWORK="$health_network" NEROCD_HEALTH_HOST="$health_host" docker compose -p "$project" "${files[@]}" "$@"
}
db_user=${NEROCD_RUNTIME_OWNER_DATABASE_USER:-nerocd}
psql_query(){ compose exec -T postgres psql -U "$db_user" -d nerocd -Atc "$1"; }
diag_emit(){ record "$*"; printf '%s\n' "$*" >&2; }
active_buildx_builder(){
  local parsed count name
  parsed=$(jq -sr '
    if length == 0 or any(.[]; type != "object" or (.Current | type) != "boolean" or (.Name | type) != "string")
    then error("invalid buildx listing")
    else [.[] | select(.Current == true) | .Name] | unique | .[]
    end
  ') || return 2
  count=$(awk 'NF { count++ } END { print count + 0 }' <<<"$parsed")
  [[ $count -gt 0 ]] || return 3
  [[ $count -eq 1 ]] || return 4
  name=$(awk 'NF { print; exit }' <<<"$parsed")
  [[ "$name" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$ ]] || return 5
  printf '%s' "$name"
}
engine_builder_running(){
  local candidate=$1 inspect_file=$2 driver statuses
  docker buildx inspect "$candidate" >"$inspect_file" 2>"$inspect_file.err" || return 2
  driver=$(awk '$1 == "Driver:" { print $2 }' "$inspect_file")
  [[ "$driver" == docker ]] || return 3
  statuses=$(awk '$1 == "Status:" { print $2 }' "$inspect_file" | sort -u)
  [[ "$statuses" == running ]] || return 4
}
select_engine_builder(){
  local active rc listing="$dir/buildx-ls.json"
  engine_builder=''; builder_source=''; builder_diagnostic=selection_started
  if engine_builder_running default "$dir/buildx-default.inspect"; then
    engine_builder=default; builder_source=default; builder_diagnostic=default_selected
    return 0
  fi
  if ! docker buildx ls --format '{{json .}}' >"$listing" 2>"$dir/buildx-ls.err"; then
    builder_diagnostic=active_list_unavailable
    return 1
  fi
  if active=$(active_buildx_builder <"$listing" 2>"$dir/buildx-parse.err"); then rc=0; else rc=$?; fi
  case "$rc" in
    0) ;;
    2) builder_diagnostic=active_list_invalid; return 1 ;;
    3) builder_diagnostic=active_builder_missing; return 1 ;;
    4) builder_diagnostic=active_builder_ambiguous; return 1 ;;
    5) builder_diagnostic=active_builder_unsafe; return 1 ;;
    *) builder_diagnostic=active_list_invalid; return 1 ;;
  esac
  if engine_builder_running "$active" "$dir/buildx-active.inspect"; then
    rc=0
  else
    rc=$?
    case "$rc" in
      2) builder_diagnostic=active_builder_unavailable ;;
      3) builder_diagnostic=active_builder_wrong_driver ;;
      4) builder_diagnostic=active_builder_not_running ;;
      *) builder_diagnostic=active_builder_unavailable ;;
    esac
    return 1
  fi
  engine_builder=$active; builder_source=active; builder_diagnostic=active_selected
}
runner_terminal_class(){
  local source=$1
  if [[ ! -f "$source" ]]; then printf '%s' unavailable
  elif grep -Eq 'provenance stage=docker_compose_config status=failed_' "$source"; then printf '%s' config_failure
  elif grep -Eq 'provenance stage=(git_|ssh_).*status=failed_' "$source"; then printf '%s' provenance_failure
  elif grep -Eq 'status=failed_(image_reference|image_unavailable|image_access)_exit_-?[0-9]+$' "$source"; then printf '%s' image_failure
  elif grep -Eiq 'confirm lease authority|lease authority' "$source"; then printf '%s' authority_failure
  elif grep -Eiq 'connection refused|network is unreachable|i/o timeout|context deadline exceeded' "$source"; then printf '%s' transport_failure
  else printf '%s' unclassified
  fi
}
compose_config_failure_signature(){
  local source=$1
  [[ -f "$source" ]] || return 0
  sed -nE 's/.*(provenance stage=docker_compose_config status=failed_(image_reference|image_unavailable|image_access|port_conflict|docker_access|host_key|authentication|repository|permissions|unavailable|unknown|canceled|deadline)_exit_-?[0-9]{1,10})([^0-9].*|$)/\1/p' "$source" | tail -n 1
}
provenance_tail_pair_pattern(){
  printf '%s' '^(resolve=start|deployment_cancellation=(watching|receipt_observed)|ssh_credential=start|ssh_transport=ready|ssh_keyscan=start|ssh_fingerprint=matched|git=available|(git_init|git_remote|git_fetch|git_checkout|git_rev_parse|docker_compose_config|compose_canonicalize|provenance_callback)=start|(compose_canonicalize|provenance_callback)=failed|(git_init|git_remote_add|git_fetch|git_checkout|git_rev-parse|docker_compose_config|docker_compose_apply|docker_compose_reconcile)=failed_(image_reference|image_unavailable|image_access|port_conflict|docker_access|host_key|authentication|repository|permissions|unavailable|unknown|canceled|deadline)_exit_-?[0-9]{1,10})$'
}
provenance_stage_tail(){
  local source=$1 pair_pattern
  [[ -f "$source" ]] || return 0
  pair_pattern=$(provenance_tail_pair_pattern)
  sed -nE 's/.*provenance stage=(resolve|deployment_cancellation|ssh_credential|ssh_transport|ssh_keyscan|ssh_fingerprint|git|git_init|git_remote|git_remote_add|git_fetch|git_checkout|git_rev_parse|git_rev-parse|docker_compose_config|docker_compose_apply|docker_compose_reconcile|compose_canonicalize|provenance_callback) status=(start|watching|receipt_observed|available|ready|matched|failed|failed_(image_reference|image_unavailable|image_access|port_conflict|docker_access|host_key|authentication|repository|permissions|unavailable|unknown|canceled|deadline)_exit_-?[0-9]{1,10})([^A-Za-z0-9_-].*|$)/\1=\2/p' "$source" | sed -nE -e "/$pair_pattern/p" | tail -n 24 | jq -Rsc 'split("\n") | map(select(length > 0))'
}
classify_log(){
  local label=$1 source=$2 raw="$dir/diagnostic-$1.raw" available=true lines=0 bytes=0 class=unclassified
  if [[ ! -f "$source" ]] || ! tail -n 80 "$source" 2>/dev/null | tail -c 16384 >"$raw"; then
    available=false; class=unavailable
  else
    lines=$(awk 'END { print NR + 0 }' "$raw")
    bytes=$(wc -c <"$raw" | tr -d '[:space:]')
    if [[ $bytes -eq 0 ]]; then
      class=empty
    else
      case "$label" in
        runner_build)
          if grep -Eiq 'failed to resolve source metadata|pull access denied|repository does not exist' "$raw"; then class=source_metadata_resolution
          elif grep -Eiq 'no such image|not found|manifest unknown' "$raw"; then class=base_unavailable
          elif grep -Eiq 'failed to build|failed to solve|error:' "$raw"; then class=build_failed
          elif grep -Eq '(^|[[:space:]])DONE([[:space:]]|$)' "$raw"; then class=build_progress
          fi
          ;;
        runner_ready)
          if grep -Eiq 'failed|error' "$raw"; then class=runner_start_failed
          elif grep -Eq ' (Started|Starting|Created|Creating)[[:space:]]*$' "$raw"; then class=runner_start_progress
          fi
          ;;
        runner)
          class=$(runner_terminal_class "$raw")
          if [[ "$class" == unclassified ]]; then
            if grep -Eq 'status=failed_' "$raw"; then class=runner_operation_failed
            elif grep -Eq 'stage=(resolve|compose_) status=start|"event":"claimed_run"' "$raw"; then class=runner_activity
            fi
          fi
          ;;
      esac
    fi
  fi
  diag_emit "diagnostic_${label}=$(jq -cn --argjson available "$available" --argjson lines "$lines" --argjson bytes "$bytes" --arg class "$class" '{log_available:$available,line_count:$lines,byte_count:$bytes,class:$class}')"
}
sanitize_deployment_outcomes(){
  jq -ce '
    def deployment_status:
      if . == "queued" or . == "waiting_confirmation" or . == "assigned" or . == "preparing" or . == "applying" or . == "verifying" or . == "succeeded" or . == "failed" or . == "canceled" or . == "cancel_requested" or . == "rolling_back" or . == "rolled_back" or . == "rollback_failed" or . == "manual_intervention" then . else error("invalid deployment status") end;
    def failure_code:
      if . == "" then "none"
      elif . == "validation_failed" or . == "health_failed" or . == "apply_failed" or . == "compose_failed" or . == "resolved_image_unavailable" or . == "compose_reconcile_failed" or . == "compose_apply_failed" or . == "compose_transition_failed" or . == "compose_health_failed" or . == "provenance_resolution_failed" or . == "cancellation_requested" then .
      else "other" end;
    if type != "array" or length > 12 then error("invalid deployment outcome list")
    else [ .[] |
      if type != "object" or (.status | type) != "string" or (.failure_code | type) != "string" or ((.health_passed != null) and (.health_passed | type) != "boolean") or (.rollback_child | type) != "boolean" or (.rollback_safe | type) != "boolean" or (.previous_healthy | type) != "boolean" then error("invalid deployment outcome")
      else {
        status:(.status | deployment_status),
        failure_code:(.failure_code | failure_code),
        health:(if .health_passed == true then "passed" elif .health_passed == false then "failed" else "unknown" end),
        rollback:{child:.rollback_child,safe:.rollback_safe,previous_healthy:.previous_healthy,manual_intervention:(.status == "manual_intervention")}
      } end
    ] end
  '
}
terminal_classes(){
  local outcomes=$1 runner_class=$2
  jq -cn --argjson outcomes "$outcomes" --arg runner_class "$runner_class" '
    def outcome_class:
      if .failure_code == "resolved_image_unavailable" then "image_failure"
      elif .failure_code == "compose_apply_failed" or .failure_code == "apply_failed" then "apply_failure"
      elif .failure_code == "compose_health_failed" or .failure_code == "health_failed" then "health_failure"
      elif .failure_code == "provenance_resolution_failed" then "provenance_failure"
      elif .failure_code == "compose_transition_failed" or .failure_code == "compose_reconcile_failed" or .failure_code == "cancellation_requested" or .status == "rollback_failed" or .status == "manual_intervention" then "settlement_failure"
      else empty end;
    ([ $outcomes[] | outcome_class ] +
      (if $runner_class == "transport_failure" or $runner_class == "authority_failure" or $runner_class == "provenance_failure" or $runner_class == "image_failure" or $runner_class == "config_failure" then [$runner_class] else [] end))
    | unique | sort | .[:8]
  '
}
diagnose(){
  local output rc runner_id state_file="$dir/diagnostic-runner-state.json" outcomes runner_class config_signature provenance_tail pair_pattern
  set +e
  diag_emit 'diagnostic_begin=true'
  diag_emit "builder_resolution=${builder_diagnostic:-not_started}"
  diag_emit "fixture_registry=${fixture_registry_diagnostic:-not_started}"
  output=$(compose ps --all --format json 2>"$dir/diagnostic-compose-status.err")
  rc=$?
  if [[ $rc -eq 0 ]]; then
    output=$(jq -cs '
      def allowed_service: . == "postgres" or . == "server" or . == "proxy" or . == "git" or . == "runner" or . == "secret-dir-init" or . == "secret-init" or . == "pgdata-init" or . == "migrate" or . == "role-init";
      def state: if . == "created" or . == "running" or . == "paused" or . == "restarting" or . == "removing" or . == "exited" or . == "dead" then . else "unknown" end;
      def health: if . == "healthy" or . == "unhealthy" or . == "starting" then . elif . == "" or . == null then "none" else "unknown" end;
      [.[] | if type == "array" then .[] else . end
        | select((.Service | type) == "string" and (.Service | allowed_service))
        | {service:.Service,state:(.State | state),health:(.Health | health),exit_code:(if (.ExitCode | type) == "number" then .ExitCode else null end)}]
      | sort_by(.service)
    ' <<<"$output" 2>/dev/null)
    rc=$?
  fi
  if [[ $rc -eq 0 ]]; then diag_emit "compose_status=$output"; else diag_emit 'compose_status=unavailable'; fi
  runner_id=$(compose ps --all --quiet runner 2>"$dir/diagnostic-runner-id.err")
  rc=$?
  if [[ $rc -eq 0 && "$runner_id" =~ ^[0-9a-f]{12,64}$ ]] && docker container inspect --format '{{json .State}}' "$runner_id" >"$state_file" 2>/dev/null; then
    output=$(jq -c '
      def status: if . == "created" or . == "running" or . == "paused" or . == "restarting" or . == "removing" or . == "exited" or . == "dead" then . else "unknown" end;
      def health: if . == "healthy" or . == "unhealthy" or . == "starting" then . else "unknown" end;
      {status:(.Status | status),running:(.Running == true),paused:(.Paused == true),restarting:(.Restarting == true),oom_killed:(.OOMKilled == true),dead:(.Dead == true),exit_code:(if (.ExitCode | type) == "number" then .ExitCode else null end),error_present:((.Error | type) == "string" and (.Error | length) > 0),health:(if .Health then {status:(.Health.Status | health),failing_streak:(if (.Health.FailingStreak | type) == "number" then .Health.FailingStreak else null end)} else null end)}
    ' "$state_file" 2>/dev/null)
    if [[ $? -eq 0 ]]; then diag_emit "runner_state=$output"; else diag_emit 'runner_state=unavailable'; fi
  elif [[ $rc -eq 0 && -z "$runner_id" ]]; then
    diag_emit 'runner_state=absent'
  else
    diag_emit 'runner_state=unavailable'
  fi
  output=$(psql_query "select json_build_object('deployments',(select count(*) from deployments),'attempts',(select count(*) from deployment_attempts),'audits',(select count(*) from audit_events),'revisions',(select count(*) from revisions))" 2>"$dir/diagnostic-database.err")
  if [[ $? -eq 0 ]] && output=$(jq -ce 'select(type == "object" and ([.deployments,.attempts,.audits,.revisions] | all(type == "number")))' <<<"$output" 2>/dev/null); then
    diag_emit "database_counts=$output"
  else
    diag_emit 'database_counts=unavailable'
  fi
  output=$(psql_query "SELECT COALESCE(json_agg(outcome ORDER BY created_at DESC), '[]'::json)::text FROM (SELECT json_build_object('status',d.status,'failure_code',d.failure_code,'health_passed',d.health_passed,'rollback_child',(d.rollback_of_id IS NOT NULL),'rollback_safe',e.rollback_safe,'previous_healthy',(d.previous_healthy_revision_id IS NOT NULL)) AS outcome,d.created_at FROM deployments d JOIN environments e ON e.id=d.environment_id ORDER BY d.created_at DESC LIMIT 12) deployment_outcomes" 2>"$dir/diagnostic-deployments.err")
  if [[ $? -eq 0 ]] && outcomes=$(sanitize_deployment_outcomes <<<"$output" 2>/dev/null); then
    diag_emit "deployment_outcomes=$outcomes"
  else
    outcomes='[]'
    diag_emit 'deployment_outcomes=unavailable'
  fi
  classify_log runner_build "$dir/runner-build.log"
  classify_log runner_ready "$dir/runner.log"
  if compose logs --no-color --tail 80 runner >"$dir/diagnostic-runner.log" 2>/dev/null; then classify_log runner "$dir/diagnostic-runner.log"; else classify_log runner /nonexistent; fi
  # The class is a fixed vocabulary derived from private capped log text; the
  # text itself is never copied to stderr or the evidence file.
  runner_class=$(runner_terminal_class "$dir/diagnostic-runner.raw")
  config_signature=$(compose_config_failure_signature "$dir/diagnostic-runner.raw")
  if [[ -n "$config_signature" ]]; then diag_emit "runner_config_failure_signature=$config_signature"; else diag_emit 'runner_config_failure_signature=unavailable'; fi
  provenance_tail=$(provenance_stage_tail "$dir/diagnostic-runner.raw")
  pair_pattern=$(provenance_tail_pair_pattern)
  if [[ "$provenance_tail" =~ ^\[.*\]$ ]] && jq -e --arg pair_pattern "$pair_pattern" 'type == "array" and length <= 24 and all(.[]; type == "string" and test($pair_pattern))' <<<"$provenance_tail" >/dev/null 2>&1; then diag_emit "runner_provenance_tail=$provenance_tail"; else diag_emit 'runner_provenance_tail=unavailable'; fi
  if output=$(terminal_classes "$outcomes" "$runner_class" 2>/dev/null); then diag_emit "terminal_classes=$output"; else diag_emit 'terminal_classes=unavailable'; fi
  diag_emit 'diagnostic_end=true'
}
diagnostic_selftest(){
  local outcomes class signature provenance_tail input="$dir/diagnostic-selftest.log"
  outcomes=$(sanitize_deployment_outcomes <<'JSON'
[{"status":"manual_intervention","failure_code":"compose_health_failed","health_passed":false,"rollback_child":false,"rollback_safe":true,"previous_healthy":true},{"status":"rolled_back","failure_code":"","health_passed":true,"rollback_child":true,"rollback_safe":true,"previous_healthy":true}]
JSON
) || return 1
  jq -e 'length == 2 and .[0].status == "manual_intervention" and .[0].failure_code == "compose_health_failed" and .[0].rollback.manual_intervention == true' <<<"$outcomes" >/dev/null || return 1
  sanitize_deployment_outcomes <<'JSON' >/dev/null 2>&1 && return 1
[{"status":1,"failure_code":"compose_health_failed","health_passed":false,"rollback_child":false,"rollback_safe":true,"previous_healthy":true}]
JSON
  outcomes=$(sanitize_deployment_outcomes <<'JSON'
[{"status":"failed","failure_code":"bearer_super_secret_token","health_passed":false,"rollback_child":false,"rollback_safe":true,"previous_healthy":true}]
JSON
) || return 1
  jq -e '.[0].failure_code == "other"' <<<"$outcomes" >/dev/null || return 1
  outcomes=$(sanitize_deployment_outcomes <<'JSON'
[{"status":"failed","failure_code":"compose_apply_failed","health_passed":null,"rollback_child":false,"rollback_safe":true,"previous_healthy":false}]
JSON
) || return 1
  [[ "$outcomes" == '[{"status":"failed","failure_code":"compose_apply_failed","health":"unknown","rollback":{"child":false,"safe":true,"previous_healthy":false,"manual_intervention":false}}]' ]] || return 1
  while IFS='|' read -r line expected; do
    printf '%s\n' "$line" >"$input"
    class=$(runner_terminal_class "$input")
    [[ "$class" == "$expected" ]] || return 1
  done <<'CASES'
provenance stage=docker_compose_config status=failed_unknown_exit_1|config_failure
provenance stage=git_fetch status=failed_unknown_exit_1|provenance_failure
provenance stage=resolve status=failed_image_unavailable_exit_1|image_failure
confirm lease authority|authority_failure
connection refused|transport_failure
Authorization: Bearer super-secret-token|unclassified
CASES
  printf '%s\n' 'runner-1  | provenance stage=docker_compose_config status=failed_canceled_exit_-1 secret-token' >"$input"
  signature=$(compose_config_failure_signature "$input")
  [[ "$signature" == 'provenance stage=docker_compose_config status=failed_canceled_exit_-1' ]] || return 1
  printf '%s\n' 'provenance stage=docker_compose_config status=failed_attacker_controlled_exit_1' >"$input"
  [[ -z $(compose_config_failure_signature "$input") ]] || return 1
  printf '%s\n' 'runner-1  | provenance stage=docker_compose_config status=failed_unknown_exit_12345678901' >"$input"
  [[ -z $(compose_config_failure_signature "$input") ]] || return 1
  printf '%s\n' 'runner-1  | provenance stage=docker_compose_config status=failed_unknown_exit_ secret-token' >"$input"
  [[ -z $(compose_config_failure_signature "$input") ]] || return 1
  printf '%s\n' 'runner-1  | provenance stage=resolve status=start secret-token' 'runner-1  | provenance stage=git status=available' 'runner-1  | provenance stage=compose_canonicalize status=start' 'runner-1  | provenance stage=compose_canonicalize status=failed secret-token' 'runner-1  | provenance stage=provenance_callback status=start' 'runner-1  | provenance stage=provenance_callback status=failed secret-token' 'runner-1  | provenance stage=git_remote_add status=failed_unknown_exit_1 secret-token' 'runner-1  | provenance stage=docker_compose_config status=failed_unknown_exit_1 secret-token' 'provenance stage=attacker status=failed_unknown_exit_1' >"$input"
  provenance_tail=$(provenance_stage_tail "$input")
  jq -e '. == ["resolve=start","git=available","compose_canonicalize=start","compose_canonicalize=failed","provenance_callback=start","provenance_callback=failed","git_remote_add=failed_unknown_exit_1","docker_compose_config=failed_unknown_exit_1"]' <<<"$provenance_tail" >/dev/null || return 1
  : >"$input"
  for n in {1..25}; do
    if (( n % 2 )); then printf '%s\n' 'runner-1  | provenance stage=resolve status=start'; else printf '%s\n' 'runner-1  | provenance stage=git status=available'; fi
  done >"$input"
  provenance_tail=$(provenance_stage_tail "$input")
  jq -e 'length == 24 and .[0] == "git=available" and .[-1] == "resolve=start"' <<<"$provenance_tail" >/dev/null || return 1
  printf '%s\n%s\n%s\n%s\n%s\n%s\n%s\n' 'runner-1  | provenance stage=resolve status=failed_unknown_exit_1x secret-token' 'provenance stage=resolve status=available' 'provenance stage=ssh_transport status=start' 'provenance stage=git_remote status=failed_unknown_exit_1' 'provenance stage=compose_canonicalize status=failed_unknown_exit_1' 'provenance stage=provenance_callback status=failed_unknown_exit_1' 'provenance stage=attacker status=start' >"$input"
  [[ $(provenance_stage_tail "$input") == '[]' ]] || return 1
  printf '%s\n' 'provenance stage=git_fetch status=completed' 'provenance stage=resolve status=failed_image_unavailable_exit_1' >"$input"
  [[ $(runner_terminal_class "$input") == image_failure ]] || return 1
  outcomes=$(sanitize_deployment_outcomes <<'JSON'
[{"status":"failed","failure_code":"compose_health_failed","health_passed":false,"rollback_child":false,"rollback_safe":true,"previous_healthy":true}]
JSON
) || return 1
  [[ $(terminal_classes "$outcomes" unclassified) == '["health_failure"]' ]] || return 1
}
if [[ "${NEROCD_RUNTIME_COMPOSE_DIAGNOSTIC_SELFTEST:-}" == 1 ]]; then
  diagnostic_selftest
  rm -rf -- "$dir"
  printf 'runtime-compose: diagnostic selftest passed\n'
  exit 0
fi
start_fixture_registry(){
  local port_line ready=false attempt
  fixture_registry_diagnostic=start_started
  fixture_registry_container_id=$(docker run -d \
    --name "${project}-registry" \
    --label "com.docker.compose.project=$project" \
    --label "nerocd.acceptance.runtime-compose=$suffix" \
    --cap-drop ALL --security-opt no-new-privileges --read-only \
    --tmpfs /var/lib/registry:rw,noexec,nosuid,nodev \
    -p 127.0.0.1::5000 "$LOCAL_REGISTRY_IMAGE" 2>"$dir/registry-start.err") || {
      fixture_registry_diagnostic=start_failed
      return 1
    }
  if [[ ! "$fixture_registry_container_id" =~ ^[a-f0-9]{64}$ ]]; then
    fixture_registry_diagnostic=container_id_invalid
    return 1
  fi
  port_line=$(docker port "$fixture_registry_container_id" 5000/tcp 2>"$dir/registry-port.err") || {
    fixture_registry_diagnostic=port_unavailable
    return 1
  }
  if [[ ! "$port_line" =~ ^127\.0\.0\.1:([1-9][0-9]{0,4})$ ]]; then
    fixture_registry_diagnostic=port_binding_invalid
    return 1
  fi
  fixture_registry_port=${BASH_REMATCH[1]}
  if ((10#$fixture_registry_port > 65535)); then
    fixture_registry_diagnostic=port_binding_invalid
    return 1
  fi
  for attempt in 1 2 3 4 5; do
    if curl --fail --silent --show-error --max-time 1 "http://127.0.0.1:${fixture_registry_port}/v2/" >/dev/null 2>"$dir/registry-ready.err"; then
      ready=true
      break
    fi
    sleep 1
  done
  if [[ "$ready" != true ]]; then
    fixture_registry_diagnostic=readiness_failed
    return 1
  fi
  fixture_registry_diagnostic=ready
}
repository_digest_is_member(){
  local image=$1 repository=$2 candidate=$3 repo_digests
  [[ "$repository" =~ ^127\.0\.0\.1:[1-9][0-9]{0,4}/nerocd-compose-fixture-[abc]-[a-f0-9]{12}$ ]] || return 2
  [[ "$candidate" == "$repository@sha256:"* && "${candidate#"$repository@sha256:"}" =~ ^[a-f0-9]{64}$ ]] || return 2
  repo_digests=$(docker image inspect --format '{{json .RepoDigests}}' "$image" 2>"$dir/registry-repodigests.err") || return 2
  jq -e --arg candidate "$candidate" 'type == "array" and any(.[]; type == "string" and . == $candidate)' <<<"$repo_digests" >/dev/null 2>"$dir/registry-repodigests-jq.err" || {
    [[ $? -eq 1 ]] && return 1
    return 2
  }
}
publish_fixture_digest(){
  local source_tag=$1 label=$2 repository registry_tag source_id repo_digests resolved image_id_candidate membership_rc attempt
  [[ "$source_tag" =~ ^nerocd-compose-fixture-[abc]:[a-f0-9]{12}$ && "$label" =~ ^[abc]$ ]] || {
    fixture_registry_diagnostic=publish_input_invalid
    return 1
  }
  repository="127.0.0.1:${fixture_registry_port}/nerocd-compose-fixture-${label}-${suffix}"
  registry_tag="${repository}:candidate"
  case "$label" in
    a) fixture_a_registry_tag=$registry_tag ;;
    b) fixture_b_registry_tag=$registry_tag ;;
    c) fixture_c_registry_tag=$registry_tag ;;
  esac
  source_id=$(docker image inspect --format '{{.Id}}' "$source_tag" 2>"$dir/registry-${label}-source-inspect.err") || {
    fixture_registry_diagnostic=source_inspect_failed
    return 1
  }
  [[ "$source_id" =~ ^sha256:[a-f0-9]{64}$ ]] || {
    fixture_registry_diagnostic=source_id_invalid
    return 1
  }
  docker tag "$source_tag" "$registry_tag" 2>"$dir/registry-${label}-tag.err" || {
    fixture_registry_diagnostic=tag_failed
    return 1
  }
  for attempt in 1 2 3 4 5; do
    if docker push "$registry_tag" >"$dir/registry-${label}-push.log" 2>&1; then break; fi
    if [[ "$attempt" == 5 ]]; then
      fixture_registry_diagnostic=push_failed
      return 1
    fi
    sleep 1
  done
  repo_digests=$(docker image inspect --format '{{json .RepoDigests}}' "$registry_tag" 2>"$dir/registry-${label}-repodigests.err") || {
    fixture_registry_diagnostic=repodigests_unavailable
    return 1
  }
  resolved=$(jq -er --arg prefix "${repository}@sha256:" '
    if type != "array" then error("invalid")
    else [.[] | select(type == "string" and startswith($prefix))] | unique
      | if length == 1 then .[0] else error("ambiguous") end
    end
  ' <<<"$repo_digests" 2>"$dir/registry-${label}-repodigests-jq.err") || {
    fixture_registry_diagnostic=repodigest_missing_or_ambiguous
    return 1
  }
  if [[ "$resolved" != "$repository@sha256:"* || ! "${resolved#"$repository@sha256:"}" =~ ^[a-f0-9]{64}$ ]]; then
    fixture_registry_diagnostic=repodigest_invalid
    return 1
  fi
  case "$label" in
    a) fixture_a_ref=$resolved ;;
    b) fixture_b_ref=$resolved ;;
    c) fixture_c_ref=$resolved ;;
  esac
  repository_digest_is_member "$registry_tag" "$repository" "$resolved" || {
    fixture_registry_diagnostic=repodigest_not_member
    return 1
  }
  image_id_candidate="${repository}@${source_id}"
  if repository_digest_is_member "$registry_tag" "$repository" "$image_id_candidate"; then
    if [[ "$image_id_candidate" != "$resolved" ]]; then
      fixture_registry_diagnostic=image_id_membership_inconsistent
      return 1
    fi
    published_fixture_image_id_distinct=false
  else
    membership_rc=$?
    if [[ $membership_rc -ne 1 ]]; then
      fixture_registry_diagnostic=image_id_membership_unavailable
      return 1
    fi
    published_fixture_image_id_distinct=true
  fi
  docker image rm "$registry_tag" "$source_tag" >/dev/null 2>"$dir/registry-${label}-alias-remove.err" || {
    fixture_registry_diagnostic=alias_remove_failed
    return 1
  }
  local_registry_remove_image "$resolved" || {
    fixture_registry_diagnostic=digest_cache_remove_failed
    return 1
  }
  if local_registry_image_state "$resolved"; then
    fixture_registry_diagnostic=digest_cache_remained
    return 1
  elif [[ "$local_registry_last_query_state" != absent ]]; then
    fixture_registry_diagnostic=digest_cache_query_failed
    return 1
  fi
  for attempt in 1 2 3 4 5; do
    if docker pull "$resolved" >"$dir/registry-${label}-pull.log" 2>&1; then break; fi
    if [[ "$attempt" == 5 ]]; then
      fixture_registry_diagnostic=digest_pull_failed
      return 1
    fi
    sleep 1
  done
  docker image inspect "$resolved" >/dev/null 2>"$dir/registry-${label}-digest-inspect.err" || {
    fixture_registry_diagnostic=digest_resolution_failed
    return 1
  }
  docker tag "$resolved" "$source_tag" 2>"$dir/registry-${label}-source-retag.err" || {
    fixture_registry_diagnostic=source_retag_failed
    return 1
  }
  published_fixture_ref=$resolved
  published_fixture_registry_tag=$registry_tag
  published_fixture_id=$(docker image inspect --format '{{.Id}}' "$resolved" 2>"$dir/registry-${label}-resolved-inspect.err") || {
    fixture_registry_diagnostic=resolved_id_unavailable
    return 1
  }
  [[ "$published_fixture_id" == "$source_id" ]] || {
    fixture_registry_diagnostic=resolved_content_changed
    return 1
  }
  fixture_registry_diagnostic=published
}
remove_project_resources(){
  local exact_project=$1 kind output rc resource cleanup_ok=true error_file="$dir/cleanup-query.err"
  [[ "$exact_project" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$ ]] || { diag_emit 'cleanup_project_name_safe=false'; return 1; }
  for kind in container volume network; do
    case "$kind" in
      container) output=$(docker ps --all --quiet --filter "label=com.docker.compose.project=$exact_project" 2>"$error_file"); rc=$? ;;
      volume) output=$(docker volume ls --quiet --filter "label=com.docker.compose.project=$exact_project" 2>"$error_file"); rc=$? ;;
      network) output=$(docker network ls --quiet --filter "label=com.docker.compose.project=$exact_project" 2>"$error_file"); rc=$? ;;
    esac
    if [[ $rc -ne 0 ]]; then diag_emit "cleanup_${kind}_query=false"; cleanup_ok=false; continue; fi
    while IFS= read -r resource; do
      [[ -n "$resource" ]] || continue
      case "$kind" in
        container|network) [[ "$resource" =~ ^[0-9a-f]{12,64}$ ]] ;;
        volume) [[ "$resource" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,255}$ ]] ;;
      esac || { diag_emit "cleanup_${kind}_identifier_safe=false"; cleanup_ok=false; continue; }
      case "$kind" in
        container) docker rm --force "$resource" >/dev/null 2>"$dir/cleanup-container-rm.err" ;;
        volume) docker volume rm --force "$resource" >/dev/null 2>"$dir/cleanup-volume-rm.err" ;;
        network) docker network rm "$resource" >/dev/null 2>"$dir/cleanup-network-rm.err" ;;
      esac
      if [[ $? -ne 0 ]]; then diag_emit "cleanup_${kind}_remove=false"; cleanup_ok=false; fi
    done <<<"$output"
    case "$kind" in
      container) output=$(docker ps --all --quiet --filter "label=com.docker.compose.project=$exact_project" 2>"$error_file"); rc=$? ;;
      volume) output=$(docker volume ls --quiet --filter "label=com.docker.compose.project=$exact_project" 2>"$error_file"); rc=$? ;;
      network) output=$(docker network ls --quiet --filter "label=com.docker.compose.project=$exact_project" 2>"$error_file"); rc=$? ;;
    esac
    if [[ $rc -ne 0 || -n "$output" ]]; then diag_emit "cleanup_${kind}_query_or_residual=true"; cleanup_ok=false; fi
  done
  [[ "$cleanup_ok" == true ]]
}
remove_exact_image(){
  local exact_image=$1 output rc
  if [[ "$exact_image" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]{0,255}:[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$ ]]; then
    :
  elif [[ "$exact_image" =~ ^127\.0\.0\.1:([1-9][0-9]{0,4})/[A-Za-z0-9][A-Za-z0-9._/-]{0,255}:[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$ ]] && ((10#${BASH_REMATCH[1]} <= 65535)); then
    :
  else
    diag_emit 'cleanup_image_name_safe=false'
    return 1
  fi
  output=$(docker image ls --format '{{.Repository}}:{{.Tag}}' --filter "reference=$exact_image" 2>"$dir/cleanup-image-query.err")
  rc=$?
  [[ $rc -eq 0 ]] || { diag_emit 'cleanup_exact_image_query=false'; return 1; }
  if [[ -n "$output" && "$output" != "$exact_image" ]]; then diag_emit 'cleanup_exact_image_query_ambiguous=true'; return 1; fi
  if [[ "$output" == "$exact_image" ]] && ! docker image rm --force "$exact_image" >/dev/null 2>"$dir/cleanup-image-rm.err"; then diag_emit 'cleanup_exact_image_remove=false'; return 1; fi
  output=$(docker image ls --format '{{.Repository}}:{{.Tag}}' --filter "reference=$exact_image" 2>"$dir/cleanup-image-query.err")
  rc=$?
  [[ $rc -eq 0 && -z "$output" ]] || { diag_emit 'cleanup_exact_image_residual_or_query_failure=true'; return 1; }
}
cleanup(){
  local original=$? final cleanup_complete=true target=${compose_project:-} candidate host_uid host_gid
  trap - ERR
  set +e
  final=$original
  if [[ "$pass" != true ]]; then diagnose; fi
  # The target fixture borrows the gate-owned internal health network. Remove
  # the target first so Compose can remove that network with the control stack.
  if [[ -n "$target" ]]; then remove_project_resources "$target" || cleanup_complete=false; fi
  compose down --volumes --remove-orphans --rmi local --timeout 4 >/dev/null 2>"$dir/cleanup-down.err"
  if [[ $? -ne 0 ]]; then cleanup_complete=false; diag_emit 'cleanup_compose_down=false'; fi
  remove_project_resources "$project" || cleanup_complete=false
  if [[ -n "$target" ]]; then remove_project_resources "$target" || cleanup_complete=false; fi
  if [[ -n "$fixture_registry_container_id" ]] && ! local_registry_remove_container "$fixture_registry_container_id"; then
    cleanup_complete=false; diag_emit 'cleanup_fixture_registry_container=false'
  fi
  for candidate in "${fixture_a_ref:-}" "${fixture_b_ref:-}" "${fixture_c_ref:-}"; do
    [[ -n "$candidate" ]] || continue
    if ! local_registry_remove_image "$candidate"; then cleanup_complete=false; diag_emit 'cleanup_fixture_digest_ref=false'; fi
  done
  for candidate in "$image" "$runner_image" "$git_image" "$fixture_a" "$fixture_b" "$fixture_c" "${fixture_a_registry_tag:-}" "${fixture_b_registry_tag:-}" "${fixture_c_registry_tag:-}" "${project}-proxy:latest"; do
    [[ -n "$candidate" ]] || continue
    remove_exact_image "$candidate" || cleanup_complete=false
  done
  host_uid=$(id -u); host_gid=$(id -g)
  if [[ ! "$host_uid" =~ ^[0-9]+$ || ! "$host_gid" =~ ^[0-9]+$ ]]; then
    cleanup_complete=false; diag_emit 'cleanup_host_identity=false'
  elif [[ -d "$runner_secret_root" || -d "$runner_workdir" ]]; then
    docker run --rm --name "${project}-cleanup-ownership" --label "com.docker.compose.project=$project" --privileged --network none --user 0:0 -e "HOST_UID=$host_uid" -e "HOST_GID=$host_gid" -v "$dir:/gate-run" "$cleanup_helper_image" sh -ec '
      test -d /gate-run
      for path in /gate-run/runner-secrets /gate-run/runner-workspace; do
        if test -e "$path"; then chown -R "$HOST_UID:$HOST_GID" "$path"; fi
      done
    ' >/dev/null 2>"$dir/cleanup-ownership.err"
    if [[ $? -ne 0 ]]; then cleanup_complete=false; diag_emit 'cleanup_ownership_remediation=false'; fi
    if [[ -d "$runner_secret_root" && ( ! -O "$runner_secret_root" || ! -r "$runner_secret_root" || ! -x "$runner_secret_root" ) ]]; then cleanup_complete=false; diag_emit 'cleanup_secret_root_host_access=false'; fi
    if [[ -d "$runner_workdir" && ( ! -O "$runner_workdir" || ! -r "$runner_workdir" || ! -x "$runner_workdir" ) ]]; then cleanup_complete=false; diag_emit 'cleanup_workdir_host_access=false'; fi
  fi
  remove_project_resources "$project" || cleanup_complete=false
  if [[ ! "$dir" =~ ^/tmp/nerocd-runtime-compose\.[A-Za-z0-9]{8}$ || ! -d "$dir" || -L "$dir" ]]; then
    cleanup_complete=false; diag_emit 'cleanup_temp_guard=false'
  elif ! rm -rf -- "$dir"; then
    cleanup_complete=false; diag_emit 'cleanup_temp_remove=false'
  fi
  if [[ -e "$dir" ]]; then cleanup_complete=false; diag_emit 'cleanup_temp_residual=true'; fi
  record "cleanup_complete=$cleanup_complete"
  if [[ "$cleanup_complete" != true && $original -eq 0 ]]; then final=1; fi
  if [[ "$pass" == true && $original -eq 0 && "$cleanup_complete" == true ]]; then record 'PASS: real typed Compose A/B/C rollback and restart gate'; fi
  printf 'runtime compose evidence: %s\n' "$evidence"
  exit "$final"
}
trap cleanup EXIT; trap 'fail "unexpected command failure line $LINENO"' ERR
for x in docker curl jq git od; do command -v "$x" >/dev/null || fail "missing $x"; done; docker info >/dev/null || fail 'Docker unavailable'; [[ -S /var/run/docker.sock ]] || fail 'Docker socket unavailable'
socket_gid=$(docker_gid); [[ "$socket_gid" =~ ^[0-9]+$ ]] || fail 'Docker socket GID unavailable'; record "project=$project docker_gid=$socket_gid socket_root_equivalent=true"
docker build --pull -t "$image" "$root" >"$dir/build.log" 2>&1 || fail 'fresh server image build failed'
docker buildx version >/dev/null 2>&1 || fail 'Docker Buildx unavailable'
select_engine_builder || fail 'no unambiguous running engine-backed Buildx builder'
record "runner_builder_source=$builder_source driver=docker load=true"
docker buildx build --builder "$engine_builder" --load \
  --file "$root/acceptance/runtime-compose/RunnerDockerfile" \
  --build-arg "BASE=$image" --build-arg "DOCKER_GID=$socket_gid" --tag "$runner_image" \
  "$root/acceptance/runtime-compose" >"$dir/runner-build.log" 2>&1 || fail 'runner image build failed'
[[ $(docker image inspect --format '{{.Config.User}}' "$runner_image") == nerocd ]] || fail 'runner image user changed'
docker run --rm --network none --entrypoint /bin/sh "$runner_image" -ec 'test "$(id -u)" = 10001; for tool in nerocd git ssh docker wget; do command -v "$tool"; done; docker compose version' >"$dir/runner-tools.log" 2>&1 || fail 'runner image tools or uid unavailable'
docker build --pull -f "$root/acceptance/runtime-compose/GitDockerfile" -t "$git_image" "$root/acceptance/runtime-compose" >"$dir/git.log" 2>&1 || fail 'git fixture image build failed'
docker build --pull -f "$root/acceptance/runtime-compose/FixtureDockerfile" --build-arg VERSION=A --build-arg MODE=good --build-arg BUILD_NONCE="$suffix" -t "$fixture_a" "$root/acceptance/runtime-compose" >"$dir/a.log" 2>&1 || fail 'fixture A build failed'
docker build --pull -f "$root/acceptance/runtime-compose/FixtureDockerfile" --build-arg VERSION=B --build-arg MODE=good --build-arg BUILD_NONCE="$suffix" -t "$fixture_b" "$root/acceptance/runtime-compose" >"$dir/b.log" 2>&1 || fail 'fixture B build failed'
docker build --pull -f "$root/acceptance/runtime-compose/FixtureDockerfile" --build-arg VERSION=C --build-arg MODE=slow --build-arg BUILD_NONCE="$suffix" -t "$fixture_c" "$root/acceptance/runtime-compose" >"$dir/c.log" 2>&1 || fail 'fixture C build failed'
start_fixture_registry || fail 'fixture registry did not start safely'
publish_fixture_digest "$fixture_a" a || fail 'fixture A repository digest publication failed'; fixture_a_ref=$published_fixture_ref; fixture_a_registry_tag=$published_fixture_registry_tag; fixture_a_id=$published_fixture_id
publish_fixture_digest "$fixture_b" b || fail 'fixture B repository digest publication failed'; fixture_b_ref=$published_fixture_ref; fixture_b_registry_tag=$published_fixture_registry_tag; fixture_b_id=$published_fixture_id
publish_fixture_digest "$fixture_c" c || fail 'fixture C repository digest publication failed'; fixture_c_ref=$published_fixture_ref; fixture_c_registry_tag=$published_fixture_registry_tag; fixture_c_id=$published_fixture_id
for candidate in "$fixture_a_ref" "$fixture_b_ref" "$fixture_c_ref"; do
  docker run --rm --network none -v /var/run/docker.sock:/var/run/docker.sock --entrypoint docker "$runner_image" image inspect "$candidate" >/dev/null 2>"$dir/registry-runner-socket-inspect.err" || fail 'runner socket engine could not resolve fixture repository digest'
done
fixture_registry_diagnostic=verified
record 'fixture_registry loopback_only=true docker_assigned_port=true pinned_image=true image_id_not_trusted=true exact_repodigest=true fresh_pull=true runner_socket_resolution=true'
admin_email=admin@example.local admin_password=admin
if [[ "$runtime_profile" == production ]]; then
  owner_role="nerocd_owner_$suffix"; app_role="nerocd_app_$suffix"
  owner_password="o$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
  app_password="a$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
  admin_email="admin-$suffix@example.invalid"; admin_password="p$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
  printf 'postgres://%s:%s@postgres:5432/nerocd?sslmode=disable\n' "$owner_role" "$owner_password" >"$dir/owner-database-url"
  printf 'postgres://%s:%s@postgres:5432/nerocd?sslmode=disable\n' "$app_role" "$app_password" >"$dir/app-database-url"
  printf '%s\n' "$owner_password" >"$dir/postgres-password"
  chmod 0400 "$dir/owner-database-url" "$dir/app-database-url" "$dir/postgres-password"
  runtime_image_id=$(docker image inspect -f '{{.Id}}' "$image")
  NEROCD_RUNTIME_OWNER_DATABASE_URL_SECRET="$dir/owner-database-url"
  NEROCD_RUNTIME_APP_DATABASE_URL_SECRET="$dir/app-database-url"
  NEROCD_RUNTIME_POSTGRES_PASSWORD_SECRET="$dir/postgres-password"
  NEROCD_RUNTIME_OWNER_DATABASE_USER="$owner_role"
  NEROCD_RUNTIME_APP_DATABASE_USER="$app_role"
  NEROCD_RUNTIME_IMAGE_REF="local.invalid/nerocd@${runtime_image_id}"
  export NEROCD_RUNTIME_OWNER_DATABASE_URL_SECRET NEROCD_RUNTIME_APP_DATABASE_URL_SECRET NEROCD_RUNTIME_POSTGRES_PASSWORD_SECRET NEROCD_RUNTIME_OWNER_DATABASE_USER NEROCD_RUNTIME_APP_DATABASE_USER NEROCD_RUNTIME_IMAGE_REF
  db_user=$owner_role
fi
compose run --rm --no-deps git_init >"$dir/git-init.log" 2>&1 || fail 'git fixture initialization failed'
compose up -d --wait postgres server git proxy >"$dir/up.log" 2>&1 || fail 'control stack failed'
server_port=$(compose port server 8080 | tail -1); base="http://127.0.0.1:${server_port##*:}"
proxy_port=$(compose port proxy 8081 | tail -1); proxy_control="http://127.0.0.1:${proxy_port##*:}/__control"
http(){ local method=$1 url=$2 token=$3 body=$4 out=$5; local -a args=(-sS --max-time 15 -o "$out" -w '%{http_code}' -X "$method" -H 'Content-Type: application/json'); [[ -z "$token" ]] || args+=(-H "Authorization: Bearer $token"); args+=(--data "$body" "$url"); curl "${args[@]}"; }
proxy_status(){ curl -fsS --max-time 5 "$proxy_control/status"; }
proxy_post(){ curl -sS --max-time 5 -o /dev/null -w '%{http_code}' -X POST "$proxy_control/$1"; }
if [[ "$runtime_profile" == production ]]; then
  printf '%s\n' "$admin_password" | compose run --rm --no-deps --entrypoint nerocd server bootstrap-admin --email "$admin_email" --name 'Dogfood Admin' --password-stdin >"$dir/bootstrap.log" 2>&1 || fail 'production bootstrap-admin failed'
fi
code=$(http POST "$base/api/v1/sessions" '' "$(jq -cn --arg email "$admin_email" --arg password "$admin_password" '{email:$email,password:$password}')" "$dir/session.json"); [[ "$code" == 201 ]] || fail 'admin session failed'; admin=$(jq -er .token "$dir/session.json")
code=$(http POST "$base/api/v1/projects" "$admin" "$(jq -cn --arg n "compose-$suffix" '{name:$n,description:"ephemeral compose acceptance"}')" "$dir/project.json"); [[ "$code" == 201 ]] || fail 'project create failed'; project_id=$(jq -er .id "$dir/project.json")
code=$(http POST "$base/api/v1/runners/register" "$admin" '{"id":"runner_compose","name":"runner_compose","tags":["compose-runtime"],"capabilities":["compose-deploy"]}' "$dir/runner.json"); [[ "$code" == 201 ]] || fail 'runner registration failed'; token=$(jq -er .token "$dir/runner.json")
printf '%s\n' "$token" | compose run --rm --no-deps credential_init >"$dir/credential.log" 2>&1 || fail 'runner credential initialization failed'
compose up -d runner >"$dir/runner.log" 2>&1 || fail 'runner start failed'; runner_cid=$(compose ps -q runner); [[ -n "$runner_cid" ]] || fail 'runner unavailable'
uid=$(compose exec -T runner id -u); [[ "$uid" == 10001 ]] || fail 'runner is not non-root'; if ! compose exec -T runner sh -ec 'id; ls -ln /var/run/docker.sock; test -S /var/run/docker.sock; docker version >/dev/null' >"$dir/socket.log" 2>&1; then record 'socket_check=false'; fail 'non-root runner cannot use declared Docker socket'; fi
health_network_json=$(docker network inspect "$health_network") || fail 'health network inspection failed'
health_cidr=$(jq -er '.[0] | select(.Internal == true) | [.IPAM.Config[]?.Subnet | select(type == "string" and test("^[0-9]+(\\.[0-9]+){3}/[0-9]+$"))] | if length == 1 then .[0] else error("expected one IPv4 subnet") end' <<<"$health_network_json") || fail 'health network is not one internal IPv4 subnet'
fixture_health_body(){ compose exec -T runner sh -ec 'exec wget -q -T 5 -O - "$1"' sh "$health_url"; }
record "health_fixture network_internal=true network=$health_network cidr=$health_cidr host=$health_host port=8080 host_publish=false"
git_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$(compose ps -q git)"); fp=$(docker run --rm --network none -v "$(docker volume ls -q --filter label=com.docker.compose.project=$project --filter label=com.docker.compose.volume=git-data | head -1):/git:ro" "$git_image" sh -c 'ssh-keygen -lf /git/keys/host.pub' | awk '{print $2}')
repo=$(jq -cn --arg p "$project_id" '{project_id:$p,name:"compose-fixture",url:"ssh://git@git:2222/repo.git",provider:"git",default_ref:"main"}'); code=$(http POST "$base/api/v1/repositories" "$admin" "$repo" "$dir/repo.json"); [[ "$code" == 201 ]] || fail 'repository create failed'; repo_id=$(jq -er .id "$dir/repo.json")
policy=$(jq -cn --arg p "$project_id" --arg h git --arg c "$git_ip/32" --arg f "$fp" '{project_id:$p,configuration_id:"cfg_compose_runtime",policy:{version:1,state:"configured",mode:"internal",allowed_schemes:["ssh"],allowed_hosts:[$h],allowed_cidrs:[$c],ssh_host_fingerprints:[$f],credential_reference_id:"cred_git_deploy",allow_internal:true}}'); code=$(http PUT "$base/api/v1/repositories/$repo_id/policy" "$admin" "$policy" "$dir/policy.json"); [[ "$code" == 200 ]] || fail 'repository policy failed'
service=$(jq -cn --arg p "$project_id" --arg r "$repo_id" '{project_id:$p,name:"compose-fixture",repository_id:$r,compose_path:"compose.yaml"}'); code=$(http POST "$base/api/v1/services" "$admin" "$service" "$dir/service.json"); [[ "$code" == 201 ]] || fail 'service create failed'; service_id=$(jq -er .id "$dir/service.json")
env=$(jq -cn --arg s "$service_id" --arg cp "$compose_project" --arg url "$health_url" --arg host "$health_host" --arg cidr "$health_cidr" '{service_id:$s,name:"runtime",runner_selector:["compose-runtime"],compose_project:$cp,timeout_seconds:40,rollback_safe:true,health_policy:{url:$url,allowed_hosts:[$host],allowed_cidrs:[$cidr],allowed_ports:[8080],allow_http:true,interval_seconds:1,timeout_seconds:10,expected_status:200},secret_bindings:[{name:"git",provider:"runner_file",reference:"cred_git_deploy",target:"env:GIT_SSH_KEY",required:true,version:"v1",fingerprint:"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{name:"application",provider:"runner_file",reference:"app_secret",target:"file:app_secret",required:true,version:"v1",fingerprint:"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}' ); code=$(http POST "$base/api/v1/environments" "$admin" "$env" "$dir/env.json"); [[ "$code" == 201 ]] || fail 'environment create failed'; env_id=$(jq -er .id "$dir/env.json")
deploy(){ local label=$1; code=$(http POST "$base/api/v1/revisions" "$admin" "$(jq -cn --arg s "$service_id" '{service_id:$s,requested_ref:"main"}')" "$dir/rev-$label.json"); [[ "$code" == 201 ]] || fail "revision $label failed"; local rev=$(jq -er .id "$dir/rev-$label.json"); deploy_existing "$label" "$rev"; }
deploy_existing(){ local label=$1 rev=$2; code=$(http POST "$base/api/v1/deployments" "$admin" "$(jq -cn --arg e "$env_id" --arg r "$rev" --arg k "compose-$label" '{environment_id:$e,desired_revision_id:$r,idempotency_key:$k}')" "$dir/dep-$label.json"); [[ "$code" == 201 ]] || fail "deployment $label failed"; printf '%s %s\n' "$rev" "$(jq -er .id "$dir/dep-$label.json")"; }
cancel_deployment(){ local id=$1 request=$2; code=$(http POST "$base/api/v1/deployments/cancel" "$admin" "$(jq -cn --arg d "$id" --arg r "$request" '{deployment_id:$d,request_id:$r}')" "$dir/cancel-$id.json"); [[ "$code" == 200 ]] || fail "deployment cancellation failed code=$code"; }
origin_commit(){ compose exec -T git sh -ec "git config --global --add safe.directory /git/repo.git; git --git-dir=/git/repo.git rev-parse refs/heads/$1"; }
revision_observation(){ local id=$1 commit=$2 hash=$3 image=$4; code=$(http GET "$base/api/v1/revisions?service_id=$service_id" "$admin" '' "$dir/revisions-$id.json"); [[ "$code" == 200 ]] || fail "revision observation failed"; jq -e --arg id "$id" --arg c "$commit" --arg h "$hash" --arg i "$image" '.items[] | select(.id==$id and .provenance_state=="resolved" and .provenance_resolved==true and .git_commit==$c and .compose_hash==$h and .content_identity==($c+":"+$h) and .image_digests==[$i])' "$dir/revisions-$id.json" >/dev/null || fail "immutable revision observation mismatched"; }
provenance_receipt(){ local dep=$1 rev=$2 commit=$3 hash=$4 image=$5 expected=$6 observed duplicated; observed=$(psql_query "select count(*) from provenance_resolutions p join audit_events a on a.id=p.audit_id where p.deployment_id='$dep' and p.revision_id='$rev' and p.git_commit='$commit' and p.compose_hash='$hash' and p.content_identity='$commit:$hash' and p.image_digests=array['$image'] and a.action='runner.deployment.provenance.resolve'"); duplicated=$(psql_query "select count(*) from (select attempt from provenance_resolutions where deployment_id='$dep' group by attempt having count(*)<>1) x"); [[ "$observed" == "$expected" && "$duplicated" == 0 ]] || fail "provenance receipt count dep=$dep got=$observed want=$expected duplicate_attempts=$duplicated"; }
expected_provenance(){ local name commit image source envfile override raw canonical; name=$1; commit=$2; image=$3; source="$dir/compose-$name.yaml"; envfile="$dir/compose-$name.env"; override="$dir/compose-$name-secrets.yaml"; compose exec -T git sh -ec "git config --global --add safe.directory /git/repo.git; git --git-dir=/git/repo.git show '$commit:compose.yaml'" >"$source" || fail "fixture compose extraction failed"; printf '%s=%s\n' NEROCD_DEPLOYMENT_REVISION "$commit" >"$envfile"; printf 'secrets:\n  app_secret:\n    file: %s\n' "$runner_workdir/expected-secret" >"$override"; raw=$(NEROCD_DEPLOYMENT_REVISION="$commit" docker compose --project-name "$compose_project" --env-file "$envfile" --file "$source" --file "$override" config --format json) || fail "fixture canonical compose failed"; canonical=$(printf '%s' "$raw" | jq -cS 'del(.name) | .secrets.app_secret.file = "nerocd-secret://app_secret"') || fail "fixture canonical JSON failed"; printf '%s' "$canonical" | shasum -a 256 | awk '{print "sha256:"$1}'; grep -Eq '@sha256:[0-9a-f]{64}$' "$source" || fail "fixture source lacks immutable digest pin"; ! grep -Eiq '(^|[^[:alnum:]_])(build|pull)([^[:alnum:]_]|$)' "$source" || fail "fixture source contains forbidden build or pull"; }
status(){ psql_query "select status from deployments where id='$1'"; }
wait_status(){ local id=$1 want=$2 deadline=$((SECONDS+90)) got=''; while ((SECONDS<deadline)); do got=$(status "$id" || true); [[ "$got" == "$want" ]] && return; sleep .5; done; fail "deployment $id status=$got want=$want"; }
public_status(){ local id=$1; code=$(http GET "$base/api/v1/deployments?environment_id=$env_id" "$admin" '' "$dir/deployments-$id.json"); [[ "$code" == 200 ]] || fail "public deployment observation failed"; jq -er --arg id "$id" '.items[] | select(.id==$id) | .status' "$dir/deployments-$id.json"; }
wait_public_status(){ local id=$1 want=$2 deadline=$((SECONDS+90)) got=''; while ((SECONDS<deadline)); do got=$(public_status "$id" || true); [[ "$got" == "$want" ]] && return; sleep .25; done; fail "public deployment $id status=$got want=$want"; }
rollback_lifecycle(){
  local source=$1 child=$2 source_status=$3 child_status=$4 source_attempts=$5 rows audits active children child_run expected_rows expected_audits
  child_run=failed; [[ "$child_status" == rolled_back ]] && child_run=succeeded
  children=$(psql_query "select count(*) from deployments where rollback_of_id='$source'")
  [[ "$children" == 1 ]] || fail "rollback child cardinality source=$source count=$children"
  rows=$(psql_query "select d.id||':'||a.attempt||':'||d.status||':'||r.status||':'||l.status||':'||a.status from deployments d join task_runs r on r.id=d.task_run_id join run_leases l on l.run_id=r.id join deployment_attempts a on a.lease_id=l.id where d.id in ('$source','$child') order by case when d.id='$source' then 0 else 1 end,a.attempt")
  expected_rows="$source:1:$source_status:failed:failed:failed
$child:1:$child_status:$child_run:$child_run:$child_run"
  if [[ "$source_attempts" == 2 ]]; then
    expected_rows="$source:1:$source_status:failed:expired:failed
$source:2:$source_status:failed:failed:failed
$child:1:$child_status:$child_run:$child_run:$child_run"
  fi
  [[ "$rows" == "$expected_rows" ]] || fail "rollback lifecycle exact rows mismatch got=[$rows] want=[$expected_rows]"
  active=$(psql_query "select count(*) from run_leases l join task_runs r on r.id=l.run_id where r.id in (select task_run_id from deployments where id in ('$source','$child')) and l.status='active'")
  [[ "$active" == 0 ]] || fail "rollback left active leases=$active"
  expected_audits="deployment.create:$source
runner.deployment.transition:$source
runner.deployment.provenance.resolve:$source
runner.deployment.transition:$source
runner.deployment.transition:$source"
  [[ "$source_attempts" == 1 ]] || expected_audits="$expected_audits
runner.deployment.provenance.resolve:$source"
  expected_audits="$expected_audits
runner.deployment.failed:$source
runner.deployment.rollback_queued:$child
runner.deployment.transition:$child
runner.deployment.provenance.resolve:$child
runner.deployment.transition:$child
runner.deployment.transition:$child
runner.deployment.transition:$child"
  audits=$(psql_query "select action||':'||target_id from audit_events where target_id in ('$source','$child') order by created_at,id")
  [[ "$audits" == "$expected_audits" ]] || fail "rollback audit exact order mismatch got=[$audits] want=[$expected_audits]"
  record "rollback_lifecycle source=$source child=$child exact_rows=$(tr '\n' ',' <<<\"$rows\") exact_audits=$(tr '\n' ',' <<<\"$audits\")"
}
a_commit=$(origin_commit main); b_commit=$(origin_commit fixture-b); c_commit=$(origin_commit fixture-c); d_commit=$(origin_commit fixture-d); a_hash=$(expected_provenance a "$a_commit" "$fixture_a_id"); b_hash=$(expected_provenance b "$b_commit" "$fixture_b_id"); c_hash=$(expected_provenance c "$c_commit" "$fixture_c_id"); d_hash=$(expected_provenance d "$d_commit" "$fixture_c_id"); record "fixture_provenance a=$a_commit/$a_hash b=$b_commit/$b_hash c=$c_commit/$c_hash d=$d_commit/$d_hash images=repository_at_sha256"
read -r rev_a dep_a <<<"$(deploy a)"; wait_status "$dep_a" succeeded; revision_observation "$rev_a" "$a_commit" "$a_hash" "$fixture_a_ref"; provenance_receipt "$dep_a" "$rev_a" "$a_commit" "$a_hash" "$fixture_a_ref" 1; [[ "$(fixture_health_body)" == A ]] || fail 'A health body failed'; target_a=$(docker ps -q --filter "ancestor=$fixture_a" --filter "label=com.docker.compose.project=$compose_project" --filter 'label=com.docker.compose.service=fixture'); [[ -n "$target_a" ]] || fail 'A target missing for file secret check'; configured_uid=$(docker inspect -f '{{.Config.User}}' "$target_a"); [[ "$configured_uid" == "10001:10001" ]] || fail 'A target configured user is not 10001:10001'; runtime_uid=$(docker exec "$target_a" id -u); [[ "$runtime_uid" == 10001 ]] || fail 'A target runtime UID is not 10001'; secret_target=$(docker inspect -f '{{range .Mounts}}{{if eq .Type "bind"}}{{.Source}} {{.Destination}}{{"\n"}}{{end}}{{end}}' "$target_a" | awk -v source="$runner_secret_root/app_secret" '$1 == source {print $2}'); [[ "$secret_target" == /run/secrets/app_secret ]] || fail 'A validated file secret bind mount was not present'; docker exec "$target_a" sh -ec 'test -r "$1" && test -s "$1"' sh "$secret_target" || fail 'A file secret was not readable after success'; ! find "$runner_workdir" -mindepth 1 -maxdepth 1 -name '.nerocd-compose-secrets-*' -print -quit | grep -q . || fail 'A attempt-local file secret descriptor directory was not cleaned'; pointer=$(psql_query "select current_healthy_revision_id from environments where id='$env_id'"); [[ "$pointer" == "$rev_a" ]] || fail 'A healthy pointer failed'; record "A=$rev_a deployment=$dep_a health=A immutable_provenance=true fixture_configured_user=10001:10001 fixture_runtime_uid=10001 file_secret=health_read_persistent_source_override_cleaned"
compose run --rm --no-deps git_advance_b >"$dir/advance-b.log"; revision_observation "$rev_a" "$a_commit" "$a_hash" "$fixture_a_ref"; provenance_receipt "$dep_a" "$rev_a" "$a_commit" "$a_hash" "$fixture_a_ref" 1; read -r rev_b dep_b <<<"$(deploy b)"; wait_status "$dep_b" succeeded; revision_observation "$rev_b" "$b_commit" "$b_hash" "$fixture_b_ref"; provenance_receipt "$dep_b" "$rev_b" "$b_commit" "$b_hash" "$fixture_b_ref" 1; [[ "$(fixture_health_body)" == B ]] || fail 'B health body failed'; pointer=$(psql_query "select current_healthy_revision_id from environments where id='$env_id'"); [[ "$pointer" == "$rev_b" ]] || fail 'B healthy pointer failed'; record "B=$rev_b deployment=$dep_b health=B immutable_provenance=true"
compose_trace(){ local after=$1; compose exec -T runner sh -ec "test -f /journal/compose-trace.log && tail -n +$((after+1)) /journal/compose-trace.log"; }
compose_trace_lines(){ compose exec -T runner sh -ec 'test -f /journal/compose-trace.log && wc -l < /journal/compose-trace.log || true'; }
wait_runner_barrier(){ local name=$1 deadline=$((SECONDS+45)); while ((SECONDS<deadline)); do compose exec -T runner sh -ec "test -f /journal/compose-barrier-$name-entered" >/dev/null 2>&1 && return; sleep .2; done; fail "runner did not reach $name barrier"; }
pre_trace=$(compose_trace_lines); target_pre=$(docker ps -q --filter "ancestor=$fixture_b" --filter "label=com.docker.compose.project=$compose_project" --filter 'label=com.docker.compose.service=fixture'); [[ -n "$target_pre" ]] || fail 'pre-cancel missing B target'; created_pre=$(docker inspect -f '{{.Created}}' "$target_pre"); compose exec -T runner sh -ec 'rm -f /journal/compose-barrier-config-entered; : >/journal/compose-barrier-config'; read -r rev_pre dep_pre <<<"$(deploy preapply-cancel)"; wait_runner_barrier config; cancel_deployment "$dep_pre" preapply-cancel-receipt; code=$(http POST "$base/api/v1/deployments/cancel" "$admin" "$(jq -cn --arg d "$dep_pre" '{deployment_id:$d,request_id:"preapply-cancel-receipt"}')" "$dir/cancel-replay.json"); [[ "$code" == 200 ]] || fail "pre-cancel replay code=$code"; code=$(http POST "$base/api/v1/deployments/cancel" "$admin" "$(jq -cn --arg d "$dep_pre" '{deployment_id:$d,request_id:"changed-receipt"}')" "$dir/cancel-conflict.json"); [[ "$code" == 409 ]] || fail "pre-cancel changed receipt code=$code"; wait_status "$dep_pre" canceled; sleep 5; pre_rows=$(psql_query "select d.status||':'||r.status||':'||l.status||':'||a.status from deployments d join task_runs r on r.id=d.task_run_id join run_leases l on l.run_id=r.id join deployment_attempts a on a.lease_id=l.id where d.id='$dep_pre'"); [[ "$pre_rows" == 'canceled:canceled:canceled:canceled' ]] || fail "pre-cancel lifecycle=$pre_rows"; pre_trace_after=$(compose_trace "$pre_trace"); [[ $(awk '$3=="action=compose_up" {n++} END {print n+0}' <<<"$pre_trace_after") == 0 ]] || fail "pre-apply cancellation mutated target trace=[$pre_trace_after]"; target_after=$(docker ps -q --filter "ancestor=$fixture_b" --filter "label=com.docker.compose.project=$compose_project" --filter 'label=com.docker.compose.service=fixture'); [[ "$target_pre" == "$target_after" && "$(docker inspect -f '{{.Created}}' "$target_after")" == "$created_pre" ]] || fail 'pre-cancel mutated B target'; runner_live=$(compose ps -q runner); [[ -n "$runner_live" && "$(docker inspect -f '{{.State.Running}}' "$runner_live")" == true ]] || fail 'runner did not survive pre-cancel'; pre_audits=$(psql_query "select action from audit_events where target_id='$dep_pre' order by created_at,id"); [[ "$pre_audits" == $'deployment.create\nrunner.deployment.transition\ndeployment.cancel' ]] || fail "pre-cancel audit order=$pre_audits"; pre_audit_count=$(psql_query "select count(*) from audit_events where target_id='$dep_pre'"); sleep 2; [[ "$(psql_query "select count(*) from audit_events where target_id='$dep_pre'")" == "$pre_audit_count" ]] || fail 'pre-cancel produced later writes'; record "preapply_cancel deployment=$dep_pre terminal=canceled exact_replay_conflict=true zero_up=true runner_alive=true"
compose run --rm --no-deps git_advance_c >"$dir/advance-c-during.log"; during_trace=$(compose_trace_lines); target_during_before=$(docker ps -q --filter "ancestor=$fixture_b" --filter "label=com.docker.compose.project=$compose_project" --filter 'label=com.docker.compose.service=fixture'); created_during_before=$(docker inspect -f '{{.Created}}' "$target_during_before"); compose exec -T runner sh -ec 'rm -f /journal/compose-barrier-up-entered /journal/compose-barrier-up-descendant.pid; : >/journal/compose-barrier-up'; read -r rev_during dep_during <<<"$(deploy during-apply-cancel)"; wait_runner_barrier up; wait_public_status "$dep_during" applying; target_during_applying=$(docker ps -q --filter "ancestor=$fixture_b" --filter "label=com.docker.compose.project=$compose_project" --filter 'label=com.docker.compose.service=fixture'); [[ "$target_during_before" == "$target_during_applying" ]] || fail 'during-cancel mutated target before blocked up'; cancel_deployment "$dep_during" during-apply-cancel-receipt; code=$(http POST "$base/api/v1/deployments/cancel" "$admin" "$(jq -cn --arg d "$dep_during" '{deployment_id:$d,request_id:"during-apply-cancel-receipt"}')" "$dir/cancel-during-replay.json"); [[ "$code" == 200 ]] || fail "during-cancel replay code=$code"; code=$(http POST "$base/api/v1/deployments/cancel" "$admin" "$(jq -cn --arg d "$dep_during" '{deployment_id:$d,request_id:"during-apply-changed"}')" "$dir/cancel-during-conflict.json"); [[ "$code" == 409 ]] || fail "during-cancel changed receipt code=$code"; wait_status "$dep_during" rolled_back; child_during=$(psql_query "select id from deployments where rollback_of_id='$dep_during'"); child_during_count=$(psql_query "select count(*) from deployments where rollback_of_id='$dep_during'"); [[ "$child_during_count" == 1 && -n "$child_during" && "$(status "$child_during")" == rolled_back ]] || fail 'during-cancel rollback child cardinality or terminal state failed'; during_rows=$(psql_query "select d.id||':'||d.status||':'||r.status||':'||l.status||':'||a.status from deployments d join task_runs r on r.id=d.task_run_id join run_leases l on l.run_id=r.id join deployment_attempts a on a.lease_id=l.id where d.id in ('$dep_during','$child_during') order by case when d.id='$dep_during' then 0 else 1 end,a.attempt"); expected_during_rows="$dep_during:rolled_back:failed:failed:failed
$child_during:rolled_back:succeeded:succeeded:succeeded"; [[ "$during_rows" == "$expected_during_rows" ]] || fail "during-cancel lifecycle=$during_rows"; child_during_rev=$(psql_query "select desired_revision_id from deployments where id='$child_during'"); [[ "$child_during_rev" == "$rev_b" ]] || fail 'during-cancel child did not restore B revision'; pointer=$(psql_query "select current_healthy_revision_id from environments where id='$env_id'"); [[ "$pointer" == "$rev_b" && "$(fixture_health_body)" == B ]] || fail 'during-cancel did not restore B health/pointer'; desc_state=$(compose exec -T runner sh -ec 'p=$(cat /journal/compose-barrier-up-descendant.pid); if test ! -d /proc/$p; then echo gone; else awk "{print \$3}" /proc/$p/stat; fi'); [[ "$desc_state" == gone || "$desc_state" == Z ]] || fail "during-cancel descendant survived state=$desc_state"; during_trace_after=$(compose_trace "$during_trace"); [[ $(awk '$3=="action=compose_up" {n++} END {print n+0}' <<<"$during_trace_after") == 1 ]] || fail "during-cancel expected exactly one blocked source compose up trace=[$during_trace_after]"; c_targets=$(docker ps -aq --filter "ancestor=$fixture_c" --filter "label=com.docker.compose.project=$compose_project" --filter 'label=com.docker.compose.service=fixture'); [[ -z "$c_targets" ]] || fail 'during-cancel left C target container'; during_audits=$(psql_query "select action||':'||target_id from audit_events where target_id in ('$dep_during','$child_during') order by created_at,id"); expected_during_audits="deployment.create:$dep_during
runner.deployment.transition:$dep_during
runner.deployment.provenance.resolve:$dep_during
runner.deployment.transition:$dep_during
deployment.cancel:$dep_during
runner.deployment.cancellation_rollback:$dep_during
runner.deployment.rollback_queued:$child_during
runner.deployment.transition:$child_during
runner.deployment.provenance.resolve:$child_during
runner.deployment.transition:$child_during
runner.deployment.transition:$child_during
runner.deployment.transition:$child_during"; [[ "$during_audits" == "$expected_during_audits" ]] || fail "during-cancel audit order=$during_audits"; during_writes_before=$(psql_query "select (select count(*)::text from audit_events where target_id in ('$dep_during','$child_during'))||(select count(*)::text from deployment_transitions where deployment_id in ('$dep_during','$child_during'))||(select count(*)::text from run_logs where run_id in (select task_run_id from deployments where id in ('$dep_during','$child_during')))||(select count(*)::text from run_artifacts where run_id in (select task_run_id from deployments where id in ('$dep_during','$child_during')))"); sleep 2; during_writes_after=$(psql_query "select (select count(*)::text from audit_events where target_id in ('$dep_during','$child_during'))||(select count(*)::text from deployment_transitions where deployment_id in ('$dep_during','$child_during'))||(select count(*)::text from run_logs where run_id in (select task_run_id from deployments where id in ('$dep_during','$child_during')))||(select count(*)::text from run_artifacts where run_id in (select task_run_id from deployments where id in ('$dep_during','$child_during')))"); [[ "$during_writes_before" == "$during_writes_after" ]] || fail 'during-cancel produced late writes'; runner_live=$(compose ps -q runner); [[ -n "$runner_live" && "$(docker inspect -f '{{.State.Running}}' "$runner_live")" == true ]] || fail 'runner did not survive during-cancel'; record "during_apply_cancel deployment=$dep_during child=$child_during applying_observed=true group_terminated=true source_child=rolled_back pointer=B no_late_writes=true"
trace_before=$(compose_trace_lines); compose run --rm --no-deps git_advance_c >"$dir/advance-c.log"; read -r rev_c dep_c <<<"$(deploy_existing c "$rev_during")"; wait_public_status "$dep_c" verifying; attempt1=$(psql_query "select attempt||':'||status from deployment_attempts where deployment_id='$dep_c' order by attempt"); [[ "$attempt1" == '1:active' ]] || fail "C attempt1 was not active at verify=$attempt1"; target_before=$(docker ps -q --filter "ancestor=$fixture_c" --filter "label=com.docker.compose.project=$compose_project" --filter 'label=com.docker.compose.service=fixture'); [[ -n "$target_before" ]] || fail 'C verify had no C target'; created_before=$(docker inspect -f '{{.Created}}' "$target_before"); labels_before=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}/{{index .Config.Labels "com.docker.compose.config-hash"}}' "$target_before"); trace_before_kill=$(compose_trace "$trace_before"); [[ $(awk '$3=="action=compose_up" {n++} END {print n+0}' <<<"$trace_before_kill") == 1 ]] || fail "C baseline must contain exactly one compose up trace=[$trace_before_kill]"; awk 'BEGIN {want[1]="compose_config"; want[2]="image_inspect"; want[3]="compose_up"; n=0} {sub(/^action=/,"",$3); if ($3==want[n+1]) n++} END {exit n==3 ? 0 : 1}' <<<"$trace_before_kill" || fail "C baseline trace lacks ordered config/inspect/up trace=[$trace_before_kill]"; record "C_before_kill public_status=verifying attempt=$attempt1 container=$target_before created=$created_before labels=$labels_before trace=$(tr '\n' ';' <<<\"$trace_before_kill\")"; docker kill "$runner_cid" >/dev/null || fail 'runner kill after apply failed'; compose up -d runner >/dev/null; runner_cid=$(compose ps -q runner); sleep 2; [[ "$(public_status "$dep_c")" == verifying ]] || fail 'C restart did not remain verifying before health'; target_after=$(docker ps -aq --filter "label=com.docker.compose.project=$compose_project" --filter 'label=com.docker.compose.service=fixture'); created_after=$(docker inspect -f '{{.Created}}' "$target_after"); labels_after=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}/{{index .Config.Labels "com.docker.compose.config-hash"}}' "$target_after"); trace_after_restart=$(compose_trace "$trace_before"); [[ "$target_before" == "$target_after" && "$created_before" == "$created_after" && "$labels_before" == "$labels_after" ]] || fail 'restart reconciliation mutated C target before verification'; [[ $(awk '$3=="action=compose_up" {n++} END {print n+0}' <<<"$trace_after_restart") == 1 ]] || fail "restart added compose mutation trace=[$trace_after_restart]"; [[ "$trace_after_restart" == "$trace_before_kill" || "$trace_after_restart" == "$trace_before_kill"$'\n'* ]] || fail "restart trace did not preserve C baseline trace=[$trace_after_restart]"; record "C_after_restart public_status=verifying same_container=true no_second_up=true trace=$(tr '\n' ';' <<<\"$trace_after_restart\")"
wait_status "$dep_c" rolled_back; revision_observation "$rev_c" "$c_commit" "$c_hash" "$fixture_c_ref"; provenance_receipt "$dep_c" "$rev_c" "$c_commit" "$c_hash" "$fixture_c_ref" 2; [[ "$(fixture_health_body)" == B ]] || fail 'C rollback did not restore B health'; child_c=$(psql_query "select id from deployments where rollback_of_id='$dep_c'"); [[ -n "$child_c" && "$(status "$child_c")" == rolled_back ]] || fail 'C rollback child did not settle'; rollback_lifecycle "$dep_c" "$child_c" rolled_back rolled_back 2; child_c_rev=$(psql_query "select desired_revision_id from deployments where id='$child_c'"); [[ "$child_c_rev" == "$rev_b" ]] || fail 'C rollback child did not use B revision'; revision_observation "$child_c_rev" "$b_commit" "$b_hash" "$fixture_b_ref"; provenance_receipt "$child_c" "$child_c_rev" "$b_commit" "$b_hash" "$fixture_b_ref" 1; pointer=$(psql_query "select current_healthy_revision_id from environments where id='$env_id'"); [[ "$pointer" == "$rev_b" ]] || fail 'C rollback changed B pointer'; record "C=$rev_c deployment=$dep_c child=$child_c source_child=rolled_back pointer=B reconcile_container_unchanged=true"
metrics_before=$(curl -fsS --max-time 10 -H "Authorization: Bearer $admin" "$base/metrics")
retry_before=$(awk '$1=="nerocd_runner_retry_count" {print $2}' <<<"$metrics_before"); renew_before=$(awk '$1=="nerocd_runner_renew_failures" {print $2}' <<<"$metrics_before"); [[ "$retry_before" =~ ^[0-9]+$ && "$renew_before" =~ ^[0-9]+$ ]] || fail 'missing pre-failure runner counters'
compose exec -T runner sh -ec 'rm -f /journal/compose-barrier-up-entered /journal/compose-barrier-up-descendant.pid; : >/journal/compose-barrier-up'
read -r rev_metric dep_metric <<<"$(deploy_existing telemetry "$rev_during")"; wait_runner_barrier up; wait_public_status "$dep_metric" applying
renew_requests_before=$(jq -r '.requests["/api/v1/runners/renew"] // 0' <<<"$(proxy_status)"); failed_before=$(jq -r '.failed_renewals // 0' <<<"$(proxy_status)"); [[ "$(proxy_post fail-renew)" == 204 ]] || fail 'could not arm one-shot transient renewal failure'
deadline=$((SECONDS+20)); failed_renewals=$failed_before; while ((SECONDS<deadline)); do proxy_state=$(proxy_status); failed_renewals=$(jq -r '.failed_renewals // 0' <<<"$proxy_state"); renew_requests=$(jq -r '.requests["/api/v1/runners/renew"] // 0' <<<"$proxy_state"); [[ "$failed_renewals" == $((failed_before+1)) && "$renew_requests" -ge $((renew_requests_before+2)) ]] && break; sleep .2; done; [[ "$failed_renewals" == $((failed_before+1)) && "$renew_requests" -ge $((renew_requests_before+2)) ]] || fail "renewal transient failure/recovery was not observed failed=$failed_renewals requests=$renew_requests"
cancel_deployment "$dep_metric" telemetry-renewal-receipt; wait_status "$dep_metric" rolled_back
deadline=$((SECONDS+15)); retry_reported=''; renew_reported=''; while ((SECONDS<deadline)); do metrics_now=$(curl -fsS --max-time 10 -H "Authorization: Bearer $admin" "$base/metrics"); retry_reported=$(awk '$1=="nerocd_runner_retry_count" {print $2}' <<<"$metrics_now"); renew_reported=$(awk '$1=="nerocd_runner_renew_failures" {print $2}' <<<"$metrics_now"); [[ "$retry_reported" == $((retry_before+1)) && "$renew_reported" == $((renew_before+1)) ]] && break; sleep .2; done; [[ "$retry_reported" == $((retry_before+1)) && "$renew_reported" == $((renew_before+1)) ]] || fail "runner counter telemetry deltas retry=$retry_before->$retry_reported renew=$renew_before->$renew_reported want=+1/+1"
record "runner_counter_fault transient_renew_failure=1 retry_recovery=1 telemetry_delta=1/1"
# Partition only the runner's API path while retaining the operator's direct
# HTTP view and PostgreSQL's DB-clock view.  The compose-up barrier makes the
# initial, fenced attempt unambiguously live before the outage starts.  This
# is deliberately a full minute (rather than one synthetic 502): the fixture
# lease TTL is 12s, so the reaper must expire the first authority and the
# recovered runner must claim a new one.
compose exec -T runner sh -ec 'rm -f /journal/compose-barrier-up-entered /journal/compose-barrier-up-descendant.pid; : >/journal/compose-barrier-up'
[[ "$(proxy_post event-hold/on)" == 204 ]] || fail 'could not hold committed compose-stage event response'
read -r rev_partition dep_partition <<<"$(deploy_existing partition "$rev_during")"
# The stage is deliberately before the first transition. Its reporter is
# waiting for its response when the proxy partition begins, so the 60-second
# outage exercises a durable, real runner event rather than a fabricated row.
wait_public_status "$dep_partition" assigned
read -r partition_run partition_lease partition_attempt partition_fence <<<"$(psql_query "select d.task_run_id||' '||a.lease_id||' '||a.attempt||' '||a.fence from deployments d join deployment_attempts a on a.deployment_id=d.id where d.id='$dep_partition' and a.attempt=1")"
[[ -n "$partition_run" && -n "$partition_lease" && "$partition_attempt" == 1 && -n "$partition_fence" ]] || fail 'partition did not bind initial fenced attempt'
partition_event_deadline=$((SECONDS+15)); partition_log_before=0; held_events=0
while ((SECONDS < partition_event_deadline)); do
  held_events=$(jq -r '.held_event_responses // 0' <<<"$(proxy_status)")
  partition_log_before=$(psql_query "select count(*) from run_logs where run_id='$partition_run' and message='compose-stage-resolution'")
  [[ "$held_events" == 1 && "$partition_log_before" == 1 ]] && break
  sleep .1
done
[[ "$held_events" == 1 && "$partition_log_before" == 1 ]] || fail "partition stage log was not committed and held exactly once before outage held=$held_events logs=$partition_log_before"
[[ "$(proxy_post outage/on)" == 204 ]] || fail 'could not enable runner-only API partition'
partition_deadline=$((SECONDS+60)); partition_expired=false
while ((SECONDS < partition_deadline)); do
  lease_state=$(psql_query "select status from run_leases where id='$partition_lease'" || true)
  [[ "$lease_state" == expired ]] && partition_expired=true
  # This public control-plane observation and DB query remain available while
  # the proxy rejects only /api/v1/runners/* traffic.
  public_status "$dep_partition" >/dev/null || fail 'operator observation was unavailable during runner-only partition'
  sleep 1
done
partition_final_lease=$(psql_query "select 'status='||status||',expires_at='||to_char(expires_at at time zone 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.MS\"Z\"')||',db_now='||to_char(clock_timestamp() at time zone 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.MS\"Z\"') from run_leases where id='$partition_lease'" || true)
partition_reaper=$(psql_query "select 'active_leases='||count(*)||',expired_leases='||(select count(*) from run_leases where status='expired') from run_leases where status='active'" || true)
record "partition_lease_final=$partition_final_lease reaper=$partition_reaper"
[[ "$partition_expired" == true && "$partition_final_lease" == status=expired,* ]] || fail 'DB-clock lease expiry was not observed during 60s partition'
stale_code=$(http GET "$base/api/v1/runners/deployments/status?deployment_id=$dep_partition&run_id=$partition_run&lease_id=$partition_lease&attempt=$partition_attempt&fence=$partition_fence" "$token" '' "$dir/partition-stale.json")
[[ "$stale_code" == 403 ]] || fail "expired partition fence status code=$stale_code want=403"
stale_log=$(http POST "$base/api/v1/runners/logs" "$token" "$(jq -cn --arg r "$partition_run" --arg l "$partition_lease" --arg f "$partition_fence" '{run_id:$r,lease_id:$l,attempt:1,fence:$f,sequence:99,stream:"system",message:"stale-partition-log",event_key:"partition-stale-log"}')" "$dir/partition-stale-log.json")
[[ "$stale_log" == 403 || "$stale_log" == 404 ]] || fail "expired partition fence append code=$stale_log want=403-or-404"
[[ "$(psql_query "select count(*) from run_logs where run_id='$partition_run' and message='stale-partition-log'")" == 0 ]] || fail 'expired partition fence appended a log'
[[ "$(proxy_post outage/off)" == 204 ]] || fail 'could not heal runner-only API partition'
[[ "$(proxy_post event-hold/off)" == 204 ]] || fail 'could not release committed compose-stage event response'
# A lost authority intentionally cancels the runner operation context.  The
# service container is therefore allowed to exit; recreate that one real
# runner (not the control plane or target) before releasing its durable
# compose barrier so the recovered claim is necessarily a fresh fence.
compose up -d runner >/dev/null || fail 'could not restart runner after partition authority loss'
partition_runner_deadline=$((SECONDS+20))
while ((SECONDS < partition_runner_deadline)); do
  partition_runner=$(compose ps -q runner)
  [[ -n "$partition_runner" && "$(docker inspect -f '{{.State.Running}}' "$partition_runner" 2>/dev/null || true)" == true ]] && break
  sleep .2
done
[[ -n "${partition_runner:-}" && "$(docker inspect -f '{{.State.Running}}' "$partition_runner" 2>/dev/null || true)" == true ]] || fail 'runner did not recover after partition authority loss'
compose exec -T runner sh -ec 'rm -f /journal/compose-barrier-up'
wait_status "$dep_partition" rolled_back
partition_attempts=$(psql_query "select count(*) from deployment_attempts where deployment_id='$dep_partition'")
partition_reclaimed=$(psql_query "select count(*) from deployment_attempts where deployment_id='$dep_partition' and attempt>1")
[[ "$partition_attempts" -ge 2 && "$partition_reclaimed" -ge 1 ]] || fail "partition did not reclaim expired work attempts=$partition_attempts reclaimed=$partition_reclaimed"
partition_logs=$(psql_query "select count(*)||':'||count(distinct event_key)||':'||count(distinct sequence)||':'||coalesce(max(sequence),0)||':'||count(*) filter (where message='compose-stage-resolution') from run_logs where run_id='$partition_run'")
IFS=: read -r partition_log_count partition_log_event_keys partition_log_sequences partition_log_max partition_stage_count <<<"$partition_logs"
[[ "$partition_log_count" -eq 2 && "$partition_log_count" == "$partition_log_event_keys" && "$partition_log_count" == "$partition_log_sequences" && "$partition_log_max" == "$partition_log_count" && "$partition_stage_count" == 2 ]] || fail "partition replay duplicated/noncontiguous stage logs=$partition_logs"
partition_log_after=$partition_log_count
partition_child=$(psql_query "select id from deployments where rollback_of_id='$dep_partition'")
[[ -n "$partition_child" && "$(status "$partition_child")" == rolled_back ]] || fail 'partition recovery did not create one settled rollback child'
[[ "$(psql_query "select count(*) from deployments where rollback_of_id='$dep_partition'")" == 1 ]] || fail 'partition recovery created duplicate rollback children'
pointer=$(psql_query "select current_healthy_revision_id from environments where id='$env_id'")
[[ "$pointer" == "$rev_b" && "$(fixture_health_body)" == B ]] || fail 'partition recovery did not preserve B target/pointer'
record "runner_api_partition seconds=60 runner_only=true db_clock_expired=true stale_fence_denied=true stale_log_append_denied=true committed_event_response_held=true reclaimed=true attempts=$partition_attempts log_rows=$partition_log_before-$partition_log_after log_sequence_contiguous_unique=true exact_stage_log_once_per_fenced_attempt=true source=$dep_partition child=$partition_child pointer=B"
record 'compose_trace_c_begin=true'; compose_trace "$trace_before" >>"$evidence"; record 'compose_trace_c_end=true'
# Make the immutable B artifact unavailable *before* D can create its rollback
# child.  Doing it after the source reports its failure races the runner's next
# claim, allowing the child to pass its preflight before this forced failure is
# established.
local_registry_remove_container "$fixture_registry_container_id" || fail 'fixture registry could not be removed before forced artifact unavailability'; fixture_registry_diagnostic=removed_for_forced_unavailability; record 'fixture_registry_removed_before_forced_unavailability=true'
ids=$(docker ps -aq --filter "ancestor=$fixture_b"); [[ -z "$ids" ]] || docker rm -f $ids >/dev/null || fail 'forced rollback fixture container removal failed'; docker image rm -f "$fixture_b_id" >/dev/null || fail 'forced rollback image removal failed'; docker image inspect "$fixture_b_id" >/dev/null 2>&1 && fail 'forced rollback image remained available'; record 'rollback_artifact_unavailable_before_d=true'
compose run --rm --no-deps git_advance_d >"$dir/advance-d.log"; read -r rev_d dep_d <<<"$(deploy d)"; wait_status "$dep_d" rollback_failed; revision_observation "$rev_d" "$d_commit" "$d_hash" "$fixture_c_ref"; provenance_receipt "$dep_d" "$rev_d" "$d_commit" "$d_hash" "$fixture_c_ref" 1; child_d=$(psql_query "select id from deployments where rollback_of_id='$dep_d'"); child_d_count=$(psql_query "select count(*) from deployments where rollback_of_id='$dep_d'"); [[ "$child_d_count" == 1 && -n "$child_d" && "$(status "$child_d")" == rollback_failed ]] || fail 'forced rollback failure did not have exactly one loud child'; rollback_lifecycle "$dep_d" "$child_d" rollback_failed rollback_failed 1; child_d_rev=$(psql_query "select desired_revision_id from deployments where id='$child_d'"); [[ "$child_d_rev" == "$rev_b" ]] || fail 'forced rollback failure child did not retain B provenance'; revision_observation "$child_d_rev" "$b_commit" "$b_hash" "$fixture_b_ref"; provenance_receipt "$child_d" "$child_d_rev" "$b_commit" "$b_hash" "$fixture_b_ref" 1; pointer=$(psql_query "select current_healthy_revision_id from environments where id='$env_id'"); [[ "$pointer" == "$rev_b" ]] || fail 'rollback failure changed healthy pointer'; record "rollback_failure source=$dep_d child=$child_d pointer=B operator_visible=true immutable_provenance=true"
anonymous_metrics=$(curl -sS --max-time 10 -o "$dir/metrics-anonymous.txt" -w '%{http_code}' "$base/metrics")
[[ "$anonymous_metrics" == 401 ]] || fail "anonymous metrics status=$anonymous_metrics want=401"
metrics_status=$(curl -sS --max-time 10 -H "Authorization: Bearer $admin" -o "$dir/metrics.txt" -w '%{http_code}' "$base/metrics")
[[ "$metrics_status" == 200 ]] || fail "authenticated metrics status=$metrics_status want=200"
retry_after=$(awk '$1=="nerocd_runner_retry_count" {print $2}' "$dir/metrics.txt"); renew_after=$(awk '$1=="nerocd_runner_renew_failures" {print $2}' "$dir/metrics.txt")
# The exact +1/+1 transient-retry observation above occurs before the
# subsequent deliberate 60s authority-loss test. That test intentionally
# restarts the process owning these since-start counters, so a final scrape
# must validate only their bounded numeric shape—not carry values across a
# process replacement.
[[ "$retry_after" =~ ^[0-9]+$ && "$renew_after" =~ ^[0-9]+$ ]] || fail "runner counter scrape after partition was malformed retry=$retry_after renew=$renew_after"
# The C attempt expiry/reclaim plus C/D rollback paths are real runner-driven
# lifecycle evidence, not fixture inserts. Metrics must preserve only the
# fixed labels and aggregate counts from that durable state.
rg -q '^nerocd_queue_depth [0-9]+$' "$dir/metrics.txt" || fail 'metrics omitted queue depth'
rg -q '^nerocd_leases\{state="expired"\} [1-9][0-9]*$' "$dir/metrics.txt" || fail 'metrics omitted expired lease signal'
rg -q '^nerocd_runner_journal_depth [0-9]+$' "$dir/metrics.txt" || fail 'metrics omitted runner journal signal'
rg -q '^nerocd_deployments\{status="rolled_back"\} [1-9][0-9]*$' "$dir/metrics.txt" || fail 'metrics omitted rolled-back deployment signal'
rg -q '^nerocd_deployments\{status="rollback_failed"\} [1-9][0-9]*$' "$dir/metrics.txt" || fail 'metrics omitted rollback-failed deployment signal'
rg -q '^nerocd_rollbacks\{outcome="succeeded"\} [1-9][0-9]*$' "$dir/metrics.txt" || fail 'metrics omitted successful rollback signal'
rg -q '^nerocd_rollbacks\{outcome="failed"\} [1-9][0-9]*$' "$dir/metrics.txt" || fail 'metrics omitted failed rollback signal'
! rg -Fq "$dep_c" "$dir/metrics.txt" && ! rg -Fq "$dep_d" "$dir/metrics.txt" && ! rg -Fq 'runner_compose' "$dir/metrics.txt" || fail 'metrics disclosed runtime identity'
record 'observability_runtime_scrape authenticated=true anonymous_denied=true queue_claim_expiry_reclaim=true runner_telemetry=true deployment_rollback=true renewal_retry_delta=1 renewal_failure_delta=1 fixed_labels=true'
compose logs --no-color >"$dir/logs.txt" || true; ! grep -Eiq 'BEGIN (OPENSSH|RSA|EC|DSA) PRIVATE KEY|Bearer [A-Za-z0-9._-]+|"fence"' "$dir/logs.txt" "$dir"/*.json || fail 'secret/token/fence disclosure scan failed'; ! grep -Eiq 'generic shell|/runners/complete' "$dir/logs.txt" || fail 'generic execution route evidence found'; record 'no_generic_shell_complete_build_pull_tag=true stable_project_labels_workspace=true'
# Dogfood intentionally reuses this *same* live control-plane/runner/target
# world for its archive and reboot phases.  Library mode is only for a parent
# shell that sourced this file; normal acceptance invocation retains its
# independent cleanup and PASS contract.
if [[ "${NEROCD_RUNTIME_COMPOSE_LIBRARY:-}" == 1 ]]; then
  record 'runtime_compose_library_handoff=true'
  trap - EXIT ERR
  return 0
fi
pass=true
