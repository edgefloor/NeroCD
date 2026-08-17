#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
compose_file="$repo_root/acceptance/runtime-spool/compose.yaml"
evidence=/tmp/nerocd-runner-spool-runtime.txt
runtime_dir=$(mktemp -d /tmp/nerocd-runtime-spool.XXXXXXXX)
suffix=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')
project="nerocd-spool-$suffix"
image="nerocd-runtime-spool:$suffix"
passed=false

: >"$evidence"
record() { printf '%s\n' "$*" >>"$evidence"; }
fail() { record "FAIL: $*"; printf 'runtime-spool-gate: %s\n' "$*" >&2; exit 1; }
compose() { NEROCD_RUNTIME_IMAGE="$image" docker compose --project-name "$project" --file "$compose_file" "$@"; }

cleanup() {
  status=$?
  set +e
  cleanup_failed=false
  if [[ "$project" =~ ^nerocd-spool-[0-9a-f]{12}$ ]]; then
    compose down --volumes --remove-orphans --rmi local --timeout 5 >/dev/null 2>&1
    while IFS= read -r resource; do [[ -n "$resource" ]] && docker container rm --force "$resource" >/dev/null 2>&1; done < <(docker container ls --all --quiet --filter "label=com.docker.compose.project=$project")
    while IFS= read -r resource; do [[ -n "$resource" ]] && docker volume rm --force "$resource" >/dev/null 2>&1; done < <(docker volume ls --quiet --filter "label=com.docker.compose.project=$project")
    while IFS= read -r resource; do [[ -n "$resource" ]] && docker network rm "$resource" >/dev/null 2>&1; done < <(docker network ls --quiet --filter "label=com.docker.compose.project=$project")
    remaining=$(docker container ls --all --quiet --filter "label=com.docker.compose.project=$project"; docker volume ls --quiet --filter "label=com.docker.compose.project=$project"; docker network ls --quiet --filter "label=com.docker.compose.project=$project")
    [[ -z "$remaining" ]] || cleanup_failed=true
  else
    cleanup_failed=true
  fi
  [[ "$image" =~ ^nerocd-runtime-spool:[0-9a-f]{12}$ ]] && docker image rm --force "$image" >/dev/null 2>&1
  case "$runtime_dir" in /tmp/nerocd-runtime-spool.*) rm -rf -- "$runtime_dir" ;; esac
  if [[ "$cleanup_failed" == true ]]; then
    status=1
    record "cleanup_complete=false"
    record "FAIL: isolated runtime-spool resources remain"
  else
    record "cleanup_complete=true"
  fi
  if [[ "$passed" == true && "$cleanup_failed" == false ]]; then
    record "PASS: real runner spool replay gate"
  elif [[ $status -eq 0 ]]; then
    status=1
    record "FAIL: gate exited without every assertion"
  fi
  printf 'runtime spool evidence: %s\n' "$evidence"
  exit "$status"
}
trap cleanup EXIT
trap 'status=$?; trap - ERR; fail "unexpected command failure at line $LINENO (exit $status)"' ERR

for tool in docker curl jq od; do command -v "$tool" >/dev/null 2>&1 || fail "required tool missing: $tool"; done
docker info >/dev/null 2>&1 || fail "Docker daemon unavailable"
docker compose version >/dev/null 2>&1 || fail "Docker Compose unavailable"

record "gate=real runner event/completion spool replay"
record "scope=run transport durability prerequisite; not full deployment-event AC-14"
record "source_commit=$(git -C "$repo_root" rev-parse HEAD)"
record "source_tree=$(git -C "$repo_root" status --porcelain=v1 | wc -l | tr -d ' ')_paths_changed"
record "project=$project"
record "started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
record "docker_version=$(docker version --format '{{.Server.Version}}')"
record "compose_version=$(docker compose version --short)"

docker build --pull --tag "$image" "$repo_root" >"$runtime_dir/docker-build.txt" 2>&1 || fail "fresh NeroCD image build failed"
record "image_id=$(docker image inspect --format '{{.Id}}' "$image")"
compose up --detach --wait postgres server proxy >"$runtime_dir/compose-up.txt" 2>&1 || fail "isolated PostgreSQL/server/proxy stack failed"
server_published=$(compose port server 8080 | tail -n1)
server_port=${server_published##*:}
proxy_published=$(compose port proxy 8081 | tail -n1)
proxy_port=${proxy_published##*:}
[[ "$server_port" =~ ^[0-9]+$ && "$proxy_port" =~ ^[0-9]+$ ]] || fail "dynamic loopback ports unavailable"
base="http://127.0.0.1:$server_port"
proxy_control="http://127.0.0.1:$proxy_port/__control"

http_json() {
  local method=$1 url=$2 token=$3 body=$4 output=$5
  local -a args=(--silent --show-error --max-time 8 --output "$output" --write-out '%{http_code}' --request "$method" --header 'Content-Type: application/json')
  [[ -n "$token" ]] && args+=(--header "Authorization: Bearer $token")
  args+=(--data "$body" "$url")
  curl "${args[@]}"
}
proxy_post() { curl --silent --show-error --max-time 5 --output /dev/null --write-out '%{http_code}' --request POST "$proxy_control/$1"; }
proxy_status() { curl --silent --show-error --max-time 5 "$proxy_control/status"; }
sql() {
  compose exec --no-TTY postgres psql --username nerocd --dbname nerocd --tuples-only --no-align --field-separator '|' --set ON_ERROR_STOP=1 --command "$1" | sed '/^[[:space:]]*$/d'
}

deadline=$((SECONDS + 60)); health=''
while (( SECONDS < deadline )); do
  health=$(curl --silent --max-time 2 --output /dev/null --write-out '%{http_code}' "$base/api/v1/health" || true)
  [[ "$health" == 200 ]] && break
  sleep 1
done
[[ "$health" == 200 ]] || fail "published server health unavailable"

status=$(http_json POST "$base/api/v1/sessions" '' '{"email":"admin@example.local","password":"admin"}' "$runtime_dir/session.json")
[[ "$status" == 201 ]] || fail "admin login returned HTTP $status"
admin_token=$(jq -er '.token | select(length>0)' "$runtime_dir/session.json") || fail "admin token missing"
status=$(http_json POST "$base/api/v1/runners/register" "$admin_token" '{"id":"runner_spool","name":"Runtime Spool","tags":["runtime-spool"],"capabilities":["shell"]}' "$runtime_dir/register.json")
[[ "$status" == 201 ]] || fail "runner registration returned HTTP $status"
runner_token=$(jq -er '.token | select(length>0)' "$runtime_dir/register.json") || fail "runner token missing"

main_body=$(jq -cn '{project_id:"proj_platform",run_spec:{type:"shell",inputs:{fixture:"runtime-spool"},process:{command:["/bin/sh","/fixtures/run-fixture.sh"],timeout_seconds:180}},runner_tags:["runtime-spool"]}')
status=$(http_json POST "$base/api/v1/runs" "$admin_token" "$main_body" "$runtime_dir/main-run.json")
[[ "$status" == 201 ]] || fail "main run creation returned HTTP $status"
run_id=$(jq -er '.id | select(test("^[A-Za-z0-9_]+$"))' "$runtime_dir/main-run.json") || fail "main run id missing"
record "run_id=$run_id"

printf '%s\n' "$runner_token" | compose run --rm --no-deps --no-TTY credential_init >"$runtime_dir/credential-init.txt" 2>&1 || fail "credential/journal initialization failed"
[[ "$(proxy_post drop-event)" == 204 ]] || fail "could not arm committed event response loss"
compose up --detach runner >"$runtime_dir/runner-up.txt" 2>&1 || fail "runner failed to start"
runner_cid=$(compose ps --quiet runner)
[[ -n "$runner_cid" ]] || fail "runner container id missing"
journal_volume=$(docker volume ls --quiet --filter "label=com.docker.compose.project=$project" --filter 'label=com.docker.compose.volume=runner-journal' | head -n1)
[[ -n "$journal_volume" ]] || fail "runner journal volume missing"
journal_json() {
  docker run --rm --network none --entrypoint /bin/sh -v "$journal_volume:/journal:ro" "$image" -ec 'if [ -f /journal/journal.json ]; then cat /journal/journal.json; else printf "{\"version\":1,\"events\":[],\"completions\":[]}"; fi'
}
journal_depth() { journal_json | jq -er '(.events|length)+(.completions|length)'; }

deadline=$((SECONDS + 45)); attempt_row=''
while (( SECONDS < deadline )); do
  attempt_row=$(sql "SELECT id,attempt,(length(fence)>0)::text,floor(extract(epoch FROM (expires_at-clock_timestamp())))::bigint FROM run_leases WHERE run_id='$run_id' AND status='active';" || true)
  [[ -n "$attempt_row" ]] && break
  sleep 0.2
done
[[ -n "$attempt_row" ]] || fail "runner did not claim main run"
IFS='|' read -r lease_id attempt fence_present headroom <<<"$attempt_row"
[[ "$attempt" == 1 && "$fence_present" == true ]] || fail "claim authority malformed"
(( headroom >= 120 )) || fail "lease headroom $headroom seconds is insufficient for outage"
record "claim=attempt1 fence_present=true initial_headroom_seconds=$headroom"

deadline=$((SECONDS + 15)); first_event_epoch=''; marker_epoch=''
while (( SECONDS < deadline )); do
  first_event_epoch=$(sql "SELECT floor(extract(epoch FROM created_at))::bigint FROM run_logs WHERE run_id='$run_id' AND message='spool-event-001' LIMIT 1;" || true)
  marker_epoch=$(docker exec "$runner_cid" sh -c 'test -s /state/first-event-epoch && cat /state/first-event-epoch' 2>/dev/null || true)
  [[ -n "$first_event_epoch" && -n "$marker_epoch" ]] && break
  sleep 0.1
done
[[ -n "$first_event_epoch" && "$marker_epoch" =~ ^[0-9]+$ ]] || fail "first fixture event was not observable"
visibility=$((first_event_epoch - marker_epoch)); (( visibility < 0 )) && visibility=$((-visibility))
(( visibility <= 2 )) || fail "healthy event visibility was ${visibility}s, exceeds 2s"
deadline=$((SECONDS + 10)); lost_event=0
while (( SECONDS < deadline )); do lost_event=$(proxy_status | jq -r '.lost_event_responses'); [[ "$lost_event" == 1 ]] && break; sleep 0.1; done
[[ "$lost_event" == 1 ]] || fail "proxy did not lose a committed event response"
event_one_count=$(sql "SELECT count(*) FROM run_logs WHERE run_id='$run_id' AND message='spool-event-001';")
[[ "$event_one_count" == 1 ]] || fail "lost-response event was persisted $event_one_count times"
record "healthy_visibility_seconds=$visibility committed_event_response_lost=true exact_event_count=1"

[[ "$(proxy_post outage/on)" == 204 ]] || fail "could not start runner API outage"
outage_set_epoch=$(sql 'SELECT floor(extract(epoch FROM clock_timestamp()))::bigint;')
# Drain any request admitted before the cut using more than the runner's 5s
# request bound, then measure a full continuous 60s no-delivery window.
sleep 6
outage_observe_start=$(sql 'SELECT floor(extract(epoch FROM clock_timestamp()))::bigint;')
baseline_events=$(sql "SELECT count(*) FROM run_logs WHERE run_id='$run_id' AND event_key IS NOT NULL;")
depth_start=$(journal_depth)
record "outage_set_db_epoch=$outage_set_epoch drain_seconds=6 outage_observation_start_db_epoch=$outage_observe_start baseline_db_events=$baseline_events journal_depth_start=$depth_start"

deadline=$((SECONDS + 40)); depth_before_restart=0; completion_pending=0
while (( SECONDS < deadline )); do
  contents=$(journal_json)
  depth_before_restart=$(jq -r '(.events|length)+(.completions|length)' <<<"$contents")
  completion_pending=$(jq -r '.completions|length' <<<"$contents")
  progress=$(docker exec "$runner_cid" sh -c 'test -s /state/fixture-progress && cat /state/fixture-progress' 2>/dev/null || true)
  [[ "$progress" == 18 && "$completion_pending" == 1 ]] && break
  sleep 0.25
done
[[ "$progress" == 18 && "$completion_pending" == 1 ]] || fail "fixture did not durably journal completion during outage"
(( depth_before_restart > depth_start )) || fail "journal did not grow during outage"
events_during_growth=$(sql "SELECT count(*) FROM run_logs WHERE run_id='$run_id' AND event_key IS NOT NULL;")
[[ "$events_during_growth" == "$baseline_events" ]] || fail "database received events during isolated runner outage"
record "spool_growth=depth_${depth_start}_to_${depth_before_restart} fixture_events=18 database_events_unchanged=$baseline_events completion_durable=true"

compose stop --timeout 5 runner >"$runtime_dir/runner-stop.txt" 2>&1 || fail "runner stop during outage failed"
# Drain requests admitted before shutdown, then take the baseline which the
# newly started process is forbidden to advance until journal reconciliation.
sleep 6
proxy_before_restart=$(proxy_status)
claims_before_restart=$(jq -r '.requests["/api/v1/runners/claim"] // 0' <<<"$proxy_before_restart")
heartbeats_before_restart=$(jq -r '.requests["/api/v1/runners/heartbeat"] // 0' <<<"$proxy_before_restart")
[[ "$(journal_depth)" == "$depth_before_restart" ]] || fail "stopping runner changed durable journal depth"
compose start runner >"$runtime_dir/runner-restart.txt" 2>&1 || fail "runner restart during outage failed"
runner_cid=$(compose ps --quiet runner)
[[ -n "$runner_cid" ]] || fail "restarted runner container id missing"
sleep 7
proxy_after_restart=$(proxy_status)
claims_after_restart=$(jq -r '.requests["/api/v1/runners/claim"] // 0' <<<"$proxy_after_restart")
heartbeats_after_restart=$(jq -r '.requests["/api/v1/runners/heartbeat"] // 0' <<<"$proxy_after_restart")
[[ "$claims_after_restart" == "$claims_before_restart" && "$heartbeats_after_restart" == "$heartbeats_before_restart" ]] || fail "restart issued heartbeat/claim before journal reconciliation"
[[ "$(journal_depth)" == "$depth_before_restart" ]] || fail "outage restart lost or acknowledged journal entries"
docker exec "$runner_cid" test -e /state/runner.exit || fail "outage startup did not fail closed after preserving journal"
record "restart_during_outage=true startup_failed_closed=true journal_preserved_depth=$depth_before_restart pre_reconcile_claims=0 pre_reconcile_heartbeats=0"

sentinel_body=$(jq -cn '{project_id:"proj_platform",run_spec:{type:"shell",inputs:{fixture:"post-reconcile-sentinel"},process:{command:["/bin/true"],timeout_seconds:30}},runner_tags:["runtime-spool"]}')
status=$(http_json POST "$base/api/v1/runs" "$admin_token" "$sentinel_body" "$runtime_dir/sentinel-run.json")
[[ "$status" == 201 ]] || fail "sentinel run creation returned HTTP $status"
sentinel_id=$(jq -er '.id | select(test("^[A-Za-z0-9_]+$"))' "$runtime_dir/sentinel-run.json") || fail "sentinel run id missing"

while :; do
  now_db=$(sql 'SELECT floor(extract(epoch FROM clock_timestamp()))::bigint;')
  elapsed=$((now_db - outage_observe_start))
  current_events=$(sql "SELECT count(*) FROM run_logs WHERE run_id='$run_id' AND event_key IS NOT NULL;")
  [[ "$current_events" == "$baseline_events" ]] || fail "database received runner events during ${elapsed}s outage window"
  [[ "$(curl --silent --max-time 2 --output /dev/null --write-out '%{http_code}' "$base/api/v1/health")" == 200 ]] || fail "server health failed during runner-only outage"
  record "outage_sample_db_epoch=$now_db elapsed_seconds=$elapsed db_events=$current_events journal_depth=$(journal_depth) server_healthy=true"
  (( elapsed >= 60 )) && break
  sleep 5
done
record "outage_assertion=runner_api_path_cut_continuously_at_least_60s server_and_postgresql_available database_received_zero_new_events"

[[ "$(proxy_post drop-completion)" == 204 ]] || fail "could not arm committed completion response loss"
[[ "$(proxy_post outage/off)" == 204 ]] || fail "could not restore runner API path"
restored_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
# The process started during the outage failed closed after preserving the
# journal. Start it again only after transport is restored; reconciliation must
# still finish before heartbeat or claim traffic.
compose restart --timeout 5 runner >"$runtime_dir/runner-restored-restart.txt" 2>&1 || fail "runner restart after API restoration failed"
runner_cid=$(compose ps --quiet runner)
[[ -n "$runner_cid" ]] || fail "restored runner container id missing"
deadline=$((SECONDS + 60)); final=''
while (( SECONDS < deadline )); do
  final=$(sql "SELECT (SELECT status FROM task_runs WHERE id='$run_id'),(SELECT status FROM task_runs WHERE id='$sentinel_id'),(SELECT count(*) FROM run_leases WHERE run_id='$run_id'),(SELECT count(*) FROM run_leases WHERE run_id='$run_id' AND status='succeeded');" || true)
  [[ "$final" == 'succeeded|succeeded|1|1' ]] && break
  sleep 0.5
done
[[ "$final" == 'succeeded|succeeded|1|1' ]] || fail "replayed main completion/sentinel result incoherent: $final"
deadline=$((SECONDS + 20)); final_depth=-1; lost_completion=0
while (( SECONDS < deadline )); do
  final_depth=$(journal_depth)
  lost_completion=$(proxy_status | jq -r '.lost_completion_responses')
  [[ "$final_depth" == 0 && "$lost_completion" == 1 ]] && break
  sleep 0.2
done
[[ "$final_depth" == 0 && "$lost_completion" == 1 ]] || fail "journal did not drain or completion response was not lost"

proxy_final=$(proxy_status)
trace_order=$(jq -r '
  .trace as $trace |
  ([range(0; $trace|length) | select($trace[.]|contains("control:outage-off"))] | last) as $off |
  ([range($off+1; $trace|length) | select($trace[.]|contains("response:/api/v1/runners/complete:200"))] | first) as $complete |
  ([range($off+1; $trace|length) | select($trace[.]|contains("response:/api/v1/runners/claim:200"))] | first) as $claim |
  "\($off)|\($complete)|\($claim)"' <<<"$proxy_final")
IFS='|' read -r off_index complete_index claim_index <<<"$trace_order"
[[ "$off_index" != null && "$complete_index" != null && "$claim_index" != null ]] || fail "proxy trace lacked restore/completion/sentinel claim ordering"
(( complete_index < claim_index )) || fail "sentinel claim preceded journal completion reconciliation"

event_summary=$(sql "SELECT count(*),count(DISTINCT event_key),count(DISTINCT sequence),min(message),max(message) FROM run_logs WHERE run_id='$run_id' AND message LIKE 'spool-event-%';")
IFS='|' read -r event_count event_keys event_sequences min_message max_message <<<"$event_summary"
[[ "$event_count" == 18 && "$event_keys" == 18 && "$event_sequences" == 18 && "$min_message" == spool-event-001 && "$max_message" == spool-event-018 ]] || fail "ordered fixture event set invalid: $event_summary"
ordered_messages=$(sql "SELECT string_agg(message,',' ORDER BY sequence) FROM run_logs WHERE run_id='$run_id' AND message LIKE 'spool-event-%';")
expected_messages=$(printf 'spool-event-%03d,' {1..18}); expected_messages=${expected_messages%,}
[[ "$ordered_messages" == "$expected_messages" ]] || fail "fixture event order changed during replay"
terminal_counts=$(sql "SELECT (SELECT count(*) FROM run_logs WHERE run_id='$run_id' AND message LIKE 'Runner completed lease%'),(SELECT count(*) FROM audit_events WHERE target_id='$run_id' AND action='runner.complete'),(SELECT count(*) FROM run_leases WHERE run_id='$run_id' AND completion_key IS NOT NULL); ")
[[ "$terminal_counts" == '1|1|1' ]] || fail "completion replay duplicated terminal mutation: $terminal_counts"
[[ "$(docker exec "$runner_cid" cat /state/revision)" == revision-spooled ]] || fail "fixture final revision missing"
record "restored_at=$restored_at fail_closed_outage_restart_preserved=true restored_runner_restart=true committed_completion_response_lost=true completion_retry_exact=true"
record "reconciliation_order=events_then_completion_then_sentinel_claim trace_indexes=$complete_index<$claim_index"
record "final_assertion=18_unique_ordered_events one_terminal_log one_terminal_audit one_authoritative_attempt journal_depth_zero revision=revision-spooled"

compose logs --no-color server proxy runner >"$runtime_dir/runtime-logs.txt" 2>&1 || true
for secret in "$admin_token" "$runner_token" runtime_spool_only admin; do
  [[ -n "$secret" ]] || continue
  grep -F -- "$secret" "$evidence" "$runtime_dir/runtime-logs.txt" >/dev/null 2>&1 && fail "credential or password leaked into retained evidence/logs"
done
known_fence=$(sql "SELECT fence FROM run_leases WHERE run_id='$run_id' LIMIT 1;")
[[ -n "$known_fence" ]] && ! grep -F -- "$known_fence" "$evidence" "$runtime_dir/runtime-logs.txt" >/dev/null 2>&1 || fail "fence leaked into retained evidence/logs"
record "secret_scan=known session/runner credentials, fence, password, fixture secrets absent"
record "finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
passed=true
