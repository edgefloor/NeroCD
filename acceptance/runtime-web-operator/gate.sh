#!/usr/bin/env bash
# Real browser operator acceptance: the browser creates B and broken C through
# the shipped UI; HTTP/psql below are fixture setup and independent evidence.
set -Eeuo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
source "$root/scripts/local-image-registry.sh"
evidence=/tmp/nerocd-runtime-web-operator.txt
dir=$(mktemp -d /tmp/nerocd-runtime-web-operator.XXXXXXXX)
suffix=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')
project="nerocd-web-operator-$suffix" image="nerocd-web-operator:$suffix" runner_image="nerocd-web-runner:$suffix" git_image="nerocd-web-git:$suffix"
compose_project="target_web_$suffix" health_network="${compose_project}_health" health_host="runtime-health-$suffix" health_url="http://${health_host}:8080/cgi-bin/health"
fixture_a="nerocd-web-fixture-a:$suffix" fixture_b="nerocd-web-fixture-b:$suffix" fixture_c="nerocd-web-fixture-c:$suffix" fixture_a_repo='' fixture_b_repo='' fixture_c_repo='' fixture_a_ref='' fixture_b_ref='' fixture_c_ref='' fixture_a_registry_tag='' fixture_b_registry_tag='' fixture_c_registry_tag='' fixture_registry_container_id='' fixture_registry_port='' pass=false
cleanup_helper_image='alpine:3.22.2@sha256:4b7ce07002c69e8f3d704a9c5d6fd3053be500b7f1c69fc0d80990c2ad8dd412'
runner_workdir="$dir/runner-workspace"; runner_secret_root="$dir/runner-secrets"; mkdir -m 0700 "$runner_workdir" "$runner_secret_root"; export NEROCD_RUNTIME_WORKDIR="$runner_workdir" NEROCD_RUNTIME_SECRET_ROOT="$runner_secret_root"
: >"$evidence"; record(){ printf '%s\n' "$*" >>"$evidence"; }; fail(){ trap - ERR; record "FAIL: $*"; printf 'runtime-web-operator: %s\n' "$*" >&2; exit 1; }
docker_gid(){ docker run --rm -v /var/run/docker.sock:/var/run/docker.sock alpine:3.22.2@sha256:4b7ce07002c69e8f3d704a9c5d6fd3053be500b7f1c69fc0d80990c2ad8dd412 sh -ec 'stat -c %g /var/run/docker.sock'; }
compose(){ NEROCD_RUNTIME_IMAGE="$image" NEROCD_RUNNER_IMAGE="$runner_image" NEROCD_GIT_IMAGE="$git_image" NEROCD_FIXTURE_A="$fixture_a_ref" NEROCD_FIXTURE_B="$fixture_b_ref" NEROCD_FIXTURE_C="$fixture_c_ref" NEROCD_DOCKER_GID="$socket_gid" NEROCD_HEALTH_NETWORK="$health_network" NEROCD_HEALTH_HOST="$health_host" docker compose -p "$project" -f "$root/acceptance/runtime-compose/compose.yaml" -f "$root/acceptance/runtime-web-operator/compose.override.yaml" "$@"; }
diagnose(){
  local service state health exit_code
  set +e
  for service in postgres server runner git proxy; do
    state=$(docker inspect -f '{{.State.Status}}' "$(compose ps -q "$service")" 2>/dev/null)
    health=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$(compose ps -q "$service")" 2>/dev/null)
    exit_code=$(docker inspect -f '{{.State.ExitCode}}' "$(compose ps -q "$service")" 2>/dev/null)
    case "$state:$health:$exit_code" in
      running:healthy:0|running:none:0|created:none:0|exited:none:[0-9]*|exited:healthy:[0-9]*|exited:unhealthy:[0-9]*|running:unhealthy:0)
        record "diagnostic_${service}=state_${state}_health_${health}_exit_${exit_code}" ;;
      *) record "diagnostic_${service}=unavailable" ;;
    esac
  done
}
remove_project_resources(){
  local exact_project=$1 kind output rc resource cleanup_ok=true error_file="$dir/cleanup-query.err"
  [[ "$exact_project" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$ ]] || return 1
  for kind in container volume network; do
    case "$kind" in
      container) output=$(docker ps -aq --filter "label=com.docker.compose.project=$exact_project" 2>"$error_file"); rc=$? ;;
      volume) output=$(docker volume ls -q --filter "label=com.docker.compose.project=$exact_project" 2>"$error_file"); rc=$? ;;
      network) output=$(docker network ls -q --filter "label=com.docker.compose.project=$exact_project" 2>"$error_file"); rc=$? ;;
    esac
    [[ $rc -eq 0 ]] || { record "cleanup_${kind}_query=false"; cleanup_ok=false; continue; }
    while IFS= read -r resource; do
      [[ -z "$resource" ]] && continue
      case "$kind" in
        container|network) [[ "$resource" =~ ^[0-9a-f]{12,64}$ ]] ;;
        volume) [[ "$resource" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,255}$ ]] ;;
      esac || { record "cleanup_${kind}_identifier_safe=false"; cleanup_ok=false; continue; }
      case "$kind" in
        container) docker rm -f "$resource" >/dev/null 2>&1 ;;
        volume) docker volume rm -f "$resource" >/dev/null 2>&1 ;;
        network) docker network rm "$resource" >/dev/null 2>&1 ;;
      esac
      [[ $? -eq 0 ]] || { record "cleanup_${kind}_remove=false"; cleanup_ok=false; }
    done <<<"$output"
    case "$kind" in
      container) output=$(docker ps -aq --filter "label=com.docker.compose.project=$exact_project" 2>"$error_file"); rc=$? ;;
      volume) output=$(docker volume ls -q --filter "label=com.docker.compose.project=$exact_project" 2>"$error_file"); rc=$? ;;
      network) output=$(docker network ls -q --filter "label=com.docker.compose.project=$exact_project" 2>"$error_file"); rc=$? ;;
    esac
    [[ $rc -eq 0 && -z "$output" ]] || { record "cleanup_${kind}_query_or_residual=true"; cleanup_ok=false; }
  done
  [[ "$cleanup_ok" == true ]]
}
remove_exact_image(){
  local candidate=$1 exact_image output rc
  if [[ "$candidate" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]{0,255}:[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$ ]]; then
    exact_image=$candidate
  elif [[ "$candidate" =~ ^local\.invalid/[A-Za-z0-9][A-Za-z0-9._/-]{0,255}$ ]]; then
    exact_image="${candidate}:latest"
  else
    record 'cleanup_image_name_safe=false'
    return 1
  fi
  output=$(docker image ls --format '{{.Repository}}:{{.Tag}}' --filter "reference=$exact_image" 2>"$dir/cleanup-image-query.err")
  rc=$?
  [[ $rc -eq 0 ]] || { record 'cleanup_exact_image_query=false'; return 1; }
  [[ -z "$output" || "$output" == "$exact_image" ]] || { record 'cleanup_exact_image_query_ambiguous=true'; return 1; }
  if [[ "$output" == "$exact_image" ]] && ! docker image rm -f "$exact_image" >/dev/null 2>"$dir/cleanup-image-rm.err"; then
    record 'cleanup_exact_image_remove=false'
    return 1
  fi
  output=$(docker image ls --format '{{.Repository}}:{{.Tag}}' --filter "reference=$exact_image" 2>"$dir/cleanup-image-query.err")
  rc=$?
  [[ $rc -eq 0 && -z "$output" ]] || { record 'cleanup_exact_image_residual_or_query_failure=true'; return 1; }
}
start_fixture_registry(){
  local port_line ready=false attempt
  fixture_registry_container_id=$(docker run -d \
    --name "${project}-registry" \
    --label "nerocd.acceptance.runtime-web-operator=$suffix" \
    --cap-drop ALL --security-opt no-new-privileges --read-only \
    --tmpfs /var/lib/registry:rw,noexec,nosuid,nodev \
    -p 127.0.0.1::5000 "$LOCAL_REGISTRY_IMAGE" 2>"$dir/registry-start.err") || return 1
  [[ "$fixture_registry_container_id" =~ ^[a-f0-9]{64}$ ]] || return 1
  port_line=$(docker port "$fixture_registry_container_id" 5000/tcp 2>"$dir/registry-port.err") || return 1
  [[ "$port_line" =~ ^127\.0\.0\.1:([1-9][0-9]{0,4})$ ]] || return 1
  fixture_registry_port=${BASH_REMATCH[1]}
  ((10#$fixture_registry_port <= 65535)) || return 1
  for attempt in 1 2 3 4 5; do
    if curl --fail --silent --show-error --max-time 1 "http://127.0.0.1:${fixture_registry_port}/v2/" >/dev/null 2>"$dir/registry-ready.err"; then ready=true; break; fi
    sleep 1
  done
  [[ "$ready" == true ]]
}
repository_digest_is_member(){
  local image_ref=$1 repository=$2 candidate=$3 repo_digests
  [[ "$repository" =~ ^127\.0\.0\.1:[1-9][0-9]{0,4}/nerocd-web-fixture-[abc]-[a-f0-9]{12}$ ]] || return 2
  [[ "$candidate" == "$repository@sha256:"* && "${candidate#"$repository@sha256:"}" =~ ^[a-f0-9]{64}$ ]] || return 2
  repo_digests=$(docker image inspect --format '{{json .RepoDigests}}' "$image_ref" 2>"$dir/registry-repodigests.err") || return 2
  jq -e --arg candidate "$candidate" 'type == "array" and any(.[]; type == "string" and . == $candidate)' <<<"$repo_digests" >/dev/null 2>"$dir/registry-repodigests-jq.err"
}
publish_fixture_digest(){
  local source_tag=$1 label=$2 repository registry_tag source_id repo_digests resolved attempt
  [[ "$source_tag" =~ ^nerocd-web-fixture-[abc]:[a-f0-9]{12}$ && "$label" =~ ^[abc]$ ]] || return 1
  [[ "$fixture_registry_port" =~ ^[1-9][0-9]{0,4}$ ]] || return 1
  repository="127.0.0.1:${fixture_registry_port}/nerocd-web-fixture-${label}-${suffix}"
  registry_tag="${repository}:candidate"
  source_id=$(docker image inspect --format '{{.Id}}' "$source_tag" 2>"$dir/registry-${label}-source-inspect.err") || return 1
  [[ "$source_id" =~ ^sha256:[a-f0-9]{64}$ ]] || return 1
  docker tag "$source_tag" "$registry_tag" 2>"$dir/registry-${label}-tag.err" || return 1
  case "$label" in a) fixture_a_repo=$repository; fixture_a_registry_tag=$registry_tag ;; b) fixture_b_repo=$repository; fixture_b_registry_tag=$registry_tag ;; c) fixture_c_repo=$repository; fixture_c_registry_tag=$registry_tag ;; esac
  for attempt in 1 2 3 4 5; do
    if docker push "$registry_tag" >"$dir/registry-${label}-push.log" 2>&1; then break; fi
    [[ "$attempt" != 5 ]] || return 1
    sleep 1
  done
  repo_digests=$(docker image inspect --format '{{json .RepoDigests}}' "$registry_tag" 2>"$dir/registry-${label}-repodigests.err") || return 1
  resolved=$(jq -er --arg prefix "${repository}@sha256:" 'if type != "array" then error("invalid") else [.[] | select(type == "string" and startswith($prefix))] | unique | if length == 1 then .[0] else error("ambiguous") end end' <<<"$repo_digests" 2>"$dir/registry-${label}-repodigests-jq.err") || return 1
  [[ "$resolved" == "$repository@sha256:"* && "${resolved#"$repository@sha256:"}" =~ ^[a-f0-9]{64}$ ]] || return 1
  repository_digest_is_member "$registry_tag" "$repository" "$resolved" || return 1
  [[ "$resolved" != "$repository@$source_id" ]] || return 1
  docker image rm "$registry_tag" "$source_tag" >/dev/null 2>"$dir/registry-${label}-alias-remove.err" || return 1
  local_registry_remove_image "$resolved" || return 1
  if local_registry_image_state "$resolved"; then return 1; elif [[ "$local_registry_last_query_state" != absent ]]; then return 1; fi
  for attempt in 1 2 3 4 5; do
    if docker pull "$resolved" >"$dir/registry-${label}-pull.log" 2>&1; then break; fi
    [[ "$attempt" != 5 ]] || return 1
    sleep 1
  done
  docker image inspect "$resolved" >/dev/null 2>"$dir/registry-${label}-digest-inspect.err" || return 1
  [[ "$(docker image inspect --format '{{.Id}}' "$resolved" 2>"$dir/registry-${label}-resolved-inspect.err")" == "$source_id" ]] || return 1
  case "$label" in a) fixture_a_ref=$resolved ;; b) fixture_b_ref=$resolved ;; c) fixture_c_ref=$resolved ;; esac
}
browser_failure_class(){
  local result=$1 mode class
  [[ -f "$result" && ! -L "$result" && -O "$result" ]] || { printf '%s' unavailable; return 0; }
  if mode=$(stat -c '%a' "$result" 2>/dev/null); then :; else mode=$(stat -f '%Lp' "$result" 2>/dev/null) || { printf '%s' unavailable; return 0; }; fi
  [[ "$mode" == 600 ]] || { printf '%s' unavailable; return 0; }
  class=$(jq -er 'if type == "object" and (keys == ["failure_class"]) and (.failure_class | type == "string" and IN("navigation"; "csp"; "session"; "ui_action"; "deployment_creation"; "deployment_terminal_failure"; "deployment_terminal_timeout"; "unavailable")) then .failure_class else error("invalid") end' "$result" 2>/dev/null) || { printf '%s' unavailable; return 0; }
  printf '%s' "$class"
}
cleanup(){
  local original=$? final cleanup_complete=true target=${compose_project:-} host_uid host_gid
  trap - ERR
  set +e
  final=$original
  [[ "$pass" == true ]] || diagnose
  [[ -z "$target" ]] || remove_project_resources "$target" || cleanup_complete=false
  compose down --volumes --remove-orphans --rmi local --timeout 4 >/dev/null 2>&1 || { cleanup_complete=false; record 'cleanup_compose_down=false'; }
  remove_project_resources "$project" || cleanup_complete=false
  [[ -z "$target" ]] || remove_project_resources "$target" || cleanup_complete=false
  for image_ref in "$runner_image" "$image" "$git_image" "$fixture_a" "$fixture_b" "$fixture_c" "$fixture_a_registry_tag" "$fixture_b_registry_tag" "$fixture_c_registry_tag"; do
    [[ -z "$image_ref" ]] || remove_exact_image "$image_ref" || cleanup_complete=false
  done
  for image_ref in "$fixture_a_ref" "$fixture_b_ref" "$fixture_c_ref"; do
    [[ -z "$image_ref" ]] || local_registry_remove_image "$image_ref" || cleanup_complete=false
  done
  [[ -z "$fixture_registry_container_id" ]] || local_registry_remove_container "$fixture_registry_container_id" || cleanup_complete=false
  host_uid=$(id -u); host_gid=$(id -g)
  if [[ ! "$host_uid" =~ ^[0-9]+$ || ! "$host_gid" =~ ^[0-9]+$ ]]; then
    cleanup_complete=false; record 'cleanup_host_identity=false'
  elif [[ -d "$runner_secret_root" || -d "$runner_workdir" ]]; then
    docker run --rm --name "${project}-cleanup-ownership" --label "com.docker.compose.project=$project" --privileged --network none --user 0:0 -e "HOST_UID=$host_uid" -e "HOST_GID=$host_gid" -v "$dir:/gate-run" "$cleanup_helper_image" sh -ec '
      test -d /gate-run
      for path in /gate-run/runner-secrets /gate-run/runner-workspace; do
        if test -e "$path"; then chown -R "$HOST_UID:$HOST_GID" "$path"; fi
      done
    ' >/dev/null 2>&1 || { cleanup_complete=false; record 'cleanup_ownership_remediation=false'; }
    [[ ! -d "$runner_secret_root" || ( -O "$runner_secret_root" && -r "$runner_secret_root" && -x "$runner_secret_root" ) ]] || { cleanup_complete=false; record 'cleanup_secret_root_host_access=false'; }
    [[ ! -d "$runner_workdir" || ( -O "$runner_workdir" && -r "$runner_workdir" && -x "$runner_workdir" ) ]] || { cleanup_complete=false; record 'cleanup_workdir_host_access=false'; }
  fi
  remove_project_resources "$project" || cleanup_complete=false
  if [[ ! "$dir" =~ ^/tmp/nerocd-runtime-web-operator\.[A-Za-z0-9]{8}$ || ! -d "$dir" || -L "$dir" ]]; then
    cleanup_complete=false; record 'cleanup_temp_guard=false'
  elif ! rm -rf -- "$dir"; then
    cleanup_complete=false; record 'cleanup_temp_remove=false'
  fi
  [[ ! -e "$dir" ]] || { cleanup_complete=false; record 'cleanup_temp_residual=true'; }
  record "cleanup_complete=$cleanup_complete"
  [[ "$cleanup_complete" == true || $original -ne 0 ]] || final=1
  [[ "$pass" != true || $original -ne 0 || "$cleanup_complete" != true ]] || record 'PASS: real browser deployment operator B/C rollback gate'
  printf 'runtime web operator evidence: %s\n' "$evidence"
  exit "$final"
}
trap cleanup EXIT; trap 'fail "unexpected command failure line $LINENO"' ERR
for x in docker curl jq git bun od shasum; do command -v "$x" >/dev/null || fail "missing $x"; done
docker info >/dev/null || fail 'Docker unavailable'; [[ -S /var/run/docker.sock ]] || fail 'Docker socket unavailable'
socket_gid=$(docker_gid); [[ "$socket_gid" =~ ^[0-9]+$ ]] || fail 'Docker socket GID unavailable'
record "gate=real-browser-operator project_isolated=true socket_root_equivalent=true"; record "docker=$(docker version --format '{{.Server.Version}}') bun=$(bun --version) browser=$(cd "$root/web/app" && bunx playwright --version)"
docker build --pull -t "$image" "$root" >"$dir/server-build.log" 2>&1 || fail 'server/frontend image build failed'
docker build --pull -f "$root/acceptance/runtime-compose/GitDockerfile" -t "$git_image" "$root/acceptance/runtime-compose" >"$dir/git-build.log" 2>&1 || fail 'git fixture image build failed'
docker build --pull -f "$root/acceptance/runtime-compose/FixtureDockerfile" --build-arg VERSION=A --build-arg MODE=good --build-arg BUILD_NONCE="$suffix" -t "$fixture_a" "$root/acceptance/runtime-compose" >"$dir/a-build.log" 2>&1 || fail 'fixture A build failed'
docker build --pull -f "$root/acceptance/runtime-compose/FixtureDockerfile" --build-arg VERSION=B --build-arg MODE=good --build-arg BUILD_NONCE="$suffix" -t "$fixture_b" "$root/acceptance/runtime-compose" >"$dir/b-build.log" 2>&1 || fail 'fixture B build failed'
docker build --pull -f "$root/acceptance/runtime-compose/FixtureDockerfile" --build-arg VERSION=C --build-arg MODE=slow --build-arg BUILD_NONCE="$suffix" -t "$fixture_c" "$root/acceptance/runtime-compose" >"$dir/c-build.log" 2>&1 || fail 'fixture C build failed'
start_fixture_registry || fail 'fixture registry setup failed'
publish_fixture_digest "$fixture_a" a || fail 'fixture A registry digest publication failed'
publish_fixture_digest "$fixture_b" b || fail 'fixture B registry digest publication failed'
publish_fixture_digest "$fixture_c" c || fail 'fixture C registry digest publication failed'
[[ "$fixture_a_ref" =~ ^127\.0\.0\.1:[1-9][0-9]{0,4}/nerocd-web-fixture-a-[a-f0-9]{12}@sha256:[a-f0-9]{64}$ && "$fixture_b_ref" =~ ^127\.0\.0\.1:[1-9][0-9]{0,4}/nerocd-web-fixture-b-[a-f0-9]{12}@sha256:[a-f0-9]{64}$ && "$fixture_c_ref" =~ ^127\.0\.0\.1:[1-9][0-9]{0,4}/nerocd-web-fixture-c-[a-f0-9]{12}@sha256:[a-f0-9]{64}$ ]] || fail 'fixture references are not exact repository digests'
record 'fixture_registry=true fixture_manifest_digests=true fixture_config_ids_rejected=true'
compose config -q >"$dir/compose-config.log" 2>&1 || fail 'shared compose config preflight failed'
docker build --build-arg BASE="$image" --build-arg DOCKER_GID="$socket_gid" -f "$root/acceptance/runtime-compose/RunnerDockerfile" -t "$runner_image" "$root/acceptance/runtime-compose" >"$dir/runner-build.log" 2>&1 || fail 'runner image build failed'
[[ "$(docker image inspect -f '{{.Config.User}}' "$runner_image" 2>/dev/null)" == nerocd ]] || fail 'runner image default user is not nerocd'
[[ "$(docker run --rm --network none --entrypoint id "$runner_image" -u 2>/dev/null)" == 10001 ]] || fail 'runner image default UID is not 10001'
docker run --rm --network none --entrypoint sh "$runner_image" -ec 'command -v nerocd >/dev/null && command -v docker >/dev/null && command -v git >/dev/null && command -v ssh >/dev/null && command -v wget >/dev/null && docker compose version >/dev/null' >"$dir/runner-tools.log" 2>&1 || fail 'runner image required tools unavailable'
record 'runner_image_built=true runner_image_default_user=nerocd runner_image_uid_10001=true runner_image_tools=true'
compose run --rm --no-deps git_init >"$dir/git-init.log" 2>&1 || fail 'git fixture initialization failed'
compose up -d --wait postgres >"$dir/postgres.log" 2>&1 || fail 'postgres startup failed'
compose run --rm --no-deps --entrypoint nerocd server migrate >"$dir/migrate.log" 2>&1 || fail 'migration failed'
umask 077; email="operator-${suffix}@example.invalid"; password=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n'); printf '%s\n%s\n' "$email" "$password" >"$dir/operator.credentials"; chmod 0600 "$dir/operator.credentials"
head -n 1 "$dir/operator.credentials" >/dev/null
tail -n 1 "$dir/operator.credentials" | compose run --rm --no-deps --entrypoint nerocd server bootstrap-admin --email "$email" --name 'Browser Operator' --password-stdin >"$dir/bootstrap.log" 2>&1 || fail 'random browser bootstrap failed'
unset password
compose up -d --wait postgres server git proxy >"$dir/up.log" 2>&1 || fail 'control stack failed'
server_port=$(compose port server 8080 | tail -1); base="http://127.0.0.1:${server_port##*:}"
http(){ local method=$1 path=$2 body=$3 out=$4; curl -sS --max-time 20 -o "$out" -w '%{http_code}' -X "$method" -H 'Content-Type: application/json' -H "@$dir/auth.header" --data "$body" "$base$path"; }
jq -cn --arg e "$email" --arg p "$(tail -n 1 "$dir/operator.credentials")" '{email:$e,password:$p}' >"$dir/login.json"; chmod 0600 "$dir/login.json"
code=$(curl -sS --max-time 15 -o "$dir/session.json" -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @"$dir/login.json" "$base/api/v1/sessions"); [[ "$code" == 201 ]] || fail 'browser operator session setup failed'; admin=$(jq -er .token "$dir/session.json"); printf 'Authorization: Bearer %s\n' "$admin" >"$dir/auth.header"; chmod 0600 "$dir/auth.header"; unset admin
code=$(http POST /api/v1/runners/register '{"id":"runner_web","name":"runner_web","tags":["compose-runtime"],"capabilities":["compose-deploy"]}' "$dir/runner.json"); [[ "$code" == 201 ]] || fail 'runner registration failed'; jq -er .token "$dir/runner.json" | compose run --rm --no-deps credential_init >"$dir/credential.log" 2>&1 || fail 'runner credential init failed'
compose up -d runner >"$dir/runner-up.log" 2>&1 || fail 'runner start failed'
runner_container=''
runner_deadline=$((SECONDS + 30))
while (( SECONDS < runner_deadline )); do
  runner_container=$(compose ps -q runner 2>/dev/null || true)
  if [[ "$runner_container" =~ ^[0-9a-f]{12,64}$ && "$(docker inspect -f '{{.State.Running}}' "$runner_container" 2>/dev/null || true)" == true ]]; then
    break
  fi
  runner_container=''
  sleep 1
done
[[ -n "$runner_container" ]] || fail 'runner container did not reach running state'
[[ "$(docker exec "$runner_container" id -u 2>/dev/null)" == 10001 ]] || fail 'runner container is not UID 10001'
record 'runner_container_running=true runner_container_uid_10001=true'
health_network_json=$(docker network inspect "$health_network") || fail 'health network inspection failed'; health_cidr=$(jq -er '.[0] | select(.Internal == true) | [.IPAM.Config[]?.Subnet | select(type == "string" and test("^[0-9]+(\\.[0-9]+){3}/[0-9]+$"))] | if length == 1 then .[0] else error("expected one IPv4 subnet") end' <<<"$health_network_json") || fail 'health network is not one internal IPv4 subnet'; fixture_health_body(){ compose exec -T runner sh -ec 'exec wget -q -T 5 -O - "$1"' sh "$health_url"; }
git_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$(compose ps -q git)")
fp=$(docker run --rm --network none -v "$(docker volume ls -q --filter label=com.docker.compose.project=$project --filter label=com.docker.compose.volume=git-data | head -1):/git:ro" "$git_image" sh -c 'ssh-keygen -lf /git/keys/host.pub' | awk '{print $2}')
project_payload=$(jq -cn --arg n "web-operator-$suffix" '{name:$n,description:"ephemeral browser acceptance"}'); code=$(http POST /api/v1/projects "$project_payload" "$dir/project.json"); [[ "$code" == 201 ]] || fail 'project fixture setup failed'; project_id=$(jq -er .id "$dir/project.json")
repo=$(jq -cn --arg p "$project_id" '{project_id:$p,name:"browser-fixture",url:"ssh://git@git:2222/repo.git",provider:"git",default_ref:"main"}'); code=$(http POST /api/v1/repositories "$repo" "$dir/repo.json"); [[ "$code" == 201 ]] || fail 'repo fixture setup failed'; repo_id=$(jq -er .id "$dir/repo.json")
policy=$(jq -cn --arg p "$project_id" --arg h git --arg c "$git_ip/32" --arg f "$fp" '{project_id:$p,configuration_id:"cfg_browser_operator",policy:{version:1,state:"configured",mode:"internal",allowed_schemes:["ssh"],allowed_hosts:[$h],allowed_cidrs:[$c],ssh_host_fingerprints:[$f],credential_reference_id:"cred_git_deploy",allow_internal:true}}'); code=$(http PUT "/api/v1/repositories/$repo_id/policy" "$policy" "$dir/policy.json"); [[ "$code" == 200 ]] || fail 'repo policy fixture setup failed'
service=$(jq -cn --arg p "$project_id" --arg r "$repo_id" '{project_id:$p,name:"browser-fixture",repository_id:$r,compose_path:"compose.yaml"}'); code=$(http POST /api/v1/services "$service" "$dir/service.json"); [[ "$code" == 201 ]] || fail 'service fixture setup failed'; service_id=$(jq -er .id "$dir/service.json")
env=$(jq -cn --arg s "$service_id" --arg cp "$compose_project" --arg url "$health_url" --arg host "$health_host" --arg cidr "$health_cidr" '{service_id:$s,name:"browser-runtime",runner_selector:["compose-runtime"],compose_project:$cp,timeout_seconds:40,rollback_safe:true,health_policy:{url:$url,allowed_hosts:[$host],allowed_cidrs:[$cidr],allowed_ports:[8080],allow_http:true,interval_seconds:1,timeout_seconds:10,expected_status:200},secret_bindings:[{name:"git",provider:"runner_file",reference:"cred_git_deploy",target:"env:GIT_SSH_KEY",required:true,version:"v1"},{name:"application",provider:"runner_file",reference:"app_secret",target:"file:app_secret",required:true,version:"v1"}]}' ); code=$(http POST /api/v1/environments "$env" "$dir/env.json"); [[ "$code" == 201 ]] || fail 'environment fixture setup failed'; env_id=$(jq -er .id "$dir/env.json")
compose run --rm --no-deps git_advance_b >"$dir/advance-b.log"; b_commit=$(compose exec -T git sh -ec 'git config --global --add safe.directory /git/repo.git; git --git-dir=/git/repo.git rev-parse refs/heads/main')
compose_hash(){ local commit=$1 override="$dir/compose-secrets.yaml"; compose exec -T git sh -ec "git config --global --add safe.directory /git/repo.git; git --git-dir=/git/repo.git show '$commit:compose.yaml'" >"$dir/compose.yaml"; printf 'secrets:\n  app_secret:\n    file: %s\n' "$runner_secret_root/app_secret" >"$override"; NEROCD_DEPLOYMENT_REVISION="$commit" docker compose -p "$compose_project" -f "$dir/compose.yaml" -f "$override" config --format json | jq -cS 'del(.name) | .secrets.app_secret.file = "nerocd-secret://app_secret"' | shasum -a 256 | awk '{print "sha256:"$1}'; }
b_hash=$(compose_hash "$b_commit"); code=$(http POST /api/v1/revisions "$(jq -cn --arg s "$service_id" '{service_id:$s,requested_ref:"main"}')" "$dir/rev-b.json"); [[ "$code" == 201 ]] || fail 'B revision setup failed'; rev_b=$(jq -er .id "$dir/rev-b.json")
jq -n --arg base "$base" --arg credential "$dir/operator.credentials" --arg project "$project_id" --arg service "$service_id" --arg environment "$env_id" --arg b "$rev_b" --arg commit "$b_commit" --arg hash "$b_hash" --arg image "$fixture_b_ref" '{base:$base,credential_file:$credential,project_id:$project,service_id:$service,environment_id:$environment,revision_b:$b,commit_b:$commit,compose_hash_b:$hash,image_b:$image}' >"$dir/operator.json"; chmod 0600 "$dir/operator.json"
if ! (cd "$root/web/app" && bun "$root/acceptance/runtime-web-operator/operator.mjs" "$dir/operator.json" b "$dir/browser-b.json" >"$dir/browser-b.log" 2>&1); then browser_class=$(browser_failure_class "$dir/browser-b.json"); record "browser_failure_class=$browser_class"; printf 'runtime-web-operator: browser_failure_class=%s\n' "$browser_class" >&2; fail 'browser B deployment failed'; fi; dep_b=$(jq -er .deployment_id "$dir/browser-b.json")
wait_status(){ local id=$1 want=$2 got='' deadline=$((SECONDS+100)); while ((SECONDS<deadline)); do got=$(compose exec -T postgres psql -U nerocd -d nerocd -Atc "select status from deployments where id='$id'"); [[ "$got" == "$want" ]] && return; sleep .5; done; fail "deployment lifecycle did not settle"; }
wait_status "$dep_b" succeeded; [[ "$(fixture_health_body)" == B ]] || fail 'B target health failed'; jq -e '.csp_inline_script_blocked == true and .csp_external_script_blocked == true' "$dir/browser-b.json" >/dev/null || fail 'browser CSP probe did not pass'; record 'ui_B=true nonterminal_polling=true immutable_provenance=true health_B=true csp_inline_external_script_blocked=true'
compose run --rm --no-deps git_advance_c >"$dir/advance-c.log"; code=$(http POST /api/v1/revisions "$(jq -cn --arg s "$service_id" '{service_id:$s,requested_ref:"main"}')" "$dir/rev-c.json"); [[ "$code" == 201 ]] || fail 'C revision setup failed'; rev_c=$(jq -er .id "$dir/rev-c.json"); jq --arg c "$rev_c" '.revision_c=$c' "$dir/operator.json" >"$dir/operator-c.json"
if ! (cd "$root/web/app" && bun "$root/acceptance/runtime-web-operator/operator.mjs" "$dir/operator-c.json" c "$dir/browser-c.json" >"$dir/browser-c.log" 2>&1); then browser_class=$(browser_failure_class "$dir/browser-c.json"); record "browser_failure_class=$browser_class"; printf 'runtime-web-operator: browser_failure_class=%s\n' "$browser_class" >&2; fail 'browser C deployment failed'; fi; dep_c=$(jq -er .deployment_id "$dir/browser-c.json")
wait_status "$dep_c" rolled_back; child=$(compose exec -T postgres psql -U nerocd -d nerocd -Atc "select id from deployments where rollback_of_id='$dep_c'"); [[ -n "$child" && "$(compose exec -T postgres psql -U nerocd -d nerocd -Atc "select status from deployments where id='$child'")" == rolled_back ]] || fail 'C did not create one successful rollback child'; [[ "$(compose exec -T postgres psql -U nerocd -d nerocd -Atc "select count(*) from deployments where rollback_of_id='$dep_c'")" == 1 ]] || fail 'rollback child cardinality failed'; [[ "$(fixture_health_body)" == B ]] || fail 'rollback target health did not return B'; [[ "$(compose exec -T postgres psql -U nerocd -d nerocd -Atc "select current_healthy_revision_id from environments where id='$env_id'")" == "$rev_b" ]] || fail 'healthy pointer did not retain B'
! grep -Eiq 'Bearer [A-Za-z0-9._-]+|BEGIN (OPENSSH|RSA|EC|DSA) PRIVATE KEY|runner_web' "$dir/browser-b.log" "$dir/browser-c.log" "$evidence" || fail 'browser evidence leaked credential or private runner detail'; record 'ui_C=true applying_or_verifying_failure=true rollback_source_child=true pointer_B=true health_B=true'; record "browser_runner_endpoints=0 page_errors=0 console_nonresource_errors=0 console_resource_diagnostics=$(jq -r .console_resource_diagnostics "$dir/browser-b.json") query_free_reload_history_mobile=true"; record 'browser_cancel=not_claimed; covered_by=runtime-compose-gate'; pass=true
