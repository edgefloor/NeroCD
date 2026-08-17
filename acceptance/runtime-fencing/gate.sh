#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
compose_file="$repo_root/acceptance/runtime-fencing/compose.yaml"
evidence=/tmp/nerocd-ac07-ac08-runtime.txt
runtime_dir=$(mktemp -d /tmp/nerocd-runtime-fencing.XXXXXXXX)
suffix=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')
project="nerocd-fence-$suffix"
image="nerocd-runtime-fencing:$suffix"
started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
passed=false

: >"$evidence"

record() { printf '%s\n' "$*" >>"$evidence"; }
fail() { record "FAIL: $*"; printf 'runtime-fencing-gate: %s\n' "$*" >&2; exit 1; }
compose() { NEROCD_RUNTIME_IMAGE="$image" docker compose --project-name "$project" --file "$compose_file" "$@"; }

cleanup() {
  status=$?
  set +e
  cleanup_failed=false
  if [[ "$project" =~ ^nerocd-fence-[0-9a-f]{12}$ ]]; then
    compose down --volumes --remove-orphans --timeout 5 >/dev/null 2>&1
    while IFS= read -r resource; do [[ -n "$resource" ]] && docker container rm --force "$resource" >/dev/null 2>&1; done < <(docker container ls --all --quiet --filter "label=com.docker.compose.project=$project")
    while IFS= read -r resource; do [[ -n "$resource" ]] && docker volume rm --force "$resource" >/dev/null 2>&1; done < <(docker volume ls --quiet --filter "label=com.docker.compose.project=$project")
    while IFS= read -r resource; do [[ -n "$resource" ]] && docker network rm "$resource" >/dev/null 2>&1; done < <(docker network ls --quiet --filter "label=com.docker.compose.project=$project")
    remaining=$(docker container ls --all --quiet --filter "label=com.docker.compose.project=$project"; docker volume ls --quiet --filter "label=com.docker.compose.project=$project"; docker network ls --quiet --filter "label=com.docker.compose.project=$project")
    [[ -z "$remaining" ]] || cleanup_failed=true
  else
    cleanup_failed=true
  fi
  [[ "$image" =~ ^nerocd-runtime-fencing:[0-9a-f]{12}$ ]] && docker image rm --force "$image" >/dev/null 2>&1
  case "$runtime_dir" in /tmp/nerocd-runtime-fencing.*) rm -rf -- "$runtime_dir" ;; esac
  if [[ "$cleanup_failed" == true ]]; then
    status=1
    record "cleanup_complete=false"
    record "FAIL: isolated project resources remain after cleanup"
  else
    record "cleanup_complete=true"
  fi
  if [[ "$passed" == true && "$cleanup_failed" == false ]]; then
    record "PASS: real fenced-run partition gate"
  elif [[ $status -eq 0 ]]; then
    status=1
    record "FAIL: gate exited without completing every assertion"
  fi
  printf 'runtime fencing evidence: %s\n' "$evidence"
  exit "$status"
}
trap cleanup EXIT
unexpected_error() {
  local status=$? line=$1
  trap - ERR
  fail "unexpected command failure at line $line (exit $status)"
}
trap 'unexpected_error "$LINENO"' ERR

for tool in docker curl jq od; do command -v "$tool" >/dev/null 2>&1 || fail "required tool missing: $tool"; done
docker info >/dev/null 2>&1 || fail "Docker daemon is unavailable"
docker compose version >/dev/null 2>&1 || fail "Docker Compose is unavailable"

record "gate=real fenced-run partition"
record "scope=run-level fencing/reaper/process containment only; not full deployment/environment AC-07/AC-08"
record "source_commit=$(git -C "$repo_root" rev-parse HEAD)"
record "source_tree=$(git -C "$repo_root" status --porcelain=v1 | wc -l | tr -d ' ')_paths_changed"
record "project=$project"
record "started_at=$started_at"
record "docker_version=$(docker version --format '{{.Server.Version}}')"
record "compose_version=$(docker compose version --short)"
record "curl_version=$(curl --version | head -n1)"
record "jq_version=$(jq --version)"

docker build --pull --tag "$image" "$repo_root" >"$runtime_dir/docker-build.txt" 2>&1 || fail "fresh NeroCD image build failed"
record "image_id=$(docker image inspect --format '{{.Id}}' "$image")"
compose up --detach --wait postgres server >"$runtime_dir/compose-up.txt" 2>&1 || fail "isolated PostgreSQL/server stack did not become healthy"
published=$(compose port server 8080 | tail -n1)
port=${published##*:}
[[ "$port" =~ ^[0-9]+$ ]] || fail "could not resolve dynamic loopback server port"
base="http://127.0.0.1:$port"

http_json() {
  local method=$1 url=$2 token=$3 body=$4 output=$5
  local -a args=(--silent --show-error --max-time 8 --output "$output" --write-out '%{http_code}' --request "$method" --header 'Content-Type: application/json')
  [[ -n "$token" ]] && args+=(--header "Authorization: Bearer $token")
  args+=(--data "$body" "$url")
  curl "${args[@]}"
}

deadline=$((SECONDS + 60))
while (( SECONDS < deadline )); do
  status=$(curl --silent --max-time 2 --output /dev/null --write-out '%{http_code}' "$base/api/v1/health" || true)
  [[ "$status" == 200 ]] && break
  sleep 1
done
[[ "$status" == 200 ]] || fail "published server health endpoint unavailable"

status=$(http_json POST "$base/api/v1/sessions" '' '{"email":"admin@example.local","password":"admin"}' "$runtime_dir/session.json")
[[ "$status" == 201 ]] || fail "admin login returned HTTP $status"
admin_token=$(jq -er '.token | select(length > 0)' "$runtime_dir/session.json") || fail "admin login omitted token"

register_runner() {
  local runner_id=$1 output=$2 status
  status=$(http_json POST "$base/api/v1/runners/register" "$admin_token" "{\"id\":\"$runner_id\",\"name\":\"Runtime Fencing $runner_id\",\"tags\":[\"runtime-fencing\"],\"capabilities\":[\"shell\"]}" "$output")
  [[ "$status" == 201 ]] || fail "register $runner_id returned HTTP $status"
  jq -er '.token | select(length > 0)' "$output"
}
token_a=$(register_runner runner_runtime_a "$runtime_dir/register-a.json")
token_b=$(register_runner runner_runtime_b "$runtime_dir/register-b.json")

run_body=$(jq -cn '{project_id:"proj_platform",run_spec:{type:"shell",inputs:{fixture:"real-fenced-run-partition"},process:{command:["/bin/sh","/fixtures/run-fixture.sh"],timeout_seconds:900}},runner_tags:["runtime-fencing"]}')
status=$(http_json POST "$base/api/v1/runs" "$admin_token" "$run_body" "$runtime_dir/run.json")
[[ "$status" == 201 ]] || fail "create run returned HTTP $status"
run_id=$(jq -er '.id | select(test("^[A-Za-z0-9_]+$"))' "$runtime_dir/run.json") || fail "create run omitted safe run id"
record "run_id=$run_id"

printf '%s\n%s\n' "$token_a" "$token_b" | compose run --rm --no-deps --no-TTY credential_init >"$runtime_dir/credential-init.txt" 2>&1 || fail "credential volume initialization failed"
compose up --detach runner_a runner_b >"$runtime_dir/runners-up.txt" 2>&1 || fail "runner containers failed to start"
runner_a_cid=$(compose ps --quiet runner_a)
runner_b_cid=$(compose ps --quiet runner_b)
[[ -n "$runner_a_cid" && -n "$runner_b_cid" ]] || fail "runner container IDs unavailable"
docker exec "$runner_a_cid" sh -c ': > /state/start-runners' || fail "could not release runner start barrier"
record "runners_released_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"

sql() {
  compose exec --no-TTY postgres psql --username nerocd --dbname nerocd --tuples-only --no-align --field-separator '|' --set ON_ERROR_STOP=1 --command "$1" | sed '/^[[:space:]]*$/d'
}

deadline=$((SECONDS + 90)); attempt1_row=''
while (( SECONDS < deadline )); do
  attempt1_row=$(sql "SELECT id,runner_id,attempt,fence,extract(epoch FROM expires_at)::bigint FROM run_leases WHERE run_id='$run_id' AND attempt=1 AND status='active';" || true)
  [[ -n "$attempt1_row" ]] && break
  sleep 1
done
[[ -n "$attempt1_row" ]] || fail "two real runners did not produce active attempt 1"
IFS='|' read -r lease1 runner1 attempt1 fence1 expiry1 <<<"$attempt1_row"
[[ "$attempt1" == 1 && -n "$fence1" ]] || fail "attempt 1 capability incomplete"
case "$runner1" in
  runner_runtime_a) winner_cid=$runner_a_cid; loser_cid=$runner_b_cid; old_token=$token_a ;;
  runner_runtime_b) winner_cid=$runner_b_cid; loser_cid=$runner_a_cid; old_token=$token_b ;;
  *) fail "unexpected attempt 1 runner identity" ;;
esac
winner_name=${runner1#runner_runtime_}
loser_name=$([[ "$winner_name" == a ]] && printf b || printf a)
loser_runner="runner_runtime_$loser_name"
record "attempt1_runner=$runner1"
record "race_assertion=one active attempt1; loser did not claim"

deadline=$((SECONDS + 30))
while (( SECONDS < deadline )); do docker exec "$winner_cid" test -e /state/attempt1.ready && break; sleep 1; done
docker exec "$winner_cid" test -e /state/attempt1.ready || fail "attempt 1 fixture did not become ready"
docker exec "$winner_cid" test ! -e /state/revision || fail "attempt 1 unexpectedly published a revision"
leader_pid=$(docker exec "$winner_cid" cat /state/attempt1.leader.pid)
descendant_pid=$(docker exec "$winner_cid" cat /state/attempt1.descendant.pid)
[[ "$leader_pid" =~ ^[0-9]+$ && "$descendant_pid" =~ ^[0-9]+$ ]] || fail "fixture process IDs are invalid"
descendant_before=$(docker exec "$winner_cid" sh -c 'pid=$1; line=$(cat "/proc/$pid/stat"); rest=${line##*) }; set -- $rest; printf "%s|%s" "$1" "$3"' sh "$descendant_pid")
IFS='|' read -r descendant_before_state descendant_before_group <<<"$descendant_before"
[[ "$descendant_before_state" != Z && "$descendant_before_group" == "$leader_pid" ]] || fail "TERM-ignoring descendant is not live in the attempt process group"
record "fixture_process_assertion=leader_and_TERM_ignoring_descendant_share_process_group"

healthy_db_start=$(sql 'SELECT extract(epoch FROM clock_timestamp())::bigint;')
healthy_wall_start=$(date +%s)
previous_liveness=''; samples=0
record "healthy_phase_db_start=$healthy_db_start"
while :; do
  now_db=$(sql 'SELECT extract(epoch FROM clock_timestamp())::bigint;')
  elapsed=$((now_db - healthy_db_start))
  lease_sample=$(sql "SELECT count(*) FILTER (WHERE status='active'),count(*),max(extract(epoch FROM expires_at)::bigint),max(attempt) FROM run_leases WHERE run_id='$run_id';")
  IFS='|' read -r active_count lease_count expiry_sample max_attempt <<<"$lease_sample"
  [[ "$active_count" == 1 && "$lease_count" == 1 && "$max_attempt" == 1 ]] || fail "healthy phase reassigned or lost its single active attempt"
  (( expiry_sample > now_db + 3 )) || fail "lease renewal stopped during healthy phase"
  [[ "$(sql "SELECT status FROM task_runs WHERE id='$run_id';")" == running ]] || fail "healthy phase run became terminal"
  heartbeat_sample=$(sql "SELECT count(*) FROM runners WHERE id IN ('runner_runtime_a','runner_runtime_b') AND status='active' AND last_heartbeat_at > clock_timestamp()-interval '5 seconds';")
  [[ "$heartbeat_sample" == 2 ]] || fail "runner heartbeat freshness failed during healthy phase"
  liveness=$(docker exec "$winner_cid" sh -c 'test -s /state/liveness && cat /state/liveness' 2>/dev/null || true)
  [[ -n "$liveness" ]] || fail "attempt 1 liveness counter missing"
  [[ -z "$previous_liveness" || "$liveness" != "$previous_liveness" ]] || fail "attempt 1 liveness counter did not advance"
  docker exec "$winner_cid" test ! -e /state/revision || fail "attempt 1 published a revision during healthy phase"
  samples=$((samples + 1))
  record "healthy_sample=$samples db_epoch=$now_db elapsed_seconds=$elapsed lease_expiry_epoch=$expiry_sample heartbeats_fresh=2 active_leases=1 attempt=1 fence_present=true"
  previous_liveness=$liveness
  (( elapsed >= 600 )) && break
  sleep 10
done
healthy_wall_elapsed=$(($(date +%s) - healthy_wall_start))
(( healthy_wall_elapsed >= 600 )) || fail "healthy wall-clock phase was shorter than 10 minutes"
(( samples >= 50 )) || fail "insufficient healthy-phase evidence samples"
record "healthy_phase_elapsed_seconds=$healthy_wall_elapsed"
record "healthy_phase_assertion=continuous >=10m; renewals, heartbeats, one attempt, fixture nonterminal"

docker pause "$loser_cid" >/dev/null || fail "could not pause loser before partition"
[[ "$(docker inspect --format '{{.State.Paused}}' "$loser_cid")" == true ]] || fail "loser is not paused"
docker inspect --format '{{range $name, $network := .NetworkSettings.Networks}}{{println $name}}{{end}}' "$loser_cid" | grep -Fx -- "${project}_runtime" >/dev/null || fail "paused loser lost its runtime network attachment"

claim_log_count() {
  compose logs --no-color server 2>/dev/null | awk 'index($0,"/api/v1/runners/claim") { count++ } END { print count+0 }'
}

# A paused process cannot originate another request, but one already admitted by
# the server must also be proven drained. Wait beyond runnerHTTPClient's 5s
# request bound, then require both a stable completed-request count and repeated
# PostgreSQL snapshots with no non-idle server transaction.
drain_started=$(date +%s)
claims_at_pause=$(claim_log_count)
sleep 7
consecutive_idle=0
deadline=$((SECONDS + 20))
while (( SECONDS < deadline )); do
  pending_db_sessions=$(sql "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND backend_type='client backend' AND state<>'idle';")
  if [[ "$pending_db_sessions" == 0 ]]; then
    consecutive_idle=$((consecutive_idle + 1))
    (( consecutive_idle >= 5 )) && break
  else
    consecutive_idle=0
  fi
  sleep 0.5
done
(( consecutive_idle >= 5 )) || fail "server database activity did not quiesce after pausing loser"
claims_at_quiescence=$(claim_log_count)
sleep 2
claims_after_stability=$(claim_log_count)
[[ "$claims_after_stability" == "$claims_at_quiescence" ]] || fail "a claim request completed after the quiescence point"
pending_db_sessions=$(sql "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND backend_type='client backend' AND state<>'idle';")
[[ "$pending_db_sessions" == 0 ]] || fail "a server database transaction remained active after claim drain"
[[ "$(docker inspect --format '{{.State.Paused}}' "$loser_cid")" == true ]] || fail "loser resumed during claim drain"
docker inspect --format '{{range $name, $network := .NetworkSettings.Networks}}{{println $name}}{{end}}' "$loser_cid" | grep -Fx -- "${project}_runtime" >/dev/null || fail "paused loser lost its runtime network attachment during claim drain"
pre_partition_state=$(sql "SELECT (SELECT status FROM run_leases WHERE id='$lease1'),(SELECT status FROM task_runs WHERE id='$run_id'),(SELECT count(*) FROM run_leases WHERE run_id='$run_id' AND attempt=2),floor(extract(epoch FROM ((SELECT expires_at FROM run_leases WHERE id='$lease1')-clock_timestamp())))::bigint;")
IFS='|' read -r pre_lease_status pre_run_status pre_attempt2_count expiry_seconds_ahead <<<"$pre_partition_state"
[[ "$pre_lease_status" == active && "$pre_run_status" == running && "$pre_attempt2_count" == 0 ]] || fail "authority changed while quiescing loser: $pre_partition_state"
(( expiry_seconds_ahead >= 3 )) || fail "winner lease was not safely unexpired at partition boundary"
drain_seconds=$(($(date +%s) - drain_started))
(( drain_seconds >= 9 )) || fail "claim drain was shorter than the observable bounded interval"
claims_drained=$((claims_at_quiescence - claims_at_pause))
record "loser_claims_paused_at=$(date -u +%Y-%m-%dT%H:%M:%SZ) loser_network_connected=true drain_seconds=$drain_seconds completed_claims_during_drain=$claims_drained stable_claim_count=$claims_after_stability pending_db_sessions=0 consecutive_idle_checks=$consecutive_idle pre_partition_state=active|running|0 expiry_seconds_ahead=$expiry_seconds_ahead"

expiry_at_partition=$(sql "SELECT extract(epoch FROM expires_at)::bigint FROM run_leases WHERE id='$lease1';")
network_id=$(docker network ls --quiet --filter "label=com.docker.compose.project=$project" --filter 'label=com.docker.compose.network=runtime' | head -n1)
[[ -n "$network_id" ]] || fail "isolated runtime network not found"
partition_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
docker network disconnect --force "$network_id" "$winner_cid" || fail "winner network partition failed"
record "partition_at=$partition_at expiry_at_partition_epoch=$expiry_at_partition disconnected_runner=$runner1 loser_connected=$loser_runner server_connected=true"

deadline=$((SECONDS + 30))
while (( SECONDS < deadline )); do docker exec "$winner_cid" test -e "/state/runner-$winner_name.exit" && break; sleep 1; done
docker exec "$winner_cid" test -e "/state/runner-$winner_name.exit" || fail "winner runner process did not exit after authority loss"
[[ "$(docker inspect --format '{{.State.Running}}' "$winner_cid")" == true ]] || fail "winner wrapper PID1 did not remain alive"
last_expiry=$(sql "SELECT extract(epoch FROM expires_at)::bigint FROM run_leases WHERE id='$lease1';")
record "runner_exit_observed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ) final_attempt1_expiry_epoch=$last_expiry"
descendant_state=$(docker exec "$winner_cid" sh -c "if [ -r /proc/$descendant_pid/stat ]; then line=\$(cat /proc/$descendant_pid/stat); rest=\${line##*) }; printf '%s' \"\${rest%% *}\"; else printf gone; fi")
case "$descendant_state" in gone|Z) ;; *) fail "attempt 1 descendant remains runnable with state $descendant_state" ;; esac
group_states=$(docker exec "$winner_cid" sh -c '
  pgid=$1
  for stat in /proc/[0-9]*/stat; do
    line=$(cat "$stat" 2>/dev/null) || continue
    rest=${line##*) }
    set -- $rest
    [ "$3" = "$pgid" ] && printf "%s\n" "$1"
  done
  exit 0
' sh "$leader_pid")
runnable_group_states=$(printf '%s\n' "$group_states" | sed '/^$/d;/^Z$/d')
[[ -z "$runnable_group_states" ]] || fail "attempt 1 process group retains runnable members: $runnable_group_states"
liveness_stopped=$(docker exec "$winner_cid" cat /state/liveness); sleep 3
[[ "$(docker exec "$winner_cid" cat /state/liveness)" == "$liveness_stopped" ]] || fail "attempt 1 liveness continued after runner exit"
group_state_evidence=$(printf '%s' "$group_states" | tr '\n' ',' | sed 's/,$//')
[[ -n "$group_state_evidence" ]] || group_state_evidence=gone
record "process_evidence=runner_exited wrapper_alive descendant_state=$descendant_state process_group_states=$group_state_evidence runnable_group_members=0 liveness_stopped=true"

deadline=$((SECONDS + 30))
while (( SECONDS < deadline )); do now_db=$(sql 'SELECT extract(epoch FROM clock_timestamp())::bigint;'); (( now_db > last_expiry )) && break; sleep 1; done
(( now_db > last_expiry )) || fail "DB clock did not pass attempt 1 last expiry"

deadline=$((SECONDS + 60)); reaper_only_state=''
while (( SECONDS < deadline )); do
  [[ "$(docker inspect --format '{{.State.Paused}}' "$loser_cid")" == true ]] || fail "loser resumed before reaper-only observation"
  reaper_only_state=$(sql "SELECT (SELECT status FROM run_leases WHERE id='$lease1'),(SELECT status FROM task_runs WHERE id='$run_id'),(SELECT count(*) FROM run_leases WHERE run_id='$run_id' AND attempt=2);" || true)
  [[ "$reaper_only_state" == 'expired|queued|0' ]] && break
  sleep 1
done
[[ "$reaper_only_state" == 'expired|queued|0' ]] || fail "periodic reaper did not independently produce expired|queued|0: $reaper_only_state"
record "periodic_reaper_observed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ) loser_claims_paused=true attempt1_status=expired run_status=queued attempt2_count=0"

docker unpause "$loser_cid" >/dev/null || fail "could not resume loser after reaper-only observation"
[[ "$(docker inspect --format '{{.State.Paused}}' "$loser_cid")" == false ]] || fail "loser remained paused"
record "loser_claims_resumed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
deadline=$((SECONDS + 60)); attempt2_row=''
while (( SECONDS < deadline )); do
  attempt2_row=$(sql "SELECT id,runner_id,attempt,fence FROM run_leases WHERE run_id='$run_id' AND attempt=2 AND status='active';" || true)
  [[ -n "$attempt2_row" ]] && break
  sleep 1
done
[[ -n "$attempt2_row" ]] || fail "resumed loser did not claim attempt 2"
IFS='|' read -r lease2 runner2 attempt2 fence2 <<<"$attempt2_row"
[[ "$runner2" == "$loser_runner" && "$attempt2" == 2 && -n "$fence2" && "$fence2" != "$fence1" ]] || fail "attempt 2 authority did not advance correctly"
record "post_reaper_reassignment_at=$(date -u +%Y-%m-%dT%H:%M:%SZ) attempt2_runner=$runner2 attempt2=2 fence_changed=true"

deadline=$((SECONDS + 30))
while (( SECONDS < deadline )); do docker exec "$loser_cid" test -e /state/attempt2.reconciled && break; sleep 1; done
docker exec "$loser_cid" test -e /state/attempt2.reconciled || fail "attempt 2 did not reconcile revision-b"
[[ "$(docker exec "$loser_cid" cat /state/revision)" == revision-b ]] || fail "attempt 2 revision is not revision-b"

stale_statuses=()
stale_call() {
  local label=$1 method=$2 path=$3 body=$4 status
  status=$(http_json "$method" "$base$path" "$old_token" "$body" "$runtime_dir/stale-$label.json")
  [[ "$status" == 404 ]] || fail "stale $label returned HTTP $status, expected stable 404"
  stale_statuses+=("$label=$status")
}
stale_call renew POST /api/v1/runners/renew "$(jq -cn --arg lease "$lease1" --arg fence "$fence1" '{lease_id:$lease,attempt:1,fence:$fence}')"
stale_call log POST /api/v1/runners/logs "$(jq -cn --arg run "$run_id" --arg lease "$lease1" --arg fence "$fence1" '{run_id:$run,lease_id:$lease,attempt:1,fence:$fence,event_key:"stale-partition-event",sequence:900001,stream:"stderr",message:"stale-partition-write"}')"
stale_call artifact POST /api/v1/runners/artifacts "$(jq -cn --arg run "$run_id" --arg lease "$lease1" --arg fence "$fence1" '{run_id:$run,lease_id:$lease,attempt:1,fence:$fence,name:"stale-partition-artifact",path:"stale-partition-artifact",found:true,required:false,size:1,kind:"file"}')"
stale_call complete POST /api/v1/runners/complete "$(jq -cn --arg lease "$lease1" --arg fence "$fence1" '{lease_id:$lease,attempt:1,fence:$fence,completion_key:"stale-partition-completion",status:"succeeded"}')"
encoded_fence=$(jq -rn --arg value "$fence1" '$value|@uri')
lease_lookup_status=$(curl --silent --show-error --max-time 8 --output "$runtime_dir/stale-lease.json" --write-out '%{http_code}' --header "Authorization: Bearer $old_token" "$base/api/v1/runners/lease?lease_id=$lease1&attempt=1&fence=$encoded_fence")
[[ "$lease_lookup_status" == 404 ]] || fail "stale lease lookup returned HTTP $lease_lookup_status, expected stable 404"
stale_statuses+=("lease_lookup=$lease_lookup_status")
record "stale_http_statuses=${stale_statuses[*]}"

stale_logs=$(sql "SELECT count(*) FROM run_logs WHERE run_id='$run_id' AND message='stale-partition-write';")
stale_artifacts=$(sql "SELECT count(*) FROM run_artifacts WHERE run_id='$run_id' AND name='stale-partition-artifact';")
authority_state=$(sql "SELECT (SELECT status FROM task_runs WHERE id='$run_id'),(SELECT status FROM run_leases WHERE id='$lease1'),(SELECT status FROM run_leases WHERE id='$lease2');")
[[ "$stale_logs" == 0 && "$stale_artifacts" == 0 && "$authority_state" == 'running|expired|active' ]] || fail "stale attempt mutated persistent state"
record "stale_db_assertion=logs_absent artifacts_absent run_running attempt1_expired attempt2_active"

docker exec "$loser_cid" sh -c ': > /state/allow-attempt2-complete'
deadline=$((SECONDS + 60)); final_state=''
while (( SECONDS < deadline )); do
  final_state=$(sql "SELECT (SELECT status FROM task_runs WHERE id='$run_id'),(SELECT count(*) FROM run_leases WHERE run_id='$run_id'),(SELECT count(*) FROM run_leases WHERE run_id='$run_id' AND status='active'),(SELECT string_agg(attempt::text||':'||status,',' ORDER BY attempt) FROM run_leases WHERE run_id='$run_id');" || true)
  [[ "$final_state" == 'succeeded|2|0|1:expired,2:succeeded' ]] && break
  sleep 1
done
[[ "$final_state" == 'succeeded|2|0|1:expired,2:succeeded' ]] || fail "final run/lease state is incoherent: $final_state"
[[ "$(docker exec "$loser_cid" cat /state/revision)" == revision-b ]] || fail "final revision changed from revision-b"
revision_files=$(docker exec "$loser_cid" sh -c "find /state -maxdepth 1 -type f -name 'revision*' | wc -l | tr -d ' '")
[[ "$revision_files" == 1 ]] || fail "fixture retained more than one final revision file"
[[ "$(docker exec "$winner_cid" cat /state/liveness)" == "$liveness_stopped" ]] || fail "old attempt liveness resumed"

# The partitioned attempt emitted stdout after its API path disappeared, so its
# durable journal must contain at least one never-committed stale event. Restore
# connectivity and restart that runner only after reassignment/completion; its
# startup reconciliation must receive an explicit 404 from its bounded read-only
# lease probe, durably discard the stale attempt, and never persist the pending
# key. Authentication, conflict, transport, and local-deadline failures are not
# valid discard signals.
pending_journal=$(docker exec "$winner_cid" sh -c 'test -f /journal/journal.json && cat /journal/journal.json') || fail "partitioned winner journal is unavailable"
pending_events=$(jq -er '.events|length' <<<"$pending_journal") || fail "partitioned winner journal is invalid"
(( pending_events > 0 )) || fail "partitioned winner did not retain stale journal events"
pending_event_key=$(jq -er '.events[0].id | select(test("^event_[0-9a-f]{32}$"))' <<<"$pending_journal") || fail "pending stale event key is malformed"
[[ "$(sql "SELECT count(*) FROM run_logs WHERE run_id='$run_id' AND event_key='$pending_event_key';")" == 0 ]] || fail "pending stale journal event was already persisted"
docker network connect "$network_id" "$winner_cid" >/dev/null || fail "could not restore winner API network for stale replay"
docker restart --time 5 "$winner_cid" >/dev/null || fail "could not restart winner for stale journal reconciliation"
deadline=$((SECONDS + 30)); stale_depth=-1
while (( SECONDS < deadline )); do
  stale_journal_now=$(docker exec "$winner_cid" sh -c 'if [ -f /journal/journal.json ]; then cat /journal/journal.json; else printf "{\"events\":[],\"completions\":[]}"; fi' 2>/dev/null || true)
  stale_depth=$(jq -r '(.events|length)+(.completions|length)' <<<"$stale_journal_now" 2>/dev/null || printf '%s' -1)
  [[ "$stale_depth" == 0 ]] && break
  sleep 0.5
done
[[ "$stale_depth" == 0 ]] || fail "stale journal did not reconcile after reassignment"
probe_observed=$(compose logs --no-color "runner_$winner_name" 2>/dev/null | grep -F "journal_reconciliation=fenced attempt=1" || true)
[[ -n "$probe_observed" ]] || fail "stale journal was removed without observable explicit read-only lease probe fencing"
[[ "$(sql "SELECT count(*) FROM run_logs WHERE run_id='$run_id' AND event_key='$pending_event_key';")" == 0 ]] || fail "stale journal replay persisted a first mutation"
[[ "$(sql "SELECT (SELECT status FROM task_runs WHERE id='$run_id'),(SELECT string_agg(attempt::text||':'||status,',' ORDER BY attempt) FROM run_leases WHERE run_id='$run_id');")" == 'succeeded|1:expired,2:succeeded' ]] || fail "stale journal reconciliation changed authoritative terminal state"
record "stale_journal_reassignment=pending_events_$pending_events explicit_read_only_probe_404_observed journal_depth_zero stale_event_absent authoritative_state_unchanged"

sanitized_leases=$(sql "SELECT attempt,status,runner_id,(length(fence)>0)::text FROM run_leases WHERE run_id='$run_id' ORDER BY attempt;")
while IFS= read -r row; do record "lease_row=$row"; done <<<"$sanitized_leases"
record "final_assertion=run_succeeded exactly_two_leases no_active_lease revision=revision-b one_revision no_descendant_activity"
record "finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"

compose logs --no-color server runner_a runner_b >"$runtime_dir/runtime-logs.txt" 2>&1 || true
for secret in "$admin_token" "$token_a" "$token_b" "$fence1" "$fence2" runtime_fencing_only admin; do
  [[ -n "$secret" ]] || continue
  grep -F -- "$secret" "$evidence" "$runtime_dir/runtime-logs.txt" >/dev/null 2>&1 && fail "credential, fence, or password leaked into retained/runtime logs"
done
record "secret_scan=known session/runner tokens, fences, passwords absent"
sed -n -E '/"event":"(claimed_run|completed_run)"/p' "$runtime_dir/runtime-logs.txt" \
  | sed -E 's/"lease_id":"[^"]+"/"lease_id":"[redacted]"/' \
  | tail -n 8 \
  | while IFS= read -r line; do record "redacted_log_excerpt=$line"; done
passed=true
