#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
compose_file="$repo_root/acceptance/runtime-secrets/compose.yaml"
evidence=/tmp/nerocd-secret-containment-runtime.txt
runtime_dir=$(mktemp -d /tmp/nerocd-runtime-secrets.XXXXXXXX)
suffix=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')
project="nerocd-secrets-$suffix"
image="nerocd-runtime-secrets:$suffix"
passed=false

: >"$evidence"
record() { printf '%s\n' "$*" >>"$evidence"; }
fail() { record "FAIL: $*"; printf 'runtime-secrets-gate: %s\n' "$*" >&2; exit 1; }
compose() { NEROCD_RUNTIME_IMAGE="$image" docker compose --project-name "$project" --file "$compose_file" "$@"; }

cleanup() {
  result=$?
  set +e
  cleanup_failed=false
  if [[ "$project" =~ ^nerocd-secrets-[0-9a-f]{12}$ ]]; then
    compose down --volumes --remove-orphans --rmi local --timeout 5 >/dev/null 2>&1
    while IFS= read -r resource; do [[ -n "$resource" ]] && docker container rm --force "$resource" >/dev/null 2>&1; done < <(docker container ls --all --quiet --filter "label=com.docker.compose.project=$project")
    while IFS= read -r resource; do [[ -n "$resource" ]] && docker volume rm --force "$resource" >/dev/null 2>&1; done < <(docker volume ls --quiet --filter "label=com.docker.compose.project=$project")
    while IFS= read -r resource; do [[ -n "$resource" ]] && docker network rm "$resource" >/dev/null 2>&1; done < <(docker network ls --quiet --filter "label=com.docker.compose.project=$project")
    remaining=$(docker container ls --all --quiet --filter "label=com.docker.compose.project=$project"; docker volume ls --quiet --filter "label=com.docker.compose.project=$project"; docker network ls --quiet --filter "label=com.docker.compose.project=$project")
    [[ -z "$remaining" ]] || cleanup_failed=true
  else
    cleanup_failed=true
  fi
  [[ "$image" =~ ^nerocd-runtime-secrets:[0-9a-f]{12}$ ]] && docker image rm --force "$image" >/dev/null 2>&1
  case "$runtime_dir" in /tmp/nerocd-runtime-secrets.*) rm -rf -- "$runtime_dir" ;; esac
  if [[ "$cleanup_failed" == true ]]; then
    result=1
    record "cleanup_complete=false"
    record "FAIL: isolated runtime-secrets resources remain"
  else
    record "cleanup_complete=true"
  fi
  if [[ "$passed" == true && "$cleanup_failed" == false ]]; then
    record "PASS: runner_file containment and redaction gate"
  elif [[ $result -eq 0 ]]; then
    result=1
    record "FAIL: gate exited without every assertion"
  fi
  printf 'runtime secrets evidence: %s\n' "$evidence"
  exit "$result"
}
trap cleanup EXIT
trap 'code=$?; trap - ERR; fail "unexpected command failure at line $LINENO (exit $code)"' ERR

for tool in docker curl jq od openssl; do command -v "$tool" >/dev/null 2>&1 || fail "required tool missing: $tool"; done
docker info >/dev/null 2>&1 || fail "Docker daemon unavailable"
docker compose version >/dev/null 2>&1 || fail "Docker Compose unavailable"

record "gate=real runner_file authorization containment and pre-journal redaction"
record "scope=AC-13 transport prerequisite; not typed deployment/full AC-13"
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
journal_volume=''
journal_copy() {
  docker run --rm --network none --entrypoint /bin/sh -v "$journal_volume:/journal:ro" "$image" -ec 'if [ -f /journal/journal.json ]; then cat /journal/journal.json; else printf "{\"version\":1,\"events\":[],\"completions\":[]}"; fi' >"$1"
}
journal_depth() { journal_copy "$runtime_dir/journal-depth.json"; jq -er '(.events|length)+(.completions|length)' "$runtime_dir/journal-depth.json"; }
wait_run_status() {
  local run_id=$1 expected=$2 deadline=$((SECONDS + 90)) observed=''
  while (( SECONDS < deadline )); do
    observed=$(sql "SELECT status FROM task_runs WHERE id='$run_id';" || true)
    [[ "$observed" == "$expected" ]] && return 0
    sleep 0.25
  done
  fail "run $run_id did not reach $expected (last=$observed)"
}
create_secret_run() {
  local reference=$1 version=$2 marker=$3 wait_release=$4 expected_class=$5 response=$6
  local body code
  body=$(jq -cn --arg reference "$reference" --arg version "$version" --arg marker "$marker" --arg wait "$wait_release" --arg expected "$expected_class" '{project_id:"proj_platform",run_spec:{type:"shell",inputs:{fixture:"runtime-secrets"},process:{command:["/bin/sh","/fixtures/run-fixture.sh"],environment:{RUN_MARKER:$marker,WAIT_FOR_RELEASE:$wait,EXPECTED_SECRET_CLASS:$expected},timeout_seconds:90},secrets:[{name:"runtime-secret",provider:"runner_file",reference:$reference,target:"env:RUNTIME_SECRET",required:true,version:$version,fingerprint:"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",redact_encodings:["base64","base64url","hex"]}]},runner_tags:["runtime-secrets"]}')
  code=$(http_json POST "$base/api/v1/runs" "$admin_token" "$body" "$response")
  [[ "$code" == 201 ]] || fail "run creation for $marker returned HTTP $code"
  jq -er '.id | select(test("^[A-Za-z0-9_]+$"))' "$response"
}
start_or_restart_runner() {
  if [[ -n "$(compose ps --all --quiet runner)" ]]; then
    compose restart --timeout 5 runner >"$runtime_dir/runner-restart-$1.txt" 2>&1 || fail "runner restart failed ($1)"
  else
    compose up --detach runner >"$runtime_dir/runner-start-$1.txt" 2>&1 || fail "runner start failed ($1)"
  fi
}
marker_absent() {
  local marker=$1
  local cid
  cid=$(compose ps --quiet runner)
  [[ -n "$cid" ]] && ! docker exec "$cid" test -e "/state/$marker"
}

deadline=$((SECONDS + 60)); health=''
while (( SECONDS < deadline )); do
  health=$(curl --silent --max-time 2 --output /dev/null --write-out '%{http_code}' "$base/api/v1/health" || true)
  [[ "$health" == 200 ]] && break
  sleep 1
done
[[ "$health" == 200 ]] || fail "published server health unavailable"

code=$(http_json POST "$base/api/v1/sessions" '' '{"email":"admin@example.local","password":"admin"}' "$runtime_dir/session.json")
[[ "$code" == 201 ]] || fail "admin login returned HTTP $code"
admin_token=$(jq -er '.token | select(length>0)' "$runtime_dir/session.json") || fail "admin token missing"
code=$(http_json POST "$base/api/v1/runners/register" "$admin_token" '{"id":"runner_secrets","name":"Runtime Secrets","tags":["runtime-secrets"],"capabilities":["shell"]}' "$runtime_dir/register.json")
[[ "$code" == 201 ]] || fail "runner registration returned HTTP $code"
runner_token=$(jq -er '.token | select(length>0)' "$runtime_dir/register.json") || fail "runner token missing"

secret_v1="v1-$(openssl rand -hex 24)"
secret_v2="v2-$(openssl rand -hex 24)"
[[ "$secret_v1" != "$secret_v2" ]] || fail "random rotation values collided"
encoded_v1_b64=$(printf '%s' "$secret_v1" | base64 | tr -d '\n')
encoded_v1_url=$(printf '%s' "$secret_v1" | base64 | tr '+/' '-_' | tr -d '=\n')
encoded_v1_hex=$(printf '%s' "$secret_v1" | od -An -tx1 | tr -d ' \n')
encoded_v2_b64=$(printf '%s' "$secret_v2" | base64 | tr -d '\n')
encoded_v2_url=$(printf '%s' "$secret_v2" | base64 | tr '+/' '-_' | tr -d '=\n')
encoded_v2_hex=$(printf '%s' "$secret_v2" | od -An -tx1 | tr -d ' \n')

printf '%s\n' "$runner_token" | compose run --rm --no-deps --no-TTY credential_init >"$runtime_dir/credential-init.txt" 2>&1 || fail "credential/journal initialization failed"
printf '%s\n' "$secret_v1" | compose run --rm --no-deps --no-TTY secret_init >"$runtime_dir/secret-v1-init.txt" 2>&1 || fail "secure v1 secret initialization failed"
compose run --rm --no-deps --no-TTY invalid_secret_init >"$runtime_dir/invalid-secret-init.txt" 2>&1 || fail "negative secret fixtures failed"
secret_mode=$(compose run --rm --no-deps --no-TTY --entrypoint /bin/sh secret_init -ec 'stat -c "%u:%g|%a|%F" /secrets/root /secrets/root/runtime-token' 2>/dev/null | tail -n2 | tr '\n' ';')
[[ "$secret_mode" == *'10001:10001|700|directory'* && "$secret_mode" == *'10001:10001|400|regular file'* ]] || fail "secure secret root/file ownership or mode invalid"
record "credential=dedicated_0600 runner_nonroot_uid=10001 secret_root=0700 secret_file=0400 install=stdin"

primary_id=$(create_secret_run runtime-token v1 primary-marker true v1 "$runtime_dir/primary-run.json")
record "primary_run_id=$primary_id provider=runner_file version=v1 persisted_reference=logical_id_only"
start_or_restart_runner primary
runner_cid=$(compose ps --quiet runner)
[[ -n "$runner_cid" ]] || fail "runner container id missing"
journal_volume=$(docker volume ls --quiet --filter "label=com.docker.compose.project=$project" --filter 'label=com.docker.compose.volume=runner-journal' | head -n1)
[[ -n "$journal_volume" ]] || fail "runner journal volume missing"

deadline=$((SECONDS + 45)); lease_row=''; marker_ready=false
while (( SECONDS < deadline )); do
  lease_row=$(sql "SELECT id,attempt,(length(fence)>0)::text,floor(extract(epoch FROM (expires_at-clock_timestamp())))::bigint FROM run_leases WHERE run_id='$primary_id' AND status='active';" || true)
  docker exec "$runner_cid" test -e /state/primary-marker 2>/dev/null && marker_ready=true || true
  [[ -n "$lease_row" && "$marker_ready" == true ]] && break
  sleep 0.2
done
[[ -n "$lease_row" && "$marker_ready" == true ]] || fail "primary secret authorization/process barrier was not reached"
IFS='|' read -r primary_lease primary_attempt fence_present headroom <<<"$lease_row"
[[ "$primary_attempt" == 1 && "$fence_present" == true ]] || fail "primary authority malformed"
(( headroom >= 25 )) || fail "primary lease headroom insufficient for outage"
primary_fence=$(sql "SELECT fence FROM run_leases WHERE id='$primary_lease';")
audit_before=$(sql "SELECT count(*) FROM audit_events WHERE target_id='$primary_id' AND action='secret.access';")
[[ "$audit_before" == 1 ]] || fail "primary secret access audit count before use was $audit_before"
[[ "$(proxy_post outage/on)" == 204 ]] || fail "could not isolate runner API"
docker exec "$runner_cid" touch /state/release-output

deadline=$((SECONDS + 30)); depth=0; fixture_done=false
while (( SECONDS < deadline )); do
  depth=$(journal_depth)
  docker exec "$runner_cid" test -e /state/primary-marker.revision 2>/dev/null && fixture_done=true || true
  (( depth > 0 )) && [[ "$fixture_done" == true ]] && break
  sleep 0.2
done
(( depth > 0 )) && [[ "$fixture_done" == true ]] || fail "offline fixture did not grow journal and finish"
journal_copy "$runtime_dir/offline-journal.json"
for forbidden in "$secret_v1" "$encoded_v1_b64" "$encoded_v1_url" "$encoded_v1_hex" "$runner_token"; do
  [[ -n "$forbidden" ]] || continue
  ! grep -F -- "$forbidden" "$runtime_dir/offline-journal.json" >/dev/null 2>&1 || fail "secret or bearer credential appeared in offline journal"
done
redaction_count=$(grep -o '\[REDACTED\]' "$runtime_dir/offline-journal.json" | wc -l | tr -d ' ')
(( redaction_count >= 5 )) || fail "offline journal did not contain expected redaction markers"
db_events_during_outage=$(sql "SELECT count(*) FROM run_logs WHERE run_id='$primary_id' AND message IN ('safe-before-secret','safe-after-secret');")
[[ "$db_events_during_outage" == 0 ]] || fail "runner events reached database during API outage"
record "offline_journal=depth_$depth redaction_markers=$redaction_count raw_base64_base64url_hex_absent=true bearer_absent=true"

compose stop --timeout 5 runner >"$runtime_dir/runner-stop-outage.txt" 2>&1 || fail "runner stop during outage failed"
depth_stopped=$(journal_depth)
[[ "$depth_stopped" == "$depth" ]] || fail "runner stop mutated durable journal"
compose start runner >"$runtime_dir/runner-restart-outage.txt" 2>&1 || fail "runner restart during outage failed"
sleep 7
[[ "$(journal_depth)" == "$depth" ]] || fail "startup outage discarded or acknowledged journal"
runner_cid=$(compose ps --quiet runner)
docker exec "$runner_cid" test -e /state/runner.exit || fail "outage restart did not fail closed before claim/heartbeat"
record "restart_during_outage=true journal_preserved=true startup_failed_closed=true"

[[ "$(proxy_post outage/off)" == 204 ]] || fail "could not restore runner API"
start_or_restart_runner restored
wait_run_status "$primary_id" succeeded
deadline=$((SECONDS + 30)); final_depth=-1
while (( SECONDS < deadline )); do
  final_depth=$(journal_depth)
  [[ "$final_depth" == 0 ]] && break
  sleep 0.2
done
[[ "$final_depth" == 0 ]] || fail "journal did not drain after restored replay"
primary_terminal=$(sql "SELECT (SELECT count(*) FROM run_leases WHERE run_id='$primary_id' AND status='succeeded'),(SELECT count(*) FROM run_logs WHERE run_id='$primary_id' AND message='safe-before-secret'),(SELECT count(*) FROM run_logs WHERE run_id='$primary_id' AND message='safe-after-secret'),(SELECT count(*) FROM audit_events WHERE target_id='$primary_id' AND action='runner.complete');")
[[ "$primary_terminal" == '1|1|1|1' ]] || fail "primary exactly-once result invalid: $primary_terminal"
primary_revision=$(docker exec "$(compose ps --quiet runner)" cat /state/primary-marker.revision)
[[ "$primary_revision" == revision-v1 ]] || fail "primary attempt did not classify and use v1"
primary_event_order=$(sql "SELECT count(*),count(DISTINCT event_key),count(DISTINCT sequence),count(*) FILTER (WHERE message='[REDACTED]'),string_agg(message,',' ORDER BY sequence) FILTER (WHERE stream='stdout'),count(*) FILTER (WHERE stream='stderr' AND message='[REDACTED]') FROM run_logs WHERE run_id='$primary_id' AND message IN ('safe-before-secret','safe-after-secret','[REDACTED]');")
expected_stdout='safe-before-secret,[REDACTED],[REDACTED],[REDACTED],[REDACTED],[REDACTED],safe-after-secret'
[[ "$primary_event_order" == "8|8|8|6|$expected_stdout|1" ]] || fail "primary ordered redacted event set invalid: $primary_event_order"
primary_safe_audit=$(sql "SELECT count(*) FROM audit_events WHERE target_id='$primary_id' AND action='secret.access' AND metadata->>'lease_id'='$primary_lease' AND metadata->>'attempt'='1' AND metadata->>'binding'='runtime-secret' AND metadata->>'provider'='runner_file' AND metadata->>'version'='v1' AND NOT (metadata ?| array['fingerprint','reference','target','fence','value','path']);")
[[ "$primary_safe_audit" == 1 ]] || fail "primary safe idempotent audit metadata invalid"
record "restored_replay=journal_depth_zero eight_unique_ordered_redacted_events=true terminal_completion_once=true secret_access_audit_once_safe=true"

audit_count_before_stale=$(sql "SELECT count(*) FROM audit_events WHERE action='secret.access';")
stale_access_id="secret_access_$(openssl rand -hex 16)"
stale_body=$(jq -cn --arg access "$stale_access_id" --arg run "$primary_id" --arg lease "$primary_lease" --arg fence "$primary_fence" '{access_id:$access,run_id:$run,lease_id:$lease,attempt:1,fence:$fence,binding:"runtime-secret",provider:"runner_file",version:"v1"}')
code=$(http_json POST "$base/api/v1/runners/secrets/access" "$runner_token" "$stale_body" "$runtime_dir/stale-access.json")
[[ "$code" == 404 ]] || fail "completed/stale secret access returned HTTP $code instead of 404"
audit_count_after_stale=$(sql "SELECT count(*) FROM audit_events WHERE action='secret.access';")
[[ "$audit_count_after_stale" == "$audit_count_before_stale" ]] || fail "stale access created an audit"
record "post_completion_access_http=404 new_audit=0"

rm -f "$runtime_dir/stale-access.json"

# Block the real runner's authorization request before it can reach the server,
# then partition that runner until DB-clock expiry and the periodic reaper have
# revoked attempt 1. The absence of both audit and process marker proves the
# stale attempt did not read/inject the local secret or spawn its process.
blocked_before=$(proxy_status | jq -er '.blocked_secret_access_requests')
[[ "$(proxy_post secret-block/on)" == 204 ]] || fail "could not arm secret-access pre-read block"
stale_runtime_id=$(create_secret_run runtime-token v1 stale-preread-marker false v1 "$runtime_dir/stale-preread-run.json")
deadline=$((SECONDS + 30)); stale_attempt_row=''; blocked_seen=false
while (( SECONDS < deadline )); do
  stale_attempt_row=$(sql "SELECT id,attempt,fence,floor(extract(epoch FROM expires_at))::bigint FROM run_leases WHERE run_id='$stale_runtime_id' AND status='active';" || true)
  blocked_now=$(proxy_status | jq -er '.blocked_secret_access_requests')
  (( blocked_now > blocked_before )) && blocked_seen=true || true
  [[ -n "$stale_attempt_row" && "$blocked_seen" == true ]] && break
  sleep 0.2
done
[[ -n "$stale_attempt_row" && "$blocked_seen" == true ]] || fail "real runner did not block at pre-read secret authorization"
IFS='|' read -r stale_lease stale_attempt stale_fence stale_expiry_epoch <<<"$stale_attempt_row"
[[ "$stale_attempt" == 1 ]] || fail "pre-read stale fixture did not begin at attempt 1"
marker_absent stale-preread-marker || fail "process marker appeared before secret authorization"
[[ "$(sql "SELECT count(*) FROM audit_events WHERE target_id='$stale_runtime_id' AND action='secret.access';")" == 0 ]] || fail "blocked pre-read access reached audit persistence"

runner_cid=$(compose ps --quiet runner)
runtime_network=$(docker network ls --quiet --filter "label=com.docker.compose.project=$project" --filter 'label=com.docker.compose.network=runtime' | head -n1)
[[ -n "$runner_cid" && -n "$runtime_network" ]] || fail "runner/network identity unavailable for stale pre-read partition"
partitioned_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
docker network disconnect --force "$runtime_network" "$runner_cid" >/dev/null || fail "could not partition pre-read runner"
[[ "$(proxy_post secret-block/off)" == 204 ]] || fail "could not fail the held pre-read request after partition"
authority_loss_wait_started=$SECONDS
deadline=$((SECONDS + 65)); runner_stopped=false
while (( SECONDS < deadline )); do
  docker exec "$runner_cid" test -e /state/runner.exit 2>/dev/null && runner_stopped=true && break
  sleep 0.2
done
[[ "$runner_stopped" == true ]] || fail "partitioned pre-read runner did not fail closed"
authority_loss_wait_seconds=$((SECONDS - authority_loss_wait_started))
marker_absent stale-preread-marker || fail "stale attempt spawned process after authority loss"
[[ "$(sql "SELECT count(*) FROM audit_events WHERE target_id='$stale_runtime_id' AND action='secret.access';")" == 0 ]] || fail "stale pre-read attempt created secret audit"

deadline=$((SECONDS + 90)); reaper_state=''
while (( SECONDS < deadline )); do
  reaper_state=$(sql "SELECT (SELECT status FROM run_leases WHERE run_id='$stale_runtime_id' AND attempt=1),(SELECT status FROM task_runs WHERE id='$stale_runtime_id'),(SELECT count(*) FROM run_leases WHERE run_id='$stale_runtime_id' AND attempt=2),floor(extract(epoch FROM clock_timestamp()))::bigint;" || true)
  [[ "$reaper_state" == "expired|queued|0|"* ]] && break
  sleep 0.25
done
[[ "$reaper_state" == "expired|queued|0|"* ]] || fail "periodic reaper did not independently expire pre-read attempt: $reaper_state"
reaper_epoch=${reaper_state##*|}
(( reaper_epoch >= stale_expiry_epoch )) || fail "reaper state appeared before DB lease expiry"
record "stale_preread_partition_at=$partitioned_at blocked_before_upstream=true local_guard_fail_closed_seconds=$authority_loss_wait_seconds process_marker_before_reassign=absent secret_access_audit_before_reassign=0 reaper_state=attempt1_expired_run_queued_attempt2_absent"

docker network connect "$runtime_network" "$runner_cid" >/dev/null || fail "could not restore runner network for reassignment"
start_or_restart_runner stale-reassigned
wait_run_status "$stale_runtime_id" succeeded
runner_cid=$(compose ps --quiet runner)
stale_revision=$(docker exec "$runner_cid" cat /state/stale-preread-marker.revision)
[[ "$stale_revision" == revision-v1 ]] || fail "authoritative reassignment did not use classified v1"
stale_final=$(sql "SELECT string_agg(attempt::text||':'||status,',' ORDER BY attempt),(SELECT count(*) FROM audit_events WHERE target_id='$stale_runtime_id' AND action='secret.access') FROM run_leases WHERE run_id='$stale_runtime_id';")
[[ "$stale_final" == '1:expired,2:succeeded|1' ]] || fail "stale pre-read reassignment outcome invalid: $stale_final"
stale_audit_before=$(sql "SELECT count(*) FROM audit_events WHERE target_id='$stale_runtime_id' AND action='secret.access';")
stale_retry_id="secret_access_$(openssl rand -hex 16)"
stale_retry_body=$(jq -cn --arg access "$stale_retry_id" --arg run "$stale_runtime_id" --arg lease "$stale_lease" --arg fence "$stale_fence" '{access_id:$access,run_id:$run,lease_id:$lease,attempt:1,fence:$fence,binding:"runtime-secret",provider:"runner_file",version:"v1"}')
code=$(http_json POST "$base/api/v1/runners/secrets/access" "$runner_token" "$stale_retry_body" "$runtime_dir/stale-preread-retry.json")
[[ "$code" == 404 ]] || fail "expired pre-read attempt access returned HTTP $code instead of 404"
[[ "$(sql "SELECT count(*) FROM audit_events WHERE target_id='$stale_runtime_id' AND action='secret.access';")" == "$stale_audit_before" ]] || fail "expired pre-read retry created audit"
record "stale_preread_reassignment=attempt2_succeeded old_attempt_access_http_404 old_attempt_new_audit_0 process_spawn_attempt1_0"
rm -f "$runtime_dir/stale-preread-retry.json"

missing_id=$(create_secret_run missing-token missing-v1 missing-marker false v1 "$runtime_dir/missing-run.json")
start_or_restart_runner missing
wait_run_status "$missing_id" failed
marker_absent missing-marker || fail "missing secret spawned/mutated process fixture"
record "missing_secret=failed_before_process_spawn mutation_marker_absent=true"

symlink_id=$(create_secret_run symlink-token symlink-v1 symlink-marker false v1 "$runtime_dir/symlink-run.json")
start_or_restart_runner symlink
wait_run_status "$symlink_id" failed
marker_absent symlink-marker || fail "symlink secret spawned/mutated process fixture"
wide_id=$(create_secret_run wide-token wide-v1 wide-marker false v1 "$runtime_dir/wide-run.json")
start_or_restart_runner wide
wait_run_status "$wide_id" failed
marker_absent wide-marker || fail "permissive-mode secret spawned/mutated process fixture"
record "real_topology_denials=symlink,mode_0644 mutation_markers_absent=true"

printf '%s\n' "$secret_v2" | compose run --rm --no-deps --no-TTY secret_init >"$runtime_dir/secret-v2-init.txt" 2>&1 || fail "atomic v2 rotation failed"
rotation_id=$(create_secret_run runtime-token v2 rotation-marker false v2 "$runtime_dir/rotation-run.json")
start_or_restart_runner rotation
wait_run_status "$rotation_id" succeeded
runner_cid=$(compose ps --quiet runner)
rotation_revision=$(docker exec "$runner_cid" cat /state/rotation-marker.revision)
[[ "$rotation_revision" == revision-v2 ]] || fail "next attempt did not classify and use v2"
rotation_audit=$(sql "SELECT count(*) FROM audit_events WHERE target_id='$rotation_id' AND action='secret.access' AND metadata->>'version'='v2';")
[[ "$rotation_audit" == 1 ]] || fail "rotation v2 access audit missing"
record "rotation=atomic_file_replace fixture_classification=v2 next_attempt_version=v2 succeeded=true server_restart=false central_plaintext=false"

compose logs --no-color server proxy runner >"$runtime_dir/runtime-logs.txt" 2>&1 || true
sql "SELECT jsonb_build_object('runs',jsonb_agg(jsonb_build_object('id',id,'status',status)),'secret_access_audits',(SELECT count(*) FROM audit_events WHERE action='secret.access')) FROM task_runs WHERE id IN ('$primary_id','$stale_runtime_id','$missing_id','$symlink_id','$wide_id','$rotation_id');" >"$runtime_dir/db-summary.json"
code=$(http_json GET "$base/api/v1/run-logs?run_id=$primary_id&limit=200" "$admin_token" '{}' "$runtime_dir/api-logs.json")
[[ "$code" == 200 ]] || fail "run logs observational API returned HTTP $code"
for forbidden in "$secret_v1" "$encoded_v1_b64" "$encoded_v1_url" "$encoded_v1_hex" "$secret_v2" "$encoded_v2_b64" "$encoded_v2_url" "$encoded_v2_hex" "$admin_token" "$runner_token" "$primary_fence" runtime_secrets_only; do
  [[ -n "$forbidden" ]] || continue
  ! grep -F -- "$forbidden" "$evidence" "$runtime_dir/runtime-logs.txt" "$runtime_dir/db-summary.json" "$runtime_dir/api-logs.json" >/dev/null 2>&1 || fail "secret, credential, fence, or password leaked into retained surfaces"
done
database_leak_count=$(compose exec --no-TTY postgres psql --username nerocd --dbname nerocd --tuples-only --no-align --set ON_ERROR_STOP=1 <<SQL | sed '/^[[:space:]]*$/d'
SELECT
  (SELECT count(*) FROM task_runs WHERE run_spec::text LIKE '%${secret_v1}%' OR run_spec::text LIKE '%${secret_v2}%') +
  (SELECT count(*) FROM run_logs WHERE message LIKE '%${secret_v1}%' OR message LIKE '%${secret_v2}%' OR message LIKE '%${encoded_v1_b64}%' OR message LIKE '%${encoded_v1_url}%' OR message LIKE '%${encoded_v1_hex}%' OR message LIKE '%${encoded_v2_b64}%' OR message LIKE '%${encoded_v2_url}%' OR message LIKE '%${encoded_v2_hex}%') +
  (SELECT count(*) FROM audit_events WHERE metadata::text LIKE '%${secret_v1}%' OR metadata::text LIKE '%${secret_v2}%') +
  (SELECT count(*) FROM run_artifacts WHERE path::text LIKE '%${secret_v1}%' OR path::text LIKE '%${secret_v2}%');
SQL
)
[[ "$database_leak_count" == 0 ]] || fail "plaintext or configured encoding persisted in PostgreSQL"
fingerprint_audit_count=$(sql "SELECT count(*) FROM audit_events WHERE action='secret.access' AND (metadata ? 'fingerprint' OR metadata::text LIKE '%sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd%');")
[[ "$fingerprint_audit_count" == 0 ]] || fail "configured fingerprint persisted in secret.access audit"
reference_shape=$(sql "SELECT count(*) FROM task_runs WHERE id IN ('$primary_id','$stale_runtime_id','$rotation_id') AND run_spec::text LIKE '%runtime-token%' AND run_spec::text NOT LIKE '%/secrets/%';")
[[ "$reference_shape" == 3 ]] || fail "server did not persist logical-only runner_file references"
record "containment_scan=journal,PostgreSQL_run_spec_logs_audit_artifacts,HTTP,container_logs,evidence"
record "containment_result=raw_base64_base64url_hex_credentials_fence_password_absent"
record "final_assertion=one_primary_attempt exact_ordered_redacted_events safe_idempotent_audit_without_fingerprint journal_depth_zero real_stale_preread_fail_closed_reassigned missing_symlink_mode_fail_closed rotation_v2_classified"
record "finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
passed=true
