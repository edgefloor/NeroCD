#!/usr/bin/env bash
# Real, isolated provenance/replay gate. It resolves only: no target Compose
# project is ever applied by the runner or this harness.
set -Eeuo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
file="$root/acceptance/runtime-provenance/compose.yaml"
evidence=/tmp/nerocd-provenance-runtime.txt
dir=$(mktemp -d /tmp/nerocd-runtime-provenance.XXXXXXXX)
suffix=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')
project="nerocd-provenance-$suffix" image="nerocd-runtime-provenance:$suffix" resolver="nerocd-provenance-resolver:$suffix" gitimg="nerocd-provenance-git:$suffix" pass=false
: >"$evidence"; record(){ printf '%s\n' "$*" >>"$evidence"; }; fail(){ trap - ERR; record "FAIL: $*"; printf 'runtime-provenance: %s\n' "$*" >&2; exit 1; }
compose(){ NEROCD_RUNTIME_IMAGE="$image" NEROCD_RESOLVER_IMAGE="$resolver" NEROCD_GIT_FIXTURE_IMAGE="$gitimg" docker compose -p "$project" -f "$file" "$@"; }
diag_emit(){ record "$*"; printf '%s\n' "$*" >&2; }
redact_stream(){
  sed -E \
    -e '/PRIVATE KEY/Ic\
[REDACTED_PRIVATE_KEY_MATERIAL]' \
    -e 's#(postgres(ql)?|ssh)://[^[:space:]"]+#[REDACTED_URL]#Ig' \
    -e 's#Bearer[[:space:]]+[^[:space:]"]+#[REDACTED_BEARER]#Ig' \
    -e 's#/(secrets|git/keys)/[^[:space:]"]+#[REDACTED_PRIVATE_PATH]#Ig' \
    -e 's#((token|password|credential|secret|fence)[[:alnum:]_.-]*[[:space:]]*[:=][[:space:]]*)[^,}[:space:]"]+#\1[REDACTED]#Ig' \
    -e 's#((token|password|credential|secret|fence)[[:alnum:]_.-]*[[:space:]]+)[^,}[:space:]"]+#\1[REDACTED]#Ig'
}
safe_log_tail(){
  local label=$1 source=$2 raw="$dir/diagnostic-$1.raw" safe="$dir/diagnostic-$1.safe"
  if [[ ! -f "$source" ]] || ! tail -n 80 "$source" 2>/dev/null | tail -c 16384 >"$raw"; then
    diag_emit "diagnostic_${label}=unavailable"
    return
  fi
  if grep -Eiq 'PRIVATE[[:space:]]+KEY|^[A-Za-z0-9+/]{40,}={0,2}$|[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\.' "$raw"; then
    diag_emit "diagnostic_${label}=unavailable"
    return
  fi
  if ! redact_stream <"$raw" | tail -c 16384 >"$safe"; then
    diag_emit "diagnostic_${label}=unavailable"
    return
  fi
  if grep -Eiq '(postgres(ql)?|ssh)://|Bearer[[:space:]]+[^[]|PRIVATE[[:space:]]+KEY|/(secrets|git/keys)/|((token|password|credential|secret|fence)[[:alnum:]_.-]*[[:space:]]*([:=][[:space:]]*|[[:space:]]+))[^[]' "$safe"; then
    diag_emit "diagnostic_${label}=unavailable"
    return
  fi
  diag_emit "diagnostic_${label}_begin=true"
  while IFS= read -r line || [[ -n "$line" ]]; do diag_emit "$line"; done <"$safe"
  diag_emit "diagnostic_${label}_end=true"
}
diagnose(){
  local output rc runner_id state_file="$dir/diagnostic-runner-state.json"
  set +e
  diag_emit 'diagnostic_begin=true'
  output=$(compose ps --all --format json 2>"$dir/diagnostic-compose-status.err")
  rc=$?
  if [[ $rc -eq 0 ]]; then
    output=$(jq -cs '[.[] | if type == "array" then .[] else . end | {Service,Name,State,Health,ExitCode}] | sort_by(.Service,.Name)' <<<"$output" 2>/dev/null)
    rc=$?
  fi
  if [[ $rc -eq 0 ]] && output=$(printf '%s' "$output" | redact_stream); then
    diag_emit "compose_status=$output"
  else
    diag_emit 'compose_status=unavailable'
  fi
  runner_id=$(compose ps --all --quiet runner 2>"$dir/diagnostic-runner-id.err")
  rc=$?
  if [[ $rc -eq 0 && "$runner_id" =~ ^[0-9a-f]{12,64}$ ]] && docker container inspect --format '{{json .State}}' "$runner_id" >"$state_file" 2>/dev/null; then
    output=$(jq -c '{Status,Running,Paused,Restarting,OOMKilled,Dead,Pid,ExitCode,Error,StartedAt,FinishedAt,Health:(if .Health then {Status:.Health.Status,FailingStreak:.Health.FailingStreak} else null end)}' "$state_file" 2>/dev/null)
    if [[ $? -eq 0 ]] && output=$(printf '%s' "$output" | redact_stream); then diag_emit "runner_state=$output"; else diag_emit 'runner_state=unavailable'; fi
  elif [[ $rc -eq 0 && -z "$runner_id" ]]; then
    diag_emit 'runner_state=absent'
  else
    diag_emit 'runner_state=unavailable'
  fi
  safe_log_tail resolver_build "$dir/resolver-build.log"
  safe_log_tail runner_ready "$dir/runner-ready.log"
  if compose logs --no-color --tail 80 proxy >"$dir/diagnostic-proxy.log" 2>/dev/null; then safe_log_tail proxy "$dir/diagnostic-proxy.log"; else diag_emit 'diagnostic_proxy=unavailable'; fi
  if compose logs --no-color --tail 80 runner >"$dir/diagnostic-runner.log" 2>/dev/null; then safe_log_tail runner "$dir/diagnostic-runner.log"; else diag_emit 'diagnostic_runner=unavailable'; fi
  diag_emit 'diagnostic_end=true'
}
cleanup(){
  local original=$? final cleanup_complete=true output rc candidate error_file="$dir/cleanup-query.err"
  trap - ERR
  set +e
  final=$original
  if [[ "$pass" != true ]]; then diagnose; fi
  compose down --volumes --remove-orphans --rmi local --timeout 4 >/dev/null 2>"$dir/cleanup-down.err"
  if [[ $? -ne 0 ]]; then cleanup_complete=false; diag_emit 'cleanup_compose_down=false'; fi
  for candidate in "$image" "$resolver" "$gitimg"; do
    docker image inspect "$candidate" >/dev/null 2>"$error_file"
    rc=$?
    if [[ $rc -eq 0 ]]; then
      docker image rm "$candidate" >/dev/null 2>"$dir/cleanup-image-rm.err"
      if [[ $? -ne 0 ]]; then cleanup_complete=false; diag_emit 'cleanup_exact_image_remove=false'; fi
    elif ! grep -Eiq 'No such (image|object)' "$error_file"; then
      cleanup_complete=false; diag_emit 'cleanup_exact_image_query=false'
    fi
    docker image inspect "$candidate" >/dev/null 2>"$error_file"
    rc=$?
    if [[ $rc -eq 0 ]] || ! grep -Eiq 'No such (image|object)' "$error_file"; then cleanup_complete=false; diag_emit 'cleanup_exact_image_residual_or_query_failure=true'; fi
  done
  for candidate in container volume network; do
    case "$candidate" in
      container) output=$(docker ps --all --quiet --filter "label=com.docker.compose.project=$project" 2>"$error_file"); rc=$? ;;
      volume) output=$(docker volume ls --quiet --filter "label=com.docker.compose.project=$project" 2>"$error_file"); rc=$? ;;
      network) output=$(docker network ls --quiet --filter "label=com.docker.compose.project=$project" 2>"$error_file"); rc=$? ;;
    esac
    if [[ $rc -ne 0 || -n "$output" ]]; then cleanup_complete=false; diag_emit "cleanup_${candidate}_query_or_residual=true"; fi
  done
  if [[ ! "$dir" =~ ^/tmp/nerocd-runtime-provenance\.[A-Za-z0-9]{8}$ || ! -d "$dir" || -L "$dir" ]]; then
    cleanup_complete=false
    diag_emit 'cleanup_temp_guard=false'
  elif ! rm -rf -- "$dir"; then
    cleanup_complete=false
    diag_emit 'cleanup_temp_remove=false'
  fi
  record "cleanup_complete=$cleanup_complete"
  if [[ "$cleanup_complete" != true && $original -eq 0 ]]; then final=1; fi
  if [[ "$pass" == true && $original -eq 0 && "$cleanup_complete" == true ]]; then record 'PASS: durable provenance replay with real SSH resolver'; fi
  printf 'runtime provenance evidence: %s\n' "$evidence"
  exit "$final"
}
trap cleanup EXIT; trap 'fail "unexpected command failure line $LINENO"' ERR
for x in docker curl jq od; do command -v "$x" >/dev/null || fail "missing $x"; done; docker info >/dev/null || fail 'Docker unavailable'
record "source_commit=$(git -C "$root" rev-parse HEAD)"; record "project=$project"; record "docker=$(docker version --format '{{.Server.Version}}')"; record "compose=$(docker compose version --short)"
docker buildx version >/dev/null 2>&1 || fail 'Docker Buildx unavailable'
engine_builder=''
current_context=$(docker context show 2>/dev/null) || fail 'Docker context unavailable'
for candidate in default "$current_context"; do
  [[ -n "$candidate" ]] || continue
  if docker buildx inspect --bootstrap "$candidate" >"$dir/buildx.inspect" 2>"$dir/buildx.err" \
    && [[ $(awk '$1 == "Driver:" {print $2}' "$dir/buildx.inspect") == docker ]] \
    && [[ $(awk '$1 == "Status:" {print $2}' "$dir/buildx.inspect" | sort -u) == running ]]; then
    engine_builder=$candidate
    break
  fi
done
[[ -n "$engine_builder" ]] || fail 'no running engine-backed Buildx builder'
record "resolver_builder=$engine_builder driver=docker load=true"
docker build --pull -t "$image" "$root" >"$dir/build.log" 2>&1 || fail 'fresh image build failed'
docker buildx build --builder "$engine_builder" --load \
  --file "$root/acceptance/runtime-provenance/Dockerfile" \
  --build-arg "BASE=$image" --tag "$resolver" \
  "$root/acceptance/runtime-provenance" >"$dir/resolver-build.log" 2>&1 || fail 'dedicated resolver image build failed'
[[ $(docker image inspect --format '{{.Config.User}}' "$resolver") == nerocd ]] || fail 'resolver image user changed'
docker run --rm --network none --entrypoint /bin/sh "$resolver" -ec 'test "$(id -u)" = 10001; for tool in nerocd git ssh ssh-keyscan docker; do command -v "$tool"; done; docker compose version' >"$dir/resolver-tools.log" 2>&1 || fail 'resolver image tools or uid unavailable'
docker build --pull -f "$root/acceptance/runtime-provenance/GitDockerfile" -t "$gitimg" "$root/acceptance/runtime-provenance" >"$dir/git-build.log" 2>&1 || fail 'pinned git fixture image build failed'
record "image=$(docker image inspect -f '{{.Id}}' "$image") resolver_image=$(docker image inspect -f '{{.Id}}' "$resolver") git_image=$(docker image inspect -f '{{.Id}}' "$gitimg")"
compose run --rm --no-deps git_init >"$dir/git-init.log" 2>&1 || fail 'git fixture init failed'
compose up -d --wait postgres server proxy git >"$dir/up.log" 2>&1 || fail 'stack failed'
server_port=$(compose port server 8080 | tail -1); server_port=${server_port##*:}; proxy_port=$(compose port proxy 8081 | tail -1); proxy_port=${proxy_port##*:}; base="http://127.0.0.1:$server_port"; control="http://127.0.0.1:$proxy_port/__control"; gitvol=$(docker volume ls -q --filter label=com.docker.compose.project=$project --filter label=com.docker.compose.volume=git-data | head -1); secretvol=$(docker volume ls -q --filter label=com.docker.compose.project=$project --filter label=com.docker.compose.volume=runner-secrets | head -1); if ! docker run --rm --network "${project}_runtime" -v "$gitvol:/git:ro" -v "$secretvol:/secrets:ro" "$gitimg" sh -ec 'command -v ssh ssh-keyscan sshd git; sshd -t -f /git/sshd_config; sshd -T -f /git/sshd_config | grep -qx "passwordauthentication no"; ssh-keyscan -T 5 -p 2222 git >/tmp/known; GIT_SSH_COMMAND="ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/tmp/known -i /secrets/root/cred_git_deploy -p 2222" git ls-remote ssh://git@git:2222/repo.git >/dev/null' >"$dir/ssh-preflight.log" 2>&1; then record 'ssh_preflight_error=unavailable'; fail 'key-only SSH preflight failed'; fi; ! docker run --rm --network "${project}_runtime" -v "$gitvol:/git:ro" "$gitimg" sh -ec 'ssh-keyscan -T 5 -p 2222 git >/tmp/known; ssh -o BatchMode=yes -o PreferredAuthentications=password -o PasswordAuthentication=yes -o KbdInteractiveAuthentication=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/tmp/known -p 2222 git@git true' >/dev/null 2>&1 || fail 'SSH accepted missing/password credential'; record 'ssh_preflight=publickey_only'
http(){ local method=$1 url=$2 token=$3 body=$4 out=$5; local -a args=(-sS --max-time 10 -o "$out" -w '%{http_code}' -X "$method" -H 'Content-Type: application/json'); [[ -n "$token" ]] && args+=(-H "Authorization: Bearer $token"); args+=(--data "$body" "$url"); curl "${args[@]}"; }
control_call(){
  local method=$1 path=$2 output rc attempt
  for ((attempt=1; attempt<=40; attempt++)); do
    if output=$(curl -fsS --connect-timeout 1 --max-time 3 -X "$method" "$control/$path"); then
      printf '%s' "$output"
      return 0
    else
      rc=$?
    fi
    [[ $rc -eq 7 ]] || return "$rc"
    [[ $attempt -lt 40 ]] || return 7
    sleep .25
  done
}
code=$(http POST "$base/api/v1/sessions" '' '{"email":"admin@example.local","password":"admin"}' "$dir/session.json"); [[ $code == 201 ]] || fail 'admin session'; admin=$(jq -er .token "$dir/session.json")
code=$(http POST "$base/api/v1/runners/register" "$admin" '{"id":"runner_provenance","name":"runner_provenance","tags":["provenance-runtime"],"capabilities":["compose-deploy"]}' "$dir/runner.json"); [[ $code == 201 ]] || fail 'runner registration'; token=$(jq -er .token "$dir/runner.json")
printf '%s\n' "$token" | compose run --rm --no-deps credential_init >"$dir/credential.log" 2>&1 || fail 'credential init'
git_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$(compose ps -q git)"); gitvol=$(docker volume ls -q --filter label=com.docker.compose.project=$project --filter label=com.docker.compose.volume=git-data | head -1); fp=$(docker run --rm --network none -v "$gitvol:/git:ro" "$gitimg" sh -c 'ssh-keygen -lf /git/keys/host.pub' | awk '{print $2}')
repo_body=$(jq -cn '{project_id:"proj_platform",name:"runtime-provenance",url:"ssh://git@git:2222/git/repo.git",provider:"git",default_ref:"main"}'); code=$(http POST "$base/api/v1/repositories" "$admin" "$repo_body" "$dir/repo.json"); [[ $code == 201 ]] || fail 'repository create'; repo=$(jq -er .id "$dir/repo.json")
policy=$(jq -cn --arg host git --arg cidr "$git_ip/32" --arg fp "$fp" '{project_id:"proj_platform",configuration_id:"cfg_provenance_a",policy:{version:1,state:"configured",mode:"internal",allowed_schemes:["ssh"],allowed_hosts:[$host],allowed_cidrs:[$cidr],ssh_host_fingerprints:[$fp],credential_reference_id:"cred_git_deploy",allow_internal:true}}'); code=$(http PUT "$base/api/v1/repositories/$repo/policy" "$admin" "$policy" "$dir/policy.json"); [[ $code == 200 ]] || { record "policy_status=$code"; fail 'repository policy configure'; }
service=$(jq -cn --arg repo "$repo" '{project_id:"proj_platform",name:"runtime-provenance",repository_id:$repo,compose_path:"compose.yaml"}'); code=$(http POST "$base/api/v1/services" "$admin" "$service" "$dir/service.json"); [[ $code == 201 ]] || { record "service_status=$code"; fail 'service create'; }; svc=$(jq -er .id "$dir/service.json")
env=$(jq -cn --arg svc "$svc" '{service_id:$svc,name:"runtime",runner_selector:["provenance-runtime"],compose_project:"must-not-exist",timeout_seconds:60,rollback_safe:true,secret_bindings:[{name:"git",provider:"runner_file",reference:"cred_git_deploy",target:"env:GIT_SSH_KEY",required:true,version:"v1",fingerprint:"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}'); code=$(http POST "$base/api/v1/environments" "$admin" "$env" "$dir/env.json"); [[ $code == 201 ]] || fail 'environment create'; env_id=$(jq -er .id "$dir/env.json"); compose up --detach --no-deps runner >"$dir/runner-ready.log" 2>&1 || fail 'runner start'; runner_cid=$(compose ps -q runner); [[ -n "$runner_cid" ]] || fail 'runner container missing'; sleep 2
# This is intentionally inside the running, non-root resolver image with the
# exact mounted runner workspace and secret volume. The earlier sidecar check
# proves fixture setup; this one proves the actual GIT_SSH_COMMAND shape and
# toolchain before a deployment is claimable.
if ! compose exec -T -e NEROCD_GIT_IP="$git_ip" runner sh -ec '
  set -eu
  for tool in git ssh ssh-keyscan docker; do command -v "$tool"; done
  git --version; ssh -V 2>&1; docker compose version
  work=/workspace/.nerocd-runtime-provenance-preflight
  rm -rf -- "$work"; mkdir -m 700 "$work"
  trap "rm -rf -- \"$work\"" EXIT
  ssh-keyscan -T 5 -p 2222 "$NEROCD_GIT_IP" >"$work/scan" 2>/dev/null
  awk "NF == 3 { \$1=\"git\"; print }" "$work/scan" >"$work/known_hosts"
  test -s "$work/known_hosts"; test -r /secrets/root/cred_git_deploy
  export GIT_SSH_COMMAND="ssh -F /dev/null -o BatchMode=yes -o PreferredAuthentications=publickey -o PubkeyAuthentication=yes -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o ChallengeResponseAuthentication=no -o IdentitiesOnly=yes -o IdentityAgent=none -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$work/known_hosts -o GlobalKnownHostsFile=/dev/null -o HostKeyAlias=git -o HostName=$NEROCD_GIT_IP -o Port=2222 -i /secrets/root/cred_git_deploy"
  base="-c core.hooksPath=/dev/null -c protocol.file.allow=never -c protocol.ext.allow=never -c credential.helper= -c http.followRedirects=false"
  env -i PATH="$PATH" HOME="$work" LANG=C LC_ALL=C GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0 GIT_ASKPASS=/bin/false GIT_ALLOW_PROTOCOL=https:http:ssh GIT_SSH_COMMAND="$GIT_SSH_COMMAND" git $base init --quiet "$work/checkout"
  cd "$work/checkout"
  env -i PATH="$PATH" HOME="$work" LANG=C LC_ALL=C GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0 GIT_ASKPASS=/bin/false GIT_ALLOW_PROTOCOL=https:http:ssh GIT_SSH_COMMAND="$GIT_SSH_COMMAND" git $base remote add origin ssh://git@git:2222/git/repo.git
  env -i PATH="$PATH" HOME="$work" LANG=C LC_ALL=C GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0 GIT_ASKPASS=/bin/false GIT_ALLOW_PROTOCOL=https:http:ssh GIT_SSH_COMMAND="$GIT_SSH_COMMAND" git $base fetch --no-tags --depth=1 origin main
  env -i PATH="$PATH" HOME="$work" LANG=C LC_ALL=C GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0 GIT_ASKPASS=/bin/false GIT_ALLOW_PROTOCOL=https:http:ssh GIT_SSH_COMMAND="$GIT_SSH_COMMAND" git $base checkout --detach --quiet FETCH_HEAD
  : > "$work/empty.env"
  env -i PATH="$PATH" HOME="$work" LANG=C LC_ALL=C GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0 GIT_ASKPASS=/bin/false GIT_ALLOW_PROTOCOL=ssh GIT_SSH_COMMAND="$GIT_SSH_COMMAND" docker compose --project-name must-not-exist --env-file "$work/empty.env" --file compose.yaml config --format json >/dev/null
' >"$dir/runner-preflight.log" 2>&1; then record 'runner_preflight_error=unavailable'; fail 'actual runner resolver preflight failed'; fi
record 'runner_preflight=actual_uid_tools_ssh_fetch_compose_config'
docker pause "$runner_cid" >/dev/null || fail 'runner pause before deployment A'
resolve(){ local label=$1 target_env=${2:-$env_id}; code=$(http POST "$base/api/v1/revisions" "$admin" "$(jq -cn --arg s "$svc" '{service_id:$s,requested_ref:"main"}')" "$dir/rev-$label.json"); [[ $code == 201 ]] || { record "revision_status=$code"; fail "revision $label"; }; rev=$(jq -er .id "$dir/rev-$label.json"); code=$(http POST "$base/api/v1/deployments" "$admin" "$(jq -cn --arg e "$target_env" --arg r "$rev" --arg k "runtime-$label" '{environment_id:$e,desired_revision_id:$r,idempotency_key:$k}')" "$dir/deploy-$label.json"); [[ $code == 201 ]] || { record "deployment_status=$code"; fail "deployment $label"; }; echo "$rev"; }
control_call POST provenance-hold/on >/dev/null; control_call POST drop-provenance >/dev/null; proxy_before=$(control_call GET status); rev_a=$(resolve a); docker unpause "$runner_cid" >/dev/null || fail 'runner unpause for deployment A'
jvol=$(docker volume ls -q --filter label=com.docker.compose.project=$project --filter label=com.docker.compose.volume=runner-journal | head -1); [[ -n "$jvol" ]] || fail 'journal volume missing'; deadline=$((SECONDS+45)); held=false; while ((SECONDS<deadline)); do state=$(control_call GET status); if [[ $(jq -r .held_provenance_requests <<<"$state") -ge 1 ]]; then held=true; break; fi; sleep .25; done; [[ "$held" == true ]] || fail 'runner did not journal and reach held provenance request'; journal_exists=false; deadline=$((SECONDS+10)); while ((SECONDS<deadline)); do if docker run --rm --network none -v "$jvol:/j:ro" "$gitimg" sh -ec 'test -f /j/journal.json'; then journal_exists=true; break; fi; sleep .25; done; [[ "$journal_exists" == true ]] || fail 'journal.json was not created before provenance send'; docker run --rm --network none -v "$jvol:/j:ro" "$gitimg" cat /j/journal.json >"$dir/journal-pending.json"; jq -e '(.provenance|length)==1' "$dir/journal-pending.json" >/dev/null || fail 'journal did not contain exactly one pending provenance record'; control_call POST provenance-hold/off >/dev/null
compose stop runner >/dev/null; before=$(sha256sum "$dir/journal-pending.json" | awk '{print $1}'); compose start runner >/dev/null; deadline=$((SECONDS+60)); replayed=false; state=''; while ((SECONDS<deadline)); do state=$(control_call GET status); pending_count=$(docker run --rm --network none -v "$jvol:/j:ro" "$gitimg" sh -ec 'test -f /j/journal.json && grep -c "\"id\"" /j/journal.json || true'); if [[ $(jq -r .lost_provenance_responses <<<"$state") == 1 && $(jq -r '.requests["/api/v1/runners/deployments/provenance"] // 0' <<<"$state") -ge 2 && "$pending_count" == 0 ]]; then replayed=true; break; fi; sleep .5; done; [[ "$replayed" == true ]] || fail 'lost provenance response did not replay and drain before deadline'; trace=$(jq -r '.trace[]' <<<"$state"); lost_line=$(grep -n 'committed-response-lost:provenance' <<<"$trace" | head -1 | cut -d: -f1); replay_line=$(grep -n 'response:/api/v1/runners/deployments/provenance:200' <<<"$trace" | tail -1 | cut -d: -f1); [[ -n "$lost_line" && -n "$replay_line" && "$replay_line" -gt "$lost_line" ]] || fail 'proxy trace lacks ordered second provenance replay'; between_lost_and_replay=$(sed -n "$((lost_line+1)),$((replay_line-1))p" <<<"$trace"); ! grep -Eq 'response:/api/v1/runners/(heartbeat|claim):' <<<"$between_lost_and_replay" || fail 'heartbeat or claim occurred before replay reconciliation'; record "replay_posts=$(jq -r '.requests["/api/v1/runners/deployments/provenance"]' <<<"$state") ordered_before_heartbeat_claim=true journal_pending=0"
row=$(compose exec -T postgres psql -U nerocd -d nerocd -Atc "select git_commit,compose_hash,array_to_string(image_digests,','),content_identity,provenance_state from revisions where id='$rev_a'"); origin_a=$(docker run --rm --network none -v "$gitvol:/git:ro" "$gitimg" sh -ec 'git --git-dir=/git/repo.git rev-parse refs/heads/main'); [[ "$origin_a" =~ ^[0-9a-f]{40,64}$ ]] || fail 'origin A is not a full commit'; IFS='|' read -r a_commit a_hash a_digests a_identity a_state <<<"$row"; [[ "$a_commit" == "$origin_a" && "$a_hash" =~ ^sha256:[0-9a-f]{64}$ && "$a_digests" =~ ^[a-z0-9][a-z0-9._/:@-]*@sha256:[0-9a-f]{64}(,[a-z0-9][a-z0-9._/:@-]*@sha256:[0-9a-f]{64})*$ && "$a_identity" == "$a_commit:$a_hash" && "$a_state" == resolved ]] || fail 'A provenance was not canonical exact evidence'; a_receipts=$(compose exec -T postgres psql -U nerocd -d nerocd -Atc "select count(*) from provenance_resolutions where revision_id='$rev_a'"); a_audits=$(compose exec -T postgres psql -U nerocd -d nerocd -Atc "select count(*) from audit_events where target_id=(select id from deployments where desired_revision_id='$rev_a') and action='runner.deployment.provenance.resolve'"); [[ "$a_receipts" == 1 && "$a_audits" == 1 ]] || fail 'A replay did not converge to one receipt/audit'; record "A=$row receipt=$a_receipts audit=$a_audits journal_before=$before"
origin_b=$(compose run --rm --no-deps git_advance | tail -1); [[ "$origin_b" =~ ^[0-9a-f]{40,64}$ && "$origin_a" != "$origin_b" ]] || fail 'fixture A/B refs are not distinct full commits'; record "fixture_A=$origin_a fixture_B=$origin_b"
env_b=$(jq -cn --arg svc "$svc" '{service_id:$svc,name:"runtime-b",runner_selector:["provenance-runtime"],compose_project:"must-not-exist-b",timeout_seconds:60,rollback_safe:true,secret_bindings:[{name:"git",provider:"runner_file",reference:"cred_git_deploy",target:"env:GIT_SSH_KEY",required:true,version:"v1",fingerprint:"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}'); code=$(http POST "$base/api/v1/environments" "$admin" "$env_b" "$dir/env-b.json"); [[ $code == 201 ]] || fail 'environment B create'; env_b_id=$(jq -er .id "$dir/env-b.json")
rev_b=$(resolve b "$env_b_id"); deadline=$((SECONDS+60)); while ((SECONDS<deadline)); do b=$(compose exec -T postgres psql -U nerocd -d nerocd -Atc "select provenance_state from revisions where id='$rev_b'"); [[ $b == resolved ]] && break || true; sleep .5; done; [[ $b == resolved ]] || fail 'B did not resolve'; b_row=$(compose exec -T postgres psql -U nerocd -d nerocd -Atc "select git_commit,compose_hash,array_to_string(image_digests,','),content_identity,provenance_state from revisions where id='$rev_b'"); IFS='|' read -r b_commit b_hash b_digests b_identity b_state <<<"$b_row"; [[ "$b_commit" == "$origin_b" && "$b_commit" != "$a_commit" && "$b_hash" =~ ^sha256:[0-9a-f]{64}$ && "$b_digests" =~ ^[a-z0-9][a-z0-9._/:@-]*@sha256:[0-9a-f]{64}(,[a-z0-9][a-z0-9._/:@-]*@sha256:[0-9a-f]{64})*$ && "$b_identity" == "$b_commit:$b_hash" && "$b_state" == resolved ]] || fail 'B provenance was not canonical exact evidence'; a_after=$(compose exec -T postgres psql -U nerocd -d nerocd -Atc "select git_commit,compose_hash,array_to_string(image_digests,','),content_identity,provenance_state from revisions where id='$rev_a'"); [[ "$a_after" == "$row" ]] || fail 'A provenance changed after main advanced'; b_receipts=$(compose exec -T postgres psql -U nerocd -d nerocd -Atc "select count(*) from provenance_resolutions where revision_id='$rev_b'"); b_audits=$(compose exec -T postgres psql -U nerocd -d nerocd -Atc "select count(*) from audit_events where target_id=(select id from deployments where desired_revision_id='$rev_b') and action='runner.deployment.provenance.resolve'"); [[ "$b_receipts" == 1 && "$b_audits" == 1 ]] || fail 'B did not converge to one receipt/audit'; record "A_immutable=$a_after B=$b_row receipt=$b_receipts audit=$b_audits"
compose logs --no-color >"$dir/logs.txt" || true; ! grep -Eiq 'BEGIN (OPENSSH|RSA|EC|DSA) PRIVATE KEY|Bearer [A-Za-z0-9._-]+|runner_provenance.*token' "$dir/logs.txt" "$dir/journal-pending.json" || fail 'secret disclosure scan'
record 'target_compose_mutation=none; compose up/down is intentionally outside resolver'; pass=true
