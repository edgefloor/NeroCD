#!/usr/bin/env bash
# Real typed Compose A/B/C gate. It uses only the HTTP control-plane APIs to
# create deployment objects; psql below is read-only acceptance evidence.
set -Eeuo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
file="$root/acceptance/runtime-compose/compose.yaml" evidence=/tmp/nerocd-compose-runtime.txt
runtime_profile=${NEROCD_RUNTIME_PROFILE:-development}
case "$runtime_profile" in development|production) ;; *) printf 'runtime-compose: invalid NEROCD_RUNTIME_PROFILE\n' >&2; exit 2 ;; esac
if [[ "$runtime_profile" == production ]]; then file+=" $root/acceptance/runtime-compose/compose.production-dogfood.yaml"; fi
dir=$(mktemp -d /tmp/nerocd-runtime-compose.XXXXXXXX)
suffix=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')
project="nerocd-compose-$suffix" image="nerocd-compose-runtime:$suffix" runner_image="nerocd-compose-runner:$suffix" git_image="nerocd-compose-git:$suffix"
fixture_a="nerocd-compose-fixture-a:$suffix" fixture_b="nerocd-compose-fixture-b:$suffix" fixture_c="nerocd-compose-fixture-c:$suffix" pass=false
: >"$evidence"; record(){ printf '%s\n' "$*" >>"$evidence"; }; fail(){ trap - ERR; record "FAIL: $*"; printf 'runtime-compose: %s\n' "$*" >&2; exit 1; }
# Docker Desktop can remap the host socket group inside containers. Derive the
# effective mount GID in the same mount namespace the non-root runner uses.
docker_gid(){ docker run --rm -v /var/run/docker.sock:/var/run/docker.sock alpine:3.22.2@sha256:4b7ce07002c69e8f3d704a9c5d6fd3053be500b7f1c69fc0d80990c2ad8dd412 sh -ec 'stat -c %g /var/run/docker.sock'; }
# Docker Compose needs a locally-addressable repository to apply a fixture;
# the adapter extracts and persists only the unambiguous sha256 digest from
# this repository@digest input. Assertions below reject any non-pure persisted
# digest, so a mutable tag never enters deployment provenance.
compose(){
  local -a files=(-f "$root/acceptance/runtime-compose/compose.yaml")
  [[ "$runtime_profile" != production ]] || files+=(-f "$root/acceptance/runtime-compose/compose.production-dogfood.yaml")
  NEROCD_RUNTIME_IMAGE="$image" NEROCD_RUNNER_IMAGE="$runner_image" NEROCD_GIT_IMAGE="$git_image" NEROCD_FIXTURE_A="$fixture_a@$fixture_a_id" NEROCD_FIXTURE_B="$fixture_b@$fixture_b_id" NEROCD_FIXTURE_C="$fixture_c@$fixture_c_id" NEROCD_DOCKER_GID="$socket_gid" docker compose -p "$project" "${files[@]}" "$@"
}
db_user=${NEROCD_RUNTIME_OWNER_DATABASE_USER:-nerocd}
psql_query(){ compose exec -T postgres psql -U "$db_user" -d nerocd -Atc "$1"; }
diagnose(){ set +e; record diagnostic_begin=true; compose ps --format json >>"$evidence" 2>/dev/null || true; compose logs --no-color runner 2>/dev/null | sed -E 's/(Bearer |fence|PRIVATE KEY)[^[:space:]]*/[REDACTED]/g' | tail -n 120 >>"$evidence"; compose logs --no-color server git postgres secret-init pgdata-init migrate role-init 2>/dev/null | sed -E 's#postgres://[^@[:space:]]+@#postgres://[REDACTED]@#g;s/(Bearer |fence|PRIVATE KEY)[^[:space:]]*/[REDACTED]/g' | tail -n 160 >>"$evidence"; psql_query "select 'deployments='||count(*) from deployments union all select 'attempts='||count(*) from deployment_attempts union all select 'audits='||count(*) from audit_events union all select 'deployment_state='||d.id||':'||d.status||':'||coalesce(d.failure_code,'')||':desired='||d.desired_revision_id||':previous='||coalesce(d.previous_healthy_revision_id,'') from deployments d order by 1; select 'revision='||id||':'||requested_ref||':'||git_commit||':'||compose_hash||':'||array_to_string(image_digests,',') from revisions order by id" >>"$evidence" 2>/dev/null || true; record diagnostic_end=true; }
cleanup(){ local code=$? ids rem target=${compose_project:-}; trap - ERR; set +e; [[ "$pass" == true ]] || diagnose; [[ -z "$target" ]] || docker compose -p "$target" down --volumes --remove-orphans --timeout 4 >/dev/null 2>&1 || true; compose down --volumes --remove-orphans --rmi local --timeout 4 >/dev/null 2>&1 || true; for labeled_project in "$project" "$target"; do [[ -z "$labeled_project" ]] && continue; ids=$(docker ps -aq --filter "label=com.docker.compose.project=$labeled_project"); [[ -z "$ids" ]] || docker rm -f $ids >/dev/null 2>&1 || true; ids=$(docker volume ls -q --filter "label=com.docker.compose.project=$labeled_project"); [[ -z "$ids" ]] || docker volume rm $ids >/dev/null 2>&1 || true; ids=$(docker network ls -q --filter "label=com.docker.compose.project=$labeled_project"); [[ -z "$ids" ]] || docker network rm $ids >/dev/null 2>&1 || true; done; docker image rm -f "$image" "$runner_image" "$git_image" "$fixture_a" "$fixture_b" "$fixture_c" >/dev/null 2>&1 || true; rem=$(for labeled_project in "$project" "$target"; do [[ -z "$labeled_project" ]] || docker ps -aq --filter "label=com.docker.compose.project=$labeled_project"; [[ -z "$labeled_project" ]] || docker volume ls -q --filter "label=com.docker.compose.project=$labeled_project"; [[ -z "$labeled_project" ]] || docker network ls -q --filter "label=com.docker.compose.project=$labeled_project"; done); [[ -z "$rem" ]] || code=1; record "cleanup_complete=$([[ -z "$rem" ]] && echo true || echo false)"; [[ "$pass" != true || $code -ne 0 ]] || record 'PASS: real typed Compose A/B/C rollback and restart gate'; rm -rf -- "$dir"; printf 'runtime compose evidence: %s\n' "$evidence"; exit "$code"; }
trap cleanup EXIT; trap 'fail "unexpected command failure line $LINENO"' ERR
for x in docker curl jq git od; do command -v "$x" >/dev/null || fail "missing $x"; done; docker info >/dev/null || fail 'Docker unavailable'; [[ -S /var/run/docker.sock ]] || fail 'Docker socket unavailable'
socket_gid=$(docker_gid); [[ "$socket_gid" =~ ^[0-9]+$ ]] || fail 'Docker socket GID unavailable'; record "project=$project docker_gid=$socket_gid socket_root_equivalent=true"
docker build --pull -t "$image" "$root" >"$dir/build.log" 2>&1 || fail 'fresh server image build failed'
docker build --pull -f "$root/acceptance/runtime-compose/GitDockerfile" -t "$git_image" "$root/acceptance/runtime-compose" >"$dir/git.log" 2>&1 || fail 'git fixture image build failed'
docker build --pull -f "$root/acceptance/runtime-compose/FixtureDockerfile" --build-arg VERSION=A --build-arg MODE=good --build-arg BUILD_NONCE="$suffix" -t "$fixture_a" "$root/acceptance/runtime-compose" >"$dir/a.log" 2>&1 || fail 'fixture A build failed'
docker build --pull -f "$root/acceptance/runtime-compose/FixtureDockerfile" --build-arg VERSION=B --build-arg MODE=good --build-arg BUILD_NONCE="$suffix" -t "$fixture_b" "$root/acceptance/runtime-compose" >"$dir/b.log" 2>&1 || fail 'fixture B build failed'
docker build --pull -f "$root/acceptance/runtime-compose/FixtureDockerfile" --build-arg VERSION=C --build-arg MODE=slow --build-arg BUILD_NONCE="$suffix" -t "$fixture_c" "$root/acceptance/runtime-compose" >"$dir/c.log" 2>&1 || fail 'fixture C build failed'
fixture_a_id=$(docker image inspect -f '{{.Id}}' "$fixture_a"); fixture_b_id=$(docker image inspect -f '{{.Id}}' "$fixture_b"); fixture_c_id=$(docker image inspect -f '{{.Id}}' "$fixture_c"); [[ "$fixture_a_id" =~ ^sha256: && "$fixture_b_id" =~ ^sha256: && "$fixture_c_id" =~ ^sha256: ]] || fail 'fixture image IDs are not digests'
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
compose build runner >"$dir/runner-build.log" 2>&1 || fail 'runner image build failed'; compose up -d runner >"$dir/runner.log" 2>&1 || fail 'runner start failed'; runner_cid=$(compose ps -q runner); [[ -n "$runner_cid" ]] || fail 'runner unavailable'
uid=$(compose exec -T runner id -u); [[ "$uid" == 10001 ]] || fail 'runner is not non-root'; if ! compose exec -T runner sh -ec 'id; ls -ln /var/run/docker.sock; test -S /var/run/docker.sock; docker version >/dev/null' >"$dir/socket.log" 2>&1; then record "socket_error=$(tail -n 8 "$dir/socket.log" | tr '\n' ' ')"; fail 'non-root runner cannot use declared Docker socket'; fi
host_ip=$(compose exec -T runner sh -ec "getent ahostsv4 host.docker.internal | awk 'NR==1 {print \$1}'"); [[ "$host_ip" =~ ^[0-9.]+$ ]] || fail 'runner host gateway resolution failed'
git_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$(compose ps -q git)"); fp=$(docker run --rm --network none -v "$(docker volume ls -q --filter label=com.docker.compose.project=$project --filter label=com.docker.compose.volume=git-data | head -1):/git:ro" "$git_image" sh -c 'ssh-keygen -lf /git/keys/host.pub' | awk '{print $2}')
repo=$(jq -cn --arg p "$project_id" '{project_id:$p,name:"compose-fixture",url:"ssh://git@git:2222/repo.git",provider:"git",default_ref:"main"}'); code=$(http POST "$base/api/v1/repositories" "$admin" "$repo" "$dir/repo.json"); [[ "$code" == 201 ]] || fail 'repository create failed'; repo_id=$(jq -er .id "$dir/repo.json")
policy=$(jq -cn --arg p "$project_id" --arg h git --arg c "$git_ip/32" --arg f "$fp" '{project_id:$p,configuration_id:"cfg_compose_runtime",policy:{version:1,state:"configured",mode:"internal",allowed_schemes:["ssh"],allowed_hosts:[$h],allowed_cidrs:[$c],ssh_host_fingerprints:[$f],credential_reference_id:"cred_git_deploy",allow_internal:true}}'); code=$(http PUT "$base/api/v1/repositories/$repo_id/policy" "$admin" "$policy" "$dir/policy.json"); [[ "$code" == 200 ]] || fail 'repository policy failed'
service=$(jq -cn --arg p "$project_id" --arg r "$repo_id" '{project_id:$p,name:"compose-fixture",repository_id:$r,compose_path:"compose.yaml"}'); code=$(http POST "$base/api/v1/services" "$admin" "$service" "$dir/service.json"); [[ "$code" == 201 ]] || fail 'service create failed'; service_id=$(jq -er .id "$dir/service.json")
compose_project="target_$suffix"; env=$(jq -cn --arg s "$service_id" --arg cp "$compose_project" --arg host host.docker.internal --arg cidr "$host_ip/32" '{service_id:$s,name:"runtime",runner_selector:["compose-runtime"],compose_project:$cp,timeout_seconds:40,rollback_safe:true,health_policy:{url:"http://host.docker.internal:18080/cgi-bin/health",allowed_hosts:[$host],allowed_cidrs:[$cidr],allowed_ports:[18080],allow_http:true,interval_seconds:1,timeout_seconds:10,expected_status:200},secret_bindings:[{name:"git",provider:"runner_file",reference:"cred_git_deploy",target:"env:GIT_SSH_KEY",required:true,version:"v1",fingerprint:"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}' ); code=$(http POST "$base/api/v1/environments" "$admin" "$env" "$dir/env.json"); [[ "$code" == 201 ]] || fail 'environment create failed'; env_id=$(jq -er .id "$dir/env.json")
deploy(){ local label=$1; code=$(http POST "$base/api/v1/revisions" "$admin" "$(jq -cn --arg s "$service_id" '{service_id:$s,requested_ref:"main"}')" "$dir/rev-$label.json"); [[ "$code" == 201 ]] || fail "revision $label failed"; local rev=$(jq -er .id "$dir/rev-$label.json"); deploy_existing "$label" "$rev"; }
deploy_existing(){ local label=$1 rev=$2; code=$(http POST "$base/api/v1/deployments" "$admin" "$(jq -cn --arg e "$env_id" --arg r "$rev" --arg k "compose-$label" '{environment_id:$e,desired_revision_id:$r,idempotency_key:$k}')" "$dir/dep-$label.json"); [[ "$code" == 201 ]] || fail "deployment $label failed"; printf '%s %s\n' "$rev" "$(jq -er .id "$dir/dep-$label.json")"; }
cancel_deployment(){ local id=$1 request=$2; code=$(http POST "$base/api/v1/deployments/cancel" "$admin" "$(jq -cn --arg d "$id" --arg r "$request" '{deployment_id:$d,request_id:$r}')" "$dir/cancel-$id.json"); [[ "$code" == 200 ]] || fail "deployment cancellation failed code=$code"; }
origin_commit(){ compose exec -T git sh -ec "git config --global --add safe.directory /git/repo.git; git --git-dir=/git/repo.git rev-parse refs/heads/$1"; }
revision_observation(){ local id=$1 commit=$2 hash=$3 image=$4; code=$(http GET "$base/api/v1/revisions?service_id=$service_id" "$admin" '' "$dir/revisions-$id.json"); [[ "$code" == 200 ]] || fail "revision observation failed"; jq -e --arg id "$id" --arg c "$commit" --arg h "$hash" --arg i "$image" '.items[] | select(.id==$id and .provenance_state=="resolved" and .provenance_resolved==true and .git_commit==$c and .compose_hash==$h and .content_identity==($c+":"+$h) and .image_digests==[$i])' "$dir/revisions-$id.json" >/dev/null || fail "immutable revision observation mismatched"; }
provenance_receipt(){ local dep=$1 rev=$2 commit=$3 hash=$4 image=$5 expected=$6 observed duplicated; observed=$(psql_query "select count(*) from provenance_resolutions p join audit_events a on a.id=p.audit_id where p.deployment_id='$dep' and p.revision_id='$rev' and p.git_commit='$commit' and p.compose_hash='$hash' and p.content_identity='$commit:$hash' and p.image_digests=array['$image'] and a.action='runner.deployment.provenance.resolve'"); duplicated=$(psql_query "select count(*) from (select attempt from provenance_resolutions where deployment_id='$dep' group by attempt having count(*)<>1) x"); [[ "$observed" == "$expected" && "$duplicated" == 0 ]] || fail "provenance receipt count dep=$dep got=$observed want=$expected duplicate_attempts=$duplicated"; }
expected_provenance(){ local name commit image source envfile raw canonical; name=$1; commit=$2; image=$3; source="$dir/compose-$name.yaml"; envfile="$dir/compose-$name.env"; compose exec -T git sh -ec "git config --global --add safe.directory /git/repo.git; git --git-dir=/git/repo.git show '$commit:compose.yaml'" >"$source" || fail "fixture compose extraction failed"; printf '%s=%s\n' NEROCD_DEPLOYMENT_REVISION "$commit" >"$envfile"; raw=$(NEROCD_DEPLOYMENT_REVISION="$commit" docker compose --project-name "$compose_project" --env-file "$envfile" --file "$source" config --format json) || fail "fixture canonical compose failed"; canonical=$(printf '%s' "$raw" | jq -cS 'del(.name)') || fail "fixture canonical JSON failed"; printf '%s' "$canonical" | shasum -a 256 | awk '{print "sha256:"$1}'; grep -Eq '@sha256:[0-9a-f]{64}$' "$source" || fail "fixture source lacks immutable digest pin"; ! grep -Eiq '(^|[^[:alnum:]_])(build|pull)([^[:alnum:]_]|$)' "$source" || fail "fixture source contains forbidden build or pull"; }
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
a_commit=$(origin_commit main); b_commit=$(origin_commit fixture-b); c_commit=$(origin_commit fixture-c); d_commit=$(origin_commit fixture-d); a_hash=$(expected_provenance a "$a_commit" "$fixture_a_id"); b_hash=$(expected_provenance b "$b_commit" "$fixture_b_id"); c_hash=$(expected_provenance c "$c_commit" "$fixture_c_id"); d_hash=$(expected_provenance d "$d_commit" "$fixture_c_id"); record "fixture_provenance a=$a_commit/$a_hash b=$b_commit/$b_hash c=$c_commit/$c_hash d=$d_commit/$d_hash images=pure_sha256"
read -r rev_a dep_a <<<"$(deploy a)"; wait_status "$dep_a" succeeded; revision_observation "$rev_a" "$a_commit" "$a_hash" "$fixture_a_id"; provenance_receipt "$dep_a" "$rev_a" "$a_commit" "$a_hash" "$fixture_a_id" 1; [[ "$(curl -fsS --max-time 5 http://127.0.0.1:18080/cgi-bin/health)" == A ]] || fail 'A health body failed'; pointer=$(psql_query "select current_healthy_revision_id from environments where id='$env_id'"); [[ "$pointer" == "$rev_a" ]] || fail 'A healthy pointer failed'; record "A=$rev_a deployment=$dep_a health=A immutable_provenance=true"
compose run --rm --no-deps git_advance_b >"$dir/advance-b.log"; revision_observation "$rev_a" "$a_commit" "$a_hash" "$fixture_a_id"; provenance_receipt "$dep_a" "$rev_a" "$a_commit" "$a_hash" "$fixture_a_id" 1; read -r rev_b dep_b <<<"$(deploy b)"; wait_status "$dep_b" succeeded; revision_observation "$rev_b" "$b_commit" "$b_hash" "$fixture_b_id"; provenance_receipt "$dep_b" "$rev_b" "$b_commit" "$b_hash" "$fixture_b_id" 1; [[ "$(curl -fsS --max-time 5 http://127.0.0.1:18080/cgi-bin/health)" == B ]] || fail 'B health body failed'; pointer=$(psql_query "select current_healthy_revision_id from environments where id='$env_id'"); [[ "$pointer" == "$rev_b" ]] || fail 'B healthy pointer failed'; record "B=$rev_b deployment=$dep_b health=B immutable_provenance=true"
compose_trace(){ local after=$1; compose exec -T runner sh -ec "test -f /journal/compose-trace.log && tail -n +$((after+1)) /journal/compose-trace.log"; }
compose_trace_lines(){ compose exec -T runner sh -ec 'test -f /journal/compose-trace.log && wc -l < /journal/compose-trace.log || true'; }
wait_runner_barrier(){ local name=$1 deadline=$((SECONDS+45)); while ((SECONDS<deadline)); do compose exec -T runner sh -ec "test -f /journal/compose-barrier-$name-entered" >/dev/null 2>&1 && return; sleep .2; done; fail "runner did not reach $name barrier"; }
pre_trace=$(compose_trace_lines); target_pre=$(docker ps -q --filter "ancestor=$fixture_b" --filter "label=com.docker.compose.project=$compose_project" --filter 'label=com.docker.compose.service=fixture'); [[ -n "$target_pre" ]] || fail 'pre-cancel missing B target'; created_pre=$(docker inspect -f '{{.Created}}' "$target_pre"); compose exec -T runner sh -ec 'rm -f /journal/compose-barrier-config-entered; : >/journal/compose-barrier-config'; read -r rev_pre dep_pre <<<"$(deploy preapply-cancel)"; wait_runner_barrier config; cancel_deployment "$dep_pre" preapply-cancel-receipt; code=$(http POST "$base/api/v1/deployments/cancel" "$admin" "$(jq -cn --arg d "$dep_pre" '{deployment_id:$d,request_id:"preapply-cancel-receipt"}')" "$dir/cancel-replay.json"); [[ "$code" == 200 ]] || fail "pre-cancel replay code=$code"; code=$(http POST "$base/api/v1/deployments/cancel" "$admin" "$(jq -cn --arg d "$dep_pre" '{deployment_id:$d,request_id:"changed-receipt"}')" "$dir/cancel-conflict.json"); [[ "$code" == 409 ]] || fail "pre-cancel changed receipt code=$code"; wait_status "$dep_pre" canceled; sleep 5; pre_rows=$(psql_query "select d.status||':'||r.status||':'||l.status||':'||a.status from deployments d join task_runs r on r.id=d.task_run_id join run_leases l on l.run_id=r.id join deployment_attempts a on a.lease_id=l.id where d.id='$dep_pre'"); [[ "$pre_rows" == 'canceled:canceled:canceled:canceled' ]] || fail "pre-cancel lifecycle=$pre_rows"; pre_trace_after=$(compose_trace "$pre_trace"); [[ $(awk '$3=="action=compose_up" {n++} END {print n+0}' <<<"$pre_trace_after") == 0 ]] || fail "pre-apply cancellation mutated target trace=[$pre_trace_after]"; target_after=$(docker ps -q --filter "ancestor=$fixture_b" --filter "label=com.docker.compose.project=$compose_project" --filter 'label=com.docker.compose.service=fixture'); [[ "$target_pre" == "$target_after" && "$(docker inspect -f '{{.Created}}' "$target_after")" == "$created_pre" ]] || fail 'pre-cancel mutated B target'; runner_live=$(compose ps -q runner); [[ -n "$runner_live" && "$(docker inspect -f '{{.State.Running}}' "$runner_live")" == true ]] || fail 'runner did not survive pre-cancel'; pre_audits=$(psql_query "select action from audit_events where target_id='$dep_pre' order by created_at,id"); [[ "$pre_audits" == $'deployment.create\nrunner.deployment.transition\ndeployment.cancel' ]] || fail "pre-cancel audit order=$pre_audits"; pre_audit_count=$(psql_query "select count(*) from audit_events where target_id='$dep_pre'"); sleep 2; [[ "$(psql_query "select count(*) from audit_events where target_id='$dep_pre'")" == "$pre_audit_count" ]] || fail 'pre-cancel produced later writes'; record "preapply_cancel deployment=$dep_pre terminal=canceled exact_replay_conflict=true zero_up=true runner_alive=true"
compose run --rm --no-deps git_advance_c >"$dir/advance-c-during.log"; during_trace=$(compose_trace_lines); target_during_before=$(docker ps -q --filter "ancestor=$fixture_b" --filter "label=com.docker.compose.project=$compose_project" --filter 'label=com.docker.compose.service=fixture'); created_during_before=$(docker inspect -f '{{.Created}}' "$target_during_before"); compose exec -T runner sh -ec 'rm -f /journal/compose-barrier-up-entered /journal/compose-barrier-up-descendant.pid; : >/journal/compose-barrier-up'; read -r rev_during dep_during <<<"$(deploy during-apply-cancel)"; wait_runner_barrier up; wait_public_status "$dep_during" applying; target_during_applying=$(docker ps -q --filter "ancestor=$fixture_b" --filter "label=com.docker.compose.project=$compose_project" --filter 'label=com.docker.compose.service=fixture'); [[ "$target_during_before" == "$target_during_applying" ]] || fail 'during-cancel mutated target before blocked up'; cancel_deployment "$dep_during" during-apply-cancel-receipt; code=$(http POST "$base/api/v1/deployments/cancel" "$admin" "$(jq -cn --arg d "$dep_during" '{deployment_id:$d,request_id:"during-apply-cancel-receipt"}')" "$dir/cancel-during-replay.json"); [[ "$code" == 200 ]] || fail "during-cancel replay code=$code"; code=$(http POST "$base/api/v1/deployments/cancel" "$admin" "$(jq -cn --arg d "$dep_during" '{deployment_id:$d,request_id:"during-apply-changed"}')" "$dir/cancel-during-conflict.json"); [[ "$code" == 409 ]] || fail "during-cancel changed receipt code=$code"; wait_status "$dep_during" rolled_back; child_during=$(psql_query "select id from deployments where rollback_of_id='$dep_during'"); child_during_count=$(psql_query "select count(*) from deployments where rollback_of_id='$dep_during'"); [[ "$child_during_count" == 1 && -n "$child_during" && "$(status "$child_during")" == rolled_back ]] || fail 'during-cancel rollback child cardinality or terminal state failed'; during_rows=$(psql_query "select d.id||':'||d.status||':'||r.status||':'||l.status||':'||a.status from deployments d join task_runs r on r.id=d.task_run_id join run_leases l on l.run_id=r.id join deployment_attempts a on a.lease_id=l.id where d.id in ('$dep_during','$child_during') order by case when d.id='$dep_during' then 0 else 1 end,a.attempt"); expected_during_rows="$dep_during:rolled_back:failed:failed:failed
$child_during:rolled_back:succeeded:succeeded:succeeded"; [[ "$during_rows" == "$expected_during_rows" ]] || fail "during-cancel lifecycle=$during_rows"; child_during_rev=$(psql_query "select desired_revision_id from deployments where id='$child_during'"); [[ "$child_during_rev" == "$rev_b" ]] || fail 'during-cancel child did not restore B revision'; pointer=$(psql_query "select current_healthy_revision_id from environments where id='$env_id'"); [[ "$pointer" == "$rev_b" && "$(curl -fsS --max-time 5 http://127.0.0.1:18080/cgi-bin/health)" == B ]] || fail 'during-cancel did not restore B health/pointer'; desc_state=$(compose exec -T runner sh -ec 'p=$(cat /journal/compose-barrier-up-descendant.pid); if test ! -d /proc/$p; then echo gone; else awk "{print \$3}" /proc/$p/stat; fi'); [[ "$desc_state" == gone || "$desc_state" == Z ]] || fail "during-cancel descendant survived state=$desc_state"; during_trace_after=$(compose_trace "$during_trace"); [[ $(awk '$3=="action=compose_up" {n++} END {print n+0}' <<<"$during_trace_after") == 1 ]] || fail "during-cancel expected exactly one blocked source compose up trace=[$during_trace_after]"; c_targets=$(docker ps -aq --filter "ancestor=$fixture_c" --filter "label=com.docker.compose.project=$compose_project" --filter 'label=com.docker.compose.service=fixture'); [[ -z "$c_targets" ]] || fail 'during-cancel left C target container'; during_audits=$(psql_query "select action||':'||target_id from audit_events where target_id in ('$dep_during','$child_during') order by created_at,id"); expected_during_audits="deployment.create:$dep_during
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
wait_status "$dep_c" rolled_back; revision_observation "$rev_c" "$c_commit" "$c_hash" "$fixture_c_id"; provenance_receipt "$dep_c" "$rev_c" "$c_commit" "$c_hash" "$fixture_c_id" 2; [[ "$(curl -fsS --max-time 5 http://127.0.0.1:18080/cgi-bin/health)" == B ]] || fail 'C rollback did not restore B health'; child_c=$(psql_query "select id from deployments where rollback_of_id='$dep_c'"); [[ -n "$child_c" && "$(status "$child_c")" == rolled_back ]] || fail 'C rollback child did not settle'; rollback_lifecycle "$dep_c" "$child_c" rolled_back rolled_back 2; child_c_rev=$(psql_query "select desired_revision_id from deployments where id='$child_c'"); [[ "$child_c_rev" == "$rev_b" ]] || fail 'C rollback child did not use B revision'; revision_observation "$child_c_rev" "$b_commit" "$b_hash" "$fixture_b_id"; provenance_receipt "$child_c" "$child_c_rev" "$b_commit" "$b_hash" "$fixture_b_id" 1; pointer=$(psql_query "select current_healthy_revision_id from environments where id='$env_id'"); [[ "$pointer" == "$rev_b" ]] || fail 'C rollback changed B pointer'; record "C=$rev_c deployment=$dep_c child=$child_c source_child=rolled_back pointer=B reconcile_container_unchanged=true"
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
[[ "$partition_expired" == true && "$(psql_query "select status from run_leases where id='$partition_lease'")" == expired ]] || fail 'DB-clock lease expiry was not observed during 60s partition'
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
[[ "$pointer" == "$rev_b" && "$(curl -fsS --max-time 5 http://127.0.0.1:18080/cgi-bin/health)" == B ]] || fail 'partition recovery did not preserve B target/pointer'
record "runner_api_partition seconds=60 runner_only=true db_clock_expired=true stale_fence_denied=true stale_log_append_denied=true committed_event_response_held=true reclaimed=true attempts=$partition_attempts log_rows=$partition_log_before-$partition_log_after log_sequence_contiguous_unique=true exact_stage_log_once_per_fenced_attempt=true source=$dep_partition child=$partition_child pointer=B"
record 'compose_trace_c_begin=true'; compose_trace "$trace_before" >>"$evidence"; record 'compose_trace_c_end=true'
# Make the immutable B artifact unavailable *before* D can create its rollback
# child.  Doing it after the source reports its failure races the runner's next
# claim, allowing the child to pass its preflight before this forced failure is
# established.
ids=$(docker ps -aq --filter "ancestor=$fixture_b"); [[ -z "$ids" ]] || docker rm -f $ids >/dev/null || fail 'forced rollback fixture container removal failed'; docker image rm -f "$fixture_b" >/dev/null || fail 'forced rollback image removal failed'; docker image inspect "$fixture_b_id" >/dev/null 2>&1 && fail 'forced rollback image remained available'; record 'rollback_artifact_unavailable_before_d=true'
compose run --rm --no-deps git_advance_d >"$dir/advance-d.log"; read -r rev_d dep_d <<<"$(deploy d)"; wait_status "$dep_d" rollback_failed; revision_observation "$rev_d" "$d_commit" "$d_hash" "$fixture_c_id"; provenance_receipt "$dep_d" "$rev_d" "$d_commit" "$d_hash" "$fixture_c_id" 1; child_d=$(psql_query "select id from deployments where rollback_of_id='$dep_d'"); child_d_count=$(psql_query "select count(*) from deployments where rollback_of_id='$dep_d'"); [[ "$child_d_count" == 1 && -n "$child_d" && "$(status "$child_d")" == rollback_failed ]] || fail 'forced rollback failure did not have exactly one loud child'; rollback_lifecycle "$dep_d" "$child_d" rollback_failed rollback_failed 1; child_d_rev=$(psql_query "select desired_revision_id from deployments where id='$child_d'"); [[ "$child_d_rev" == "$rev_b" ]] || fail 'forced rollback failure child did not retain B provenance'; revision_observation "$child_d_rev" "$b_commit" "$b_hash" "$fixture_b_id"; provenance_receipt "$child_d" "$child_d_rev" "$b_commit" "$b_hash" "$fixture_b_id" 1; pointer=$(psql_query "select current_healthy_revision_id from environments where id='$env_id'"); [[ "$pointer" == "$rev_b" ]] || fail 'rollback failure changed healthy pointer'; record "rollback_failure source=$dep_d child=$child_d pointer=B operator_visible=true immutable_provenance=true"
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
