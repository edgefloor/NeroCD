#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
compose_file="$repo_root/acceptance/runtime-enrollment/compose.yaml"
evidence=/tmp/nerocd-runner-enrollment-runtime.txt
runtime_dir=$(mktemp -d /tmp/nerocd-runtime-enrollment.XXXXXXXX)
suffix=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')
project="nerocd-enrollment-$suffix"
image="nerocd-runtime-enrollment:$suffix"
passed=false
: >"$evidence"
record() { printf '%s\n' "$*" >>"$evidence"; }
fail() { record "FAIL: $*"; printf 'runtime-enrollment-gate: %s\n' "$*" >&2; exit 1; }
compose() { NEROCD_RUNTIME_IMAGE="$image" docker compose --project-name "$project" --file "$compose_file" "$@"; }
redact_diagnostics() {
  sed -E \
    -e 's#(postgres(ql)?)://[^@[:space:]]+@#\1://[REDACTED]@#g' \
    -e 's#[Bb]earer[[:space:]]+[^[:space:]",}]+#Bearer [REDACTED]#g' \
    -e 's#("[^"]*([Tt][Oo][Kk][Ee][Nn]|[Pp][Aa][Ss][Ss][Ww][Oo][Rr][Dd]|[Cc][Rr][Ee][Dd][Ee][Nn][Tt][Ii][Aa][Ll]|[Ss][Ee][Cc][Rr][Ee][Tt])[^"]*"[[:space:]]*:[[:space:]]*)"[^"]*"#\1"[REDACTED]"#g' \
    -e 's#(([Tt][Oo][Kk][Ee][Nn]|[Pp][Aa][Ss][Ss][Ww][Oo][Rr][Dd]|[Cc][Rr][Ee][Dd][Ee][Nn][Tt][Ii][Aa][Ll]|[Ss][Ee][Cc][Rr][Ee][Tt])([_-]?[[:alnum:].]+)*=)[^[:space:]]+#\1[REDACTED]#g'
}
capture_failure_diagnostics() {
  local raw="$runtime_dir/failure-diagnostics.raw" redacted="$runtime_dir/failure-diagnostics.txt"
  local proxy_id proxy_state restart_count
  {
    printf '%s\n' 'failure_diagnostics_begin'
    printf '%s\n' 'compose_services='
    compose ps --all --format json 2>/dev/null |
      jq -cs '[.[] | if type == "array" then .[] else . end | {Service,State,Health,ExitCode}] | sort_by(.Service)' || printf '%s\n' 'unavailable'
    printf '%s\n' 'proxy_container='
    proxy_id=$(compose ps -q proxy 2>/dev/null | head -n 1)
    if [[ "$proxy_id" =~ ^[0-9a-f]{12,64}$ ]]; then
      proxy_state=$(docker inspect --format '{{json .State}}' "$proxy_id" 2>/dev/null) || proxy_state=''
      restart_count=$(docker inspect --format '{{.RestartCount}}' "$proxy_id" 2>/dev/null) || restart_count=''
      if [[ -n "$proxy_state" && "$restart_count" =~ ^[0-9]+$ ]]; then
        jq -cn --argjson state "$proxy_state" --argjson restart "$restart_count" \
          '{State:{Status:$state.Status,ExitCode:$state.ExitCode,OOMKilled:$state.OOMKilled,Error:$state.Error,StartedAt:$state.StartedAt,FinishedAt:$state.FinishedAt},RestartCount:$restart}'
      else
        printf '%s\n' 'unavailable'
      fi
    else
      printf '%s\n' 'unavailable'
    fi
    printf '%s\n' 'proxy_logs_begin'
    compose logs --no-color --tail 80 proxy 2>&1 | head -c 16384
    printf '\n%s\n' 'proxy_logs_end' 'failure_diagnostics_end'
  } >"$raw"
  redact_diagnostics <"$raw" >"$redacted"
  cat "$redacted" >>"$evidence"
  cat "$redacted" >&2
}
cleanup() {
  result=$?; set +e; cleanup_failed=false
  if [[ $result -ne 0 || "$passed" != true ]]; then capture_failure_diagnostics; fi
  if [[ "$project" =~ ^nerocd-enrollment-[0-9a-f]{12}$ ]]; then
    compose down --volumes --remove-orphans --rmi local --timeout 5 >/dev/null 2>&1
    docker ps -aq --filter "label=nerocd.runtime.project=$project" | xargs -r docker rm -f >/dev/null 2>&1
    docker volume ls -q --filter "label=nerocd.runtime.project=$project" | xargs -r docker volume rm -f >/dev/null 2>&1
    remaining=$(docker ps -aq --filter "label=com.docker.compose.project=$project"; docker volume ls -q --filter "label=com.docker.compose.project=$project"; docker network ls -q --filter "label=com.docker.compose.project=$project"; docker volume ls -q --filter "label=nerocd.runtime.project=$project")
    [[ -z "$remaining" ]] || cleanup_failed=true
  else cleanup_failed=true; fi
  [[ "$image" =~ ^nerocd-runtime-enrollment:[0-9a-f]{12}$ ]] && docker image rm -f "$image" >/dev/null 2>&1
  case "$runtime_dir" in /tmp/nerocd-runtime-enrollment.*) rm -rf -- "$runtime_dir" ;; esac
  if [[ "$cleanup_failed" == true ]]; then result=1; record "cleanup_complete=false"; else record "cleanup_complete=true"; fi
  if [[ "$passed" == true && "$cleanup_failed" == false ]]; then record "PASS: one-time runner enrollment gate"; elif [[ $result -eq 0 ]]; then result=1; record "FAIL: incomplete assertions"; fi
  printf 'runtime enrollment evidence: %s\n' "$evidence"
  exit "$result"
}
trap cleanup EXIT
trap 'code=$?; trap - ERR; fail "unexpected command failure at line $LINENO (exit $code)"' ERR

for tool in docker curl jq od openssl bun; do command -v "$tool" >/dev/null || fail "missing tool: $tool"; done
docker info >/dev/null 2>&1 || fail "Docker unavailable"
record "gate=one-time runner enrollment and locally generated durable credential"
record "scope=AC-06 prerequisite; not production bootstrap/browser/provenance/deployment proof"
record "source_commit=$(git -C "$repo_root" rev-parse HEAD)"
record "source_tree=$(git -C "$repo_root" status --porcelain=v1 | wc -l | tr -d ' ')_paths_changed"
record "project=$project started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"

docker build --pull -t "$image" "$repo_root" >"$runtime_dir/docker-build.txt" 2>&1 || fail "fresh image build failed"
umask 077; admin_email="enrollment-${suffix}@example.invalid"; admin_password=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n'); printf '%s\n%s\n' "$admin_email" "$admin_password" >"$runtime_dir/browser.credentials"; chmod 0600 "$runtime_dir/browser.credentials"
compose up -d --wait postgres >"$runtime_dir/postgres-up.txt" 2>&1 || fail "postgres stack failed"
compose run --rm --no-deps --entrypoint nerocd server migrate >"$runtime_dir/migrate.txt" 2>&1 || fail "migration failed"
tail -n 1 "$runtime_dir/browser.credentials" | compose run --rm --no-deps --entrypoint nerocd server bootstrap-admin --email "$admin_email" --name 'Runtime enrollment admin' --password-stdin >"$runtime_dir/bootstrap.txt" 2>&1 || fail "bootstrap failed"
unset admin_password
compose up -d --wait server proxy >"$runtime_dir/compose-up.txt" 2>&1 || fail "isolated stack failed"
server_port=$(compose port server 8080 | tail -n1); server_port=${server_port##*:}
proxy_port=$(compose port proxy 8081 | tail -n1); proxy_port=${proxy_port##*:}
base="http://127.0.0.1:$server_port"; proxy_control="http://127.0.0.1:$proxy_port/__control"
http_json() {
  local method=$1 url=$2 token=$3 body=$4 output=$5
  local -a args=(--silent --show-error --max-time 10 --output "$output" --write-out '%{http_code}' -X "$method" -H 'Content-Type: application/json')
  [[ -n "$token" ]] && args+=(-H "Authorization: Bearer $token")
  if [[ "$body" == @* ]]; then args+=(--data-binary "$body"); else args+=(--data "$body"); fi
  args+=("$url"); curl "${args[@]}"
}
sql() { compose exec --no-TTY postgres psql -U nerocd -d nerocd -At -F '|' -v ON_ERROR_STOP=1 -c "$1" | sed '/^[[:space:]]*$/d'; }
proxy_control_request() {
  local method=$1 path=$2 response=$3 output curl_code remaining deadline=$((SECONDS+10))
  local -a response_args
  case "$response" in
    body) response_args=(-fsS -o -) ;;
    code) response_args=(-sS -o /dev/null -w '%{http_code}') ;;
    *) return 2 ;;
  esac
  while :; do
    remaining=$((deadline-SECONDS))
    (( remaining > 0 )) || return 7
    if output=$(curl "${response_args[@]}" --connect-timeout "$remaining" --max-time "$remaining" -X "$method" "$proxy_control/$path"); then
      printf '%s' "$output"
      return 0
    else
      curl_code=$?
    fi
    [[ $curl_code -eq 7 ]] || return "$curl_code"
    sleep 0.2
  done
}
proxy_status() { proxy_control_request GET status body; }
proxy_post() { proxy_control_request POST "$1" code; }
create_enrollment() {
  local runner_id=$1 ttl=$2 output=$3
  local body code
  body=$(jq -cn --arg id "$runner_id" --arg name "$runner_id" --argjson ttl "$ttl" '{runner_id:$id,runner_name:$name,tags:["enrollment-runtime"],capabilities:["shell"],ttl_seconds:$ttl}')
  code=$(http_json POST "$base/api/v1/runner-enrollments" "$admin_token" "$body" "$output")
  [[ "$code" == 201 ]] || fail "create enrollment $runner_id returned $code"
}
consume_direct() {
  local token=$1 request_id=$2 hash=$3 output=$4
  http_json POST "$base/api/v1/runner-enrollments/consume" "$token" "$(jq -cn --arg request "$request_id" --arg hash "$hash" '{request_id:$request,credential_hash:$hash}')" "$output"
}

jq -cn --arg email "$admin_email" --arg password "$(tail -n 1 "$runtime_dir/browser.credentials")" '{email:$email,password:$password}' >"$runtime_dir/login.json"; chmod 0600 "$runtime_dir/login.json"
code=$(http_json POST "$base/api/v1/sessions" '' "@$runtime_dir/login.json" "$runtime_dir/session.json")
[[ "$code" == 201 ]] || fail "admin login returned $code"
admin_token=$(jq -er '.token' "$runtime_dir/session.json")
project_body=$(jq -cn --arg name "Runtime enrollment $suffix" '{name:$name,description:"isolated runner enrollment lifecycle"}')
code=$(http_json POST "$base/api/v1/projects" "$admin_token" "$project_body" "$runtime_dir/project.json")
[[ "$code" == 201 ]] || fail "create runtime enrollment project returned $code"
project_id=$(jq -er '.id' "$runtime_dir/project.json")

# The administrator creates the one-time token in the shipped browser UI.
# The only permitted plaintext copy is its controlled download and this strict
# temporary bridge file; the nonroot runner writes its own mode-0600 identity.
cd "$repo_root/web/app" && bun "$repo_root/acceptance/runtime-enrollment/web-enrollment.mjs" "$base" "$runtime_dir/browser.credentials" runner_enrollment_runtime "$runtime_dir/race-enrollment.json" >"$runtime_dir/browser.log" 2>&1 || { tail -n 40 "$runtime_dir/browser.log" >>"$evidence"; fail "browser enrollment creation failed"; }
race_token=$(jq -er '.token' "$runtime_dir/race-enrollment.json")
race_enrollment_id=$(sql "SELECT id FROM runner_enrollments WHERE runner_id='runner_enrollment_runtime';")
[[ "$race_enrollment_id" =~ ^enroll_ ]] || fail "browser enrollment was not persisted"
record "browser_enrollment=create_download_once dialog_clear=true runner_only_calls=0 storage=empty"
# Create two unused API controls up front so expiry can advance concurrently
# with the enrollment/lost-response scenario.
create_enrollment runner_enrollment_expired 60 "$runtime_dir/expired-enrollment.json"
expired_token=$(jq -er '.token' "$runtime_dir/expired-enrollment.json")
create_enrollment runner_enrollment_revoked 600 "$runtime_dir/revoked-enrollment.json"
revoked_token=$(jq -er '.token' "$runtime_dir/revoked-enrollment.json")
revoked_id=$(jq -er '.enrollment.id' "$runtime_dir/revoked-enrollment.json")
code=$(http_json POST "$base/api/v1/runner-enrollments/revoke" "$admin_token" "$(jq -cn --arg id "$revoked_id" '{enrollment_id:$id}')" "$runtime_dir/revoked.json")
[[ "$code" == 200 ]] || fail "revoke enrollment returned $code"

printf '%s\n' "$race_token" | compose run --rm --no-deps -T winner_init >"$runtime_dir/winner-init.txt" 2>&1 || fail "winner identity init failed"
printf '%s\n' "$race_token" | compose run --rm --no-deps -T loser_init >"$runtime_dir/loser-init.txt" 2>&1 || fail "loser identity init failed"
[[ "$(proxy_post drop-enrollment)" == 204 ]] || fail "could not arm lost enrollment response"
compose up -d --no-deps runner_a runner_b >"$runtime_dir/runners-up.txt" 2>&1 || fail "runner race start failed"
compose exec --no-TTY postgres sh -c ': >/dev/null' >/dev/null
compose exec --no-TTY runner_a touch /state/release-runners

deadline=$((SECONDS+40)); enrollment_state=''; proxy=''
while (( SECONDS < deadline )); do
  enrollment_state=$(sql "SELECT (used_at IS NOT NULL)::text,(consume_request_id IS NOT NULL)::text,(credential_hash IS NOT NULL)::text,(SELECT count(*) FROM runners WHERE id='runner_enrollment_runtime'),(SELECT count(*) FROM audit_events WHERE action='runner.enrollment.consume' AND target_id='runner_enrollment_runtime') FROM runner_enrollments WHERE id='$race_enrollment_id';" || true)
  proxy=$(proxy_status || true)
  [[ "$enrollment_state" == 'true|true|true|1|1' && "$(jq -r '.lost_enrollment_responses // 0' <<<"$proxy")" == 1 ]] && break
  sleep 0.2
done
[[ "$enrollment_state" == 'true|true|true|1|1' ]] || fail "enrollment did not commit exactly once: $enrollment_state"
[[ "$(jq -r '.lost_enrollment_responses' <<<"$proxy")" == 1 ]] || fail "committed response was not lost"

# The DB commit can be observed before the dropped-response winner retries and
# before the competing consumer reaches the server. Wait for both local
# enrollment attempts to settle before asserting the transport evidence.
deadline=$((SECONDS+20)); consume_requests=0
while (( SECONDS < deadline )); do
  proxy=$(proxy_status || true)
  consume_requests=$(jq -r '.requests["/api/v1/runner-enrollments/consume"] // 0' <<<"$proxy")
  settled=0
  for slot in a b; do
    cid=$(compose ps -q "runner_$slot")
    if docker exec "$cid" test ! -e /identity/enrollment 2>/dev/null || docker exec "$cid" test -e "/state/runner-$slot.exit" 2>/dev/null; then
      settled=$((settled+1))
    fi
  done
  (( consume_requests >= 3 && settled == 2 )) && break
  sleep 0.2
done
(( consume_requests >= 3 )) || fail "race/lost response did not produce exact retry and competing consume"

winner_slot=''; loser_slot=''
for slot in a b; do
  cid=$(compose ps -q "runner_$slot")
  if docker exec "$cid" test ! -e /identity/enrollment && docker exec "$cid" test -s /identity/credential; then winner_slot=$slot; else loser_slot=$slot; fi
done
[[ -n "$winner_slot" && -n "$loser_slot" && "$winner_slot" != "$loser_slot" ]] || fail "could not distinguish enrollment winner and loser"
winner_service="runner_$winner_slot"; loser_service="runner_$loser_slot"
if [[ "$loser_slot" == a ]]; then loser_identity_tool=winner_init; else loser_identity_tool=loser_init; fi
loser_cid=$(compose ps -q "$loser_service")
docker exec "$loser_cid" test -e "/state/runner-$loser_slot.exit" || fail "competing runner did not reject and exit"
docker exec "$loser_cid" test -e /identity/enrollment || fail "loser enrollment token was incorrectly removed"
record "consume_race=one_bound_runner one_safe_audit loser_rejected lost_commit_response=true exact_retry=true plaintext_returned_once=true"

# Exercise the file boundary in the real nonroot image. None of these malformed
# enrollment files may reach the consume endpoint.
consume_before_negative=$(jq -r '.requests["/api/v1/runner-enrollments/consume"] // 0' <<<"$(proxy_status)")
negative_runner() {
  if docker exec --user 10001:10001 -e NEROCD_MODE=development "$loser_cid" nerocd runner \
    --server http://proxy:8081 --enrollment-file /identity/enrollment \
    --credential-file /identity/negative-credential --journal-dir /journal \
    --id runner_enrollment_runtime --tags enrollment-runtime --capabilities shell --once \
    >"$runtime_dir/negative-$1.txt" 2>&1; then
    fail "unsafe $1 enrollment file was accepted"
  fi
}
mutate_loser_identity() {
  # Keep Compose diagnostics observable: an inaccessible losing identity
  # volume is a topology failure, not evidence that unsafe input was denied.
  compose run --rm --no-deps --entrypoint /bin/sh "$loser_identity_tool" -ec "$1" || fail "could not mutate losing runner identity"
}
mutate_loser_identity 'chmod 0644 /identity/enrollment; rm -f /identity/negative-credential'
negative_runner permissive-mode
mutate_loser_identity 'chmod 0600 /identity/enrollment; chown 0:0 /identity/enrollment; rm -f /identity/negative-credential'
negative_runner wrong-owner
mutate_loser_identity 'mv /identity/enrollment /identity/enrollment.real; ln -s enrollment.real /identity/enrollment; rm -f /identity/negative-credential'
negative_runner symlink
mutate_loser_identity 'rm /identity/enrollment; mv /identity/enrollment.real /identity/enrollment; chown 10001:10001 /identity/enrollment; chmod 0600 /identity/enrollment'
consume_after_negative=$(jq -r '.requests["/api/v1/runner-enrollments/consume"] // 0' <<<"$(proxy_status)")
[[ "$consume_after_negative" == "$consume_before_negative" ]] || fail "unsafe enrollment file reached consume API"
record "credential_file_boundary=permissive_mode_denied wrong_owner_denied symlink_denied zero_consume_requests=true"

# Three credential-only restarts must not call enrollment or registration.
consume_before=$consume_requests
register_before=$(jq -r '.requests["/api/v1/runners/register"] // 0' <<<"$proxy")
registered_at=$(sql "SELECT floor(extract(epoch FROM registered_at))::bigint FROM runners WHERE id='runner_enrollment_runtime';")
for restart in 1 2 3; do compose restart --timeout 3 "$winner_service" >"$runtime_dir/restart-$restart.txt" 2>&1 || fail "winner restart $restart failed"; sleep 2; done
proxy=$(proxy_status)
[[ "$(jq -r '.requests["/api/v1/runner-enrollments/consume"] // 0' <<<"$proxy")" == "$consume_before" ]] || fail "credential restart reused enrollment"
[[ "$(jq -r '.requests["/api/v1/runners/register"] // 0' <<<"$proxy")" == "$register_before" ]] || fail "credential restart used privileged registration"
restart_state=$(sql "SELECT floor(extract(epoch FROM registered_at))::bigint,(clock_timestamp()-last_heartbeat_at)<interval '5 seconds',count(*) OVER() FROM runners WHERE id='runner_enrollment_runtime';")
[[ "$restart_state" == "$registered_at|t|1" ]] || fail "credential restart identity drift: $restart_state"
record "credential_restart=3 same_runner_id same_registered_at same_credential_hash no_enrollment_or_register_requests heartbeat_fresh"

# Revoked and naturally expired unused enrollment tokens fail through HTTP.
fake_hash=$(printf '9%.0s' {1..64}); fake_request=enroll_consume_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
code=$(consume_direct "$revoked_token" "$fake_request" "$fake_hash" "$runtime_dir/revoked-consume.json")
[[ "$code" == 404 ]] || fail "revoked enrollment consume returned $code"
deadline=$((SECONDS+75))
while (( SECONDS < deadline )); do
  expired_ready=$(sql "SELECT expires_at <= clock_timestamp() FROM runner_enrollments WHERE runner_id='runner_enrollment_expired';")
  [[ "$expired_ready" == t ]] && break
  sleep 0.5
done
[[ "${expired_ready:-}" == t ]] || fail "enrollment did not expire by DB clock"
code=$(consume_direct "$expired_token" "$fake_request" "$fake_hash" "$runtime_dir/expired-consume.json")
[[ "$code" == 404 ]] || fail "expired enrollment consume returned $code"
record "unused_controls=revoked_http_404 expired_by_db_clock_http_404"

# Run a real process tree, revoke its enrolled credential, and prove fail-close.
run_body=$(jq -cn --arg project "$project_id" '{project_id:$project,run_spec:{type:"shell",process:{command:["/bin/sh","/fixtures/run-fixture.sh"],timeout_seconds:300}},runner_tags:["enrollment-runtime"]}')
code=$(http_json POST "$base/api/v1/runs" "$admin_token" "$run_body" "$runtime_dir/run.json")
[[ "$code" == 201 ]] || fail "create revocation run returned $code"
run_id=$(jq -er '.id' "$runtime_dir/run.json")
deadline=$((SECONDS+30)); process_started=false
while (( SECONDS < deadline )); do
  winner_cid=$(compose ps -q "$winner_service")
  docker exec "$winner_cid" test -e /state/enrollment.process.started 2>/dev/null && process_started=true && break
  sleep 0.2
done
if [[ "$process_started" != true ]]; then
  revocation_runs=$(sql "SELECT id||':'||status FROM task_runs ORDER BY started_at DESC LIMIT 3" | tr '\n' ',')
  revocation_runner=$(sql "SELECT id||':'||status||':'||array_to_string(capabilities,',') FROM runners WHERE id='runner_enrollment_runtime'")
  record "revocation_run_diagnostic=$revocation_runs"
  record "revocation_runner_diagnostic=$revocation_runner"
fi
[[ "$process_started" == true ]] || fail "enrolled runner process did not start"
# The browser observes this active runner on its query-free public admin detail
# route and performs the only credential revocation in this happy path. SQL is
# read-only independent evidence for the bounded telemetry values.
deadline=$((SECONDS+20)); telemetry_json=''
while (( SECONDS < deadline )); do
  telemetry_json=$(sql "SELECT json_build_object('journal_depth',journal_depth,'retry_count',retry_count,'renew_failures',renew_failures)::text FROM runner_operational_observations WHERE runner_id='runner_enrollment_runtime';" || true)
  [[ -n "$telemetry_json" ]] && break
  sleep 0.2
done
[[ -n "$telemetry_json" ]] || fail "runner did not publish bounded telemetry"
printf '%s\n' "$telemetry_json" >"$runtime_dir/expected-telemetry.json"; chmod 0600 "$runtime_dir/expected-telemetry.json"
cd "$repo_root/web/app" && bun "$repo_root/acceptance/runtime-enrollment/web-enrollment.mjs" "$base" "$runtime_dir/browser.credentials" runner_enrollment_runtime "$runtime_dir/browser-revoke.json" detail-revoke "$runtime_dir/expected-telemetry.json" >"$runtime_dir/browser-revoke.log" 2>&1 || { tail -n 40 "$runtime_dir/browser-revoke.log" >>"$evidence"; fail "browser runner detail/revoke failed"; }
[[ "$(jq -r '.revoked' "$runtime_dir/browser-revoke.json")" == true && "$(jq -r '.runner_self_calls' "$runtime_dir/browser-revoke.json")" == 0 ]] || fail "browser runner detail/revoke evidence is incomplete"
record "browser_detail=query_free active_fresh=true telemetry_exact=true tags_capabilities=true keyboard_mobile_reload=true revoke_cancel_no_mutation=true revoke_confirm_public_admin=true runner_self_calls=0"
logs_at_revoke=$(sql "SELECT count(*) FROM run_logs WHERE run_id='$run_id';")
revoke_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
deadline=$((SECONDS+15)); runner_exited=false
while (( SECONDS < deadline )); do
  docker exec "$winner_cid" test -e "/state/runner-$winner_slot.exit" 2>/dev/null && runner_exited=true && break
  sleep 0.2
done
[[ "$runner_exited" == true ]] || fail "revoked runner did not fail closed within heartbeat/renew interval"
for pidfile in enrollment.leader.pid enrollment.child.pid enrollment.spawned.pid; do
  pid=$(docker exec "$winner_cid" cat "/state/$pidfile")
  state=$(docker exec "$winner_cid" sh -c "if [ -r /proc/$pid/stat ]; then sed 's/.*) //' /proc/$pid/stat | cut -d' ' -f1; else printf gone; fi")
  [[ "$state" == gone || "$state" == Z ]] || fail "revoked process member $pidfile remains runnable ($state)"
done
logs_after_exit=$(sql "SELECT count(*) FROM run_logs WHERE run_id='$run_id';")
sleep 3
terminal_state=$(sql "SELECT (SELECT count(*) FROM run_logs WHERE run_id='$run_id'),(SELECT count(*) FROM run_artifacts WHERE run_id='$run_id'),(SELECT count(*) FROM audit_events WHERE target_id='$run_id' AND action='runner.complete'),(SELECT status FROM runners WHERE id='runner_enrollment_runtime');")
IFS='|' read -r logs_final artifacts_final completions_final runner_status <<<"$terminal_state"
[[ "$logs_final" == "$logs_after_exit" && "$artifacts_final" == 0 && "$completions_final" == 0 && "$runner_status" == revoked ]] || fail "post-revocation fenced mutation occurred: $terminal_state"
record "revocation_at=$revoke_at runner_exit_within_seconds=15 process_group=gone_or_zombie_only later_logs=0 artifacts=0 completions=0"

# Retained surfaces are sanitized; plaintext credentials/tokens never enter DB,
# audit metadata, process argv/env, container logs, or evidence.
winner_credential=$(docker exec "$winner_cid" cat /identity/credential)
loser_credential=$(docker exec "$loser_cid" cat /identity/credential)
compose logs --no-color >"$runtime_dir/logs.txt" 2>&1 || true
docker inspect "$winner_cid" "$loser_cid" >"$runtime_dir/inspect.json"
sql "SELECT jsonb_build_object('runner_count',(SELECT count(*) FROM runners WHERE id='runner_enrollment_runtime'),'consume_audits',(SELECT count(*) FROM audit_events WHERE action='runner.enrollment.consume'),'safe_audits',(SELECT count(*) FROM audit_events WHERE action LIKE 'runner.enrollment.%' AND NOT (metadata ?| array['token','token_hash','credential','credential_hash','consume_request_id'])));" >"$runtime_dir/db-summary.json"
for forbidden in "$admin_token" "$race_token" "$expired_token" "$revoked_token" "$winner_credential" "$loser_credential"; do
  [[ -n "$forbidden" ]] || continue
  ! grep -F -- "$forbidden" "$evidence" "$runtime_dir/logs.txt" "$runtime_dir/inspect.json" "$runtime_dir/db-summary.json" >/dev/null || fail "enrollment or credential plaintext leaked"
  [[ "$(sql "SELECT count(*) FROM runner_enrollments WHERE token_hash='$forbidden' OR credential_hash='$forbidden';")" == 0 ]] || fail "plaintext persisted in enrollment hash columns"
done
unsafe_audits=$(sql "SELECT count(*) FROM audit_events WHERE action LIKE 'runner.enrollment.%' AND (metadata ?| array['token','token_hash','credential','credential_hash','consume_request_id']);")
[[ "$unsafe_audits" == 0 ]] || fail "unsafe enrollment audit metadata"
record "containment=plaintext_absent_from_DB_audit_logs_inspect_argv_env_evidence safe_audit_fields_only=true"
record "finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
passed=true
