#!/usr/bin/env bash
# A real, isolated production profile check.  It deliberately never calls a
# store/service directly: setup is Compose and observations use the shipped
# binary, Docker metadata, and read-only psql.
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
source "$root/scripts/local-image-registry.sh"
evidence=/tmp/nerocd-production-profile.txt
dir=$(mktemp -d /tmp/nerocd-production-profile.XXXXXXXX)
suffix=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')
project="nerocd-production-$suffix"
proxy="nerocd-production-proxy-$suffix"
image_tag="nerocd-production-server:$suffix"
pass=false
owner_password=''
app_password=''
compose_ready=false
image_built=false
proxy_created=false
startup_diagnostic_lines=80
: >"$evidence"
record(){ printf '%s\n' "$*" >>"$evidence"; }
fail(){ trap - ERR; record "FAIL: $*"; printf 'production-profile: %s\n' "$*" >&2; exit 1; }
compose(){ NEROCD_IMAGE="$image_ref" NEROCD_PROXY_NETWORK="$proxy" NEROCD_PUBLIC_ORIGIN='https://nerocd.example.invalid' NEROCD_OWNER_DATABASE_USER="$owner_role" NEROCD_APP_DATABASE_USER="$app_role" NEROCD_DATABASE_URL_SECRET="$dir/database-url" NEROCD_APP_DATABASE_URL_SECRET="$dir/app-database-url" NEROCD_POSTGRES_PASSWORD_SECRET="$dir/postgres-password" docker compose -p "$project" -f "$root/compose.production.yaml" "$@"; }
# Compose and Docker diagnostics can include command environments. Keep the
# two generated credentials out of terminal output and retained evidence.
redact_stream(){
  local line
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ -n "$owner_password" ]]; then
      line=${line//"$owner_password"/'[REDACTED_OWNER_PASSWORD]'}
    fi
    if [[ -n "$app_password" ]]; then
      line=${line//"$app_password"/'[REDACTED_APP_PASSWORD]'}
    fi
    printf '%s\n' "$line"
  done
}
append_redacted_file(){
  local file=$1
  [[ -f "$file" ]] || return 0
  redact_stream <"$file" >>"$evidence"
}
emit_startup_diagnostics(){
  local raw="$dir/startup-diagnostics.raw"
  {
    printf '%s\n' '--- production-profile startup diagnostics: compose up output ---'
    tail -n "$startup_diagnostic_lines" "$dir/up.log"
    printf '%s\n' '--- production-profile startup diagnostics: compose ps -a ---'
    compose ps -a
    for service in secret-init pgdata-init backup-data-init postgres migrate role-init server backup-scheduler; do
      printf '%s\n' "--- production-profile startup diagnostics: $service (last $startup_diagnostic_lines lines) ---"
      compose logs --no-color --tail "$startup_diagnostic_lines" "$service"
    done
  } >"$raw" 2>&1 || true
  redact_stream <"$raw" | tee -a "$evidence" >&2
}
if [[ "${1:-}" == '--redact-stdin' ]]; then
  [[ $# -eq 3 ]] || { printf '%s\n' 'usage: production-profile-gate.sh --redact-stdin OWNER_PASSWORD APP_PASSWORD' >&2; exit 64; }
  owner_password=$2
  app_password=$3
  redact_stream
  exit 0
fi
if [[ "${1:-}" == '--redact-tail-stdin' ]]; then
  [[ $# -eq 3 ]] || { printf '%s\n' 'usage: production-profile-gate.sh --redact-tail-stdin OWNER_PASSWORD APP_PASSWORD' >&2; exit 64; }
  owner_password=$2
  app_password=$3
  tail -n "$startup_diagnostic_lines" | redact_stream
  exit 0
fi
owner_psql(){ compose exec -T postgres psql -v ON_ERROR_STOP=1 -U "$owner_role" -d nerocd "$@"; }
# The password is only passed to this short-lived exec environment.  It is
# never put in an SQL command, evidence, Docker inspect, or service logs.
app_psql(){ compose exec -T -e "PGPASSWORD=$app_password" postgres psql -v ON_ERROR_STOP=1 -U "$app_role" -d nerocd "$@"; }
require_cross_auth_denied(){
  local description=$1
  local password=$2
  local role=$3
  local output="$dir/cross-auth-${description}.log"
  # Use the service address rather than loopback: the official image retains
  # loopback trust for bootstrap, while the inter-container path is SCRAM.
  if compose exec -T -e "PGPASSWORD=$password" postgres psql -h postgres -v ON_ERROR_STOP=1 -U "$role" -d nerocd -c 'SELECT 1' >"$output" 2>&1; then
    fail "credential unexpectedly authenticated as $description"
  fi
  rg -qi 'password authentication failed|authentication failed' "$output" || fail "cross-auth denial for $description was not authentication failure"
}
require_app_denied(){
  local description=$1
  local sql=$2
  local output="$dir/app-denied-${description}.log"
  if app_psql -c "$sql" >"$output" 2>&1; then
    fail "application role unexpectedly permitted $description"
  fi
  rg -qi 'permission denied|must be owner' "$output" || fail "application role denial for $description was not a privilege denial"
}
cleanup(){
  local code=$?
  local cleanup_complete=true containers volumes networks
  local remaining_containers remaining_volumes remaining_networks
  trap - ERR
  set +e
  if [[ "$compose_ready" == true ]]; then
    compose logs --no-color >"$dir/cleanup-compose.log" 2>&1 || true
    append_redacted_file "$dir/cleanup-compose.log"
    if ! compose down --volumes --remove-orphans --rmi local --timeout 10 >/dev/null 2>&1; then cleanup_complete=false; code=1; fi
  fi
  if [[ "$proxy_created" == true ]] && ! docker network rm "$proxy" >/dev/null 2>&1; then cleanup_complete=false; code=1; fi
  if ! local_registry_cleanup; then cleanup_complete=false; code=1; fi
  if [[ "$image_built" == true ]] && ! docker image rm -f "$image_tag" >/dev/null 2>&1; then cleanup_complete=false; code=1; fi
  if ! containers=$(docker ps -aq --filter "label=com.docker.compose.project=$project"); then containers=''; cleanup_complete=false; code=1; fi
  if [[ -n "$containers" ]] && ! docker rm -f $containers >/dev/null 2>&1; then cleanup_complete=false; code=1; fi
  if ! volumes=$(docker volume ls -q --filter "label=com.docker.compose.project=$project"); then volumes=''; cleanup_complete=false; code=1; fi
  if [[ -n "$volumes" ]] && ! docker volume rm $volumes >/dev/null 2>&1; then cleanup_complete=false; code=1; fi
  if ! networks=$(docker network ls -q --filter "label=com.docker.compose.project=$project"); then networks=''; cleanup_complete=false; code=1; fi
  if [[ -n "$networks" ]] && ! docker network rm $networks >/dev/null 2>&1; then cleanup_complete=false; code=1; fi
  if ! remaining_containers=$(docker ps -aq --filter "label=com.docker.compose.project=$project"); then remaining_containers=''; cleanup_complete=false; code=1; fi
  if ! remaining_volumes=$(docker volume ls -q --filter "label=com.docker.compose.project=$project"); then remaining_volumes=''; cleanup_complete=false; code=1; fi
  if ! remaining_networks=$(docker network ls -q --filter "label=com.docker.compose.project=$project"); then remaining_networks=''; cleanup_complete=false; code=1; fi
  if [[ -n "$remaining_containers" || -n "$remaining_volumes" || -n "$remaining_networks" ]]; then
    cleanup_complete=false
    code=1
  fi
  if ! rm -rf -- "$dir"; then
    cleanup_complete=false
    code=1
  elif [[ -e "$dir" ]]; then
    cleanup_complete=false
    code=1
  fi
  record "cleanup_complete=$cleanup_complete"
  [[ "$pass" == true && $code -eq 0 ]] && record 'PASS: live production profile startup and durable restart gate'
  printf 'production profile evidence: %s\n' "$evidence"
  exit "$code"
}
trap cleanup EXIT
trap 'fail "unexpected command failure at line $LINENO"' ERR

if [[ "${1:-}" == '--cleanup-pre-compose-test' ]]; then
  [[ $# -eq 2 ]] || { printf '%s\n' 'usage: production-profile-gate.sh --cleanup-pre-compose-test TRACE_FILE' >&2; exit 64; }
  cleanup_test_trace=$2
  printf 'cleanup_dir=%s\n' "$dir" >>"$cleanup_test_trace"
  local_registry_cleanup(){
    printf '%s\n' local_registry_cleanup >>"$cleanup_test_trace"
    [[ "${NEROCD_CLEANUP_TEST_MODE:-success}" != registry-cleanup-failure ]]
  }
  if [[ "${NEROCD_CLEANUP_TEST_MODE:-success}" == compose-down-failure ]]; then
    compose_ready=true
    compose(){
      printf 'compose %s\n' "$*" >>"$cleanup_test_trace"
      [[ "$1" != down ]]
    }
  fi
  exit 1
fi

for x in docker jq rg od; do command -v "$x" >/dev/null || fail "missing dependency $x"; done
docker info >/dev/null || fail 'Docker unavailable'
# Docker Desktop and OrbStack may expose a user-scoped Docker endpoint rather
# than /var/run/docker.sock. docker info above is the portable capability check.

# The gate owns the release candidate image and publishes it through a private,
# loopback-only registry so clean Linux engines get a real repository digest.
docker build -t "$image_tag" "$root" >"$dir/build.log" 2>&1 || fail 'production server image build failed'
image_built=true
image_id=$(docker image inspect --format '{{.Id}}' "$image_tag")
[[ "$image_id" =~ ^sha256:[a-f0-9]{64}$ ]] || fail 'built server image has no canonical digest'
local_registry_publish "$image_tag" "$suffix" || fail 'local registry did not publish canonical server digest'
image_ref=$local_registry_image_ref
[[ "$image_ref" =~ ^127\.0\.0\.1:[1-9][0-9]{0,4}/nerocd-gate-${suffix}@sha256:[a-f0-9]{64}$ ]] || fail 'local registry returned malformed canonical repository digest'
docker image inspect "$image_ref" >/dev/null 2>&1 || fail 'engine did not resolve canonical server digest'
record "server_image=$image_ref"

go build -o "$dir/nerocd" "$root/cmd/nerocd"

owner_role="nerocd_owner_${suffix}"
app_role="nerocd_app_${suffix}"
owner_password="o$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
app_password="a$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
printf 'postgres://%s:%s@postgres:5432/nerocd?sslmode=disable\n' "$owner_role" "$owner_password" >"$dir/database-url"
printf 'postgres://%s:%s@postgres:5432/nerocd?sslmode=disable\n' "$app_role" "$app_password" >"$dir/app-database-url"
printf '%s\n' "$owner_password" >"$dir/postgres-password"
chmod 0400 "$dir/database-url" "$dir/app-database-url" "$dir/postgres-password"
if [[ $(id -u) -eq 0 ]]; then chown 10001:10001 "$dir/database-url"; fi
compose_ready=true

docker network create "$proxy" >/dev/null || fail 'external proxy network create failed'
proxy_created=true
COMPOSE_PROFILES=tools compose config >"$dir/rendered.yaml"
rg -q "image: $image_ref" "$dir/rendered.yaml" || fail 'render did not retain canonical server digest'
! rg -q '^\s*build:' "$dir/rendered.yaml" || fail 'production profile enables build'
! rg -q '^\s*ports:' "$dir/rendered.yaml" || fail 'production profile publishes a host port'
rg -q 'service_completed_successfully' "$dir/rendered.yaml" || fail 'render lacks one-shot dependency barriers'
for service in secret-init pgdata-init backup-data-init postgres migrate server database-tools backup-scheduler; do
  rg -A100 "^  $service:" "$dir/rendered.yaml" | rg -q 'read_only: true' || fail "$service lacks read-only root filesystem"
  rg -A100 "^  $service:" "$dir/rendered.yaml" | rg -q 'cap_drop:' || fail "$service lacks dropped capabilities"
  rg -A100 "^  $service:" "$dir/rendered.yaml" | rg -q 'logging:' || fail "$service lacks bounded logs"
done
tool_render=$(rg -A100 '^  database-tools:' "$dir/rendered.yaml")
rg -q 'profiles:' <<<"$tool_render" || fail 'database tools are not an explicit profile'
rg -q '/runtime-owner' <<<"$tool_render" || fail 'database tools lack owner secret mount'
! rg -q '/runtime-app' <<<"$tool_render" || fail 'database tools receive application secret mount'
! rg -q 'proxy' <<<"$tool_render" || fail 'database tools receive proxy network'
scheduler_render=$(rg -A100 '^  backup-scheduler:' "$dir/rendered.yaml")
rg -q '/runtime-owner' <<<"$scheduler_render" || fail 'backup scheduler lacks owner secret mount'
rg -q '/backups' <<<"$scheduler_render" || fail 'backup scheduler lacks private backup volume'
! rg -q '/runtime-app' <<<"$scheduler_render" || fail 'backup scheduler receives application secret mount'
! rg -q 'proxy' <<<"$scheduler_render" || fail 'backup scheduler receives proxy network'
rg -q -- '--enabled=false' <<<"$scheduler_render" || fail 'backup scheduler is not opt-in by default'
rg -A100 '^  server:' "$dir/rendered.yaml" | rg -q 'stop_grace_period: 35s' || fail 'production server stop grace is not longer than lifecycle grace'
for key in NEROCD_IMAGE NEROCD_PROXY_NETWORK NEROCD_PUBLIC_ORIGIN NEROCD_OWNER_DATABASE_USER NEROCD_APP_DATABASE_USER NEROCD_DATABASE_URL_SECRET NEROCD_APP_DATABASE_URL_SECRET NEROCD_POSTGRES_PASSWORD_SECRET; do
  rg -q "^${key}=" "$root/.env.production.example" || fail "production env template lacks $key"
done
record 'production_env_template_render_inputs_complete=true'

# The app's own validation is also the gate for a supplied image reference.
if NEROCD_MODE=production NEROCD_IMAGE_REF='example.invalid/nerocd:latest' "$dir/nerocd" doctor >"$dir/tag.out" 2>"$dir/tag.err"; then fail 'doctor accepted a mutable image tag'; fi
if NEROCD_MODE=production NEROCD_IMAGE_REF='example.invalid/nerocd:latest@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' "$dir/nerocd" doctor >"$dir/tagdigest.out" 2>"$dir/tagdigest.err"; then fail 'doctor accepted tag@digest'; fi
if NEROCD_MODE=production NEROCD_IMAGE_REF="$image_ref" NEROCD_PUBLIC_ORIGIN='https://nerocd.example.invalid' NEROCD_OWNER_DATABASE_USER="$owner_role" NEROCD_APP_DATABASE_USER="$app_role" NEROCD_OWNER_DATABASE_URL_FILE="$dir/database-url" NEROCD_APP_DATABASE_URL_FILE="$dir/app-database-url" "$dir/nerocd" doctor >"$dir/doctor.out" 2>"$dir/doctor.err"; then :; else fail 'doctor rejected distinct owner/app credentials'; fi
if NEROCD_MODE=production NEROCD_IMAGE_REF="$image_ref" NEROCD_PUBLIC_ORIGIN='https://nerocd.example.invalid' NEROCD_OWNER_DATABASE_USER="$owner_role" NEROCD_APP_DATABASE_USER="$app_role" NEROCD_OWNER_DATABASE_URL_FILE="$dir/database-url" NEROCD_APP_DATABASE_URL_FILE="$dir/database-url" "$dir/nerocd" doctor >"$dir/equal.out" 2>"$dir/equal.err"; then fail 'doctor accepted equal owner/app credentials'; fi
! rg -Fq "$owner_password" "$dir/tag.err" "$dir/tagdigest.err" || fail 'doctor disclosed owner secret in invalid-image error'
! rg -Fq "$app_password" "$dir/tag.err" "$dir/tagdigest.err" || fail 'doctor disclosed app secret in invalid-image error'
! rg -Fq "$owner_password" "$dir/doctor.err" "$dir/equal.err" || fail 'doctor disclosed owner secret while validating credential files'
! rg -Fq "$app_password" "$dir/doctor.err" "$dir/equal.err" || fail 'doctor disclosed app secret while validating credential files'

if ! compose up -d --wait postgres server backup-scheduler >"$dir/up.log" 2>&1; then
  emit_startup_diagnostics
  fail 'production stack failed to reach healthy server'
fi
for i in {1..30}; do
  compose run --rm --no-deps probe >"$dir/probe.log" 2>&1 && break
  sleep 1
done
compose run --rm --no-deps probe >"$dir/probe-final.log" 2>&1 || fail 'proxy-network probe could not reach server readiness'
[[ -z $(docker port "$(compose ps -q postgres)") ]] || fail 'PostgreSQL has an external host port'
server_id=$(compose ps -q server); postgres_id=$(compose ps -q postgres); scheduler_id=$(compose ps -q backup-scheduler)
[[ -n "$server_id" && -n "$postgres_id" && -n "$scheduler_id" ]] || fail 'required containers missing'
[[ -z $(docker port "$server_id") ]] || fail 'server/metrics has an external host port'
[[ $(docker inspect -f '{{.Config.User}}' "$server_id") == nerocd ]] || fail 'server is not nonroot'
[[ $(docker inspect -f '{{.Config.User}}' "$scheduler_id") == '10001:10001' ]] || fail 'backup scheduler is not explicit nonroot UID'
[[ $(docker inspect -f '{{.Config.User}}' "$postgres_id") == '70:70' ]] || fail 'postgres is not nonroot'
for id in "$server_id" "$postgres_id" "$scheduler_id"; do
  rootfs=$(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' "$id")
  [[ "$rootfs" == true ]] || fail "container $id rootfs is writable value=$rootfs"
  [[ $(docker inspect -f '{{json .HostConfig.CapDrop}}' "$id") == *'ALL'* ]] || fail "container $id retains capabilities"
  [[ $(docker inspect -f '{{.HostConfig.PidsLimit}}' "$id") != 0 ]] || fail "container $id has no PID bound"
done

# The server mounts only the app file.  The completed one-shot containers are
# still inspectable, so their mount metadata proves migration sees only owner
# and role-init is the sole short-lived service with both inputs.
server_mounts=$(docker inspect -f '{{range .Mounts}}{{println .Destination}}{{end}}' "$server_id")
rg -qx '/runtime-app' <<<"$server_mounts" || fail 'server lacks app-only secret mount'
! rg -q '/runtime-owner' <<<"$server_mounts" || fail 'server can mount owner secret path'
docker exec "$server_id" sh -ec 'test -r /runtime-app/app_database_url && test ! -e /runtime-owner/owner_database_url' || fail 'server filesystem exposes owner credential'
scheduler_mounts=$(docker inspect -f '{{range .Mounts}}{{println .Destination}}{{end}}' "$scheduler_id")
rg -qx '/runtime-owner' <<<"$scheduler_mounts" || fail 'scheduler lacks owner-only secret mount'
rg -qx '/backups' <<<"$scheduler_mounts" || fail 'scheduler lacks backup volume mount'
! rg -q '/runtime-app' <<<"$scheduler_mounts" || fail 'scheduler can mount app secret path'
docker exec "$scheduler_id" sh -ec 'test -r /runtime-owner/owner_database_url && test ! -e /runtime-app/app_database_url && test -d /backups' || fail 'scheduler filesystem does not enforce owner-only inputs'
migrator_id=$(compose ps -aq migrate); role_init_id=$(compose ps -aq role-init)
[[ -n "$migrator_id" && -n "$role_init_id" ]] || fail 'secret chain one-shot containers missing'
migrator_mounts=$(docker inspect -f '{{range .Mounts}}{{println .Destination}}{{end}}' "$migrator_id")
role_init_mounts=$(docker inspect -f '{{range .Mounts}}{{println .Destination}}{{end}}' "$role_init_id")
rg -qx '/runtime-owner' <<<"$migrator_mounts" || fail 'migrator lacks owner-only secret mount'
! rg -q '/runtime-app' <<<"$migrator_mounts" || fail 'migrator can mount app secret path'
rg -qx '/runtime-owner' <<<"$role_init_mounts" || fail 'role-init lacks owner secret mount'
rg -qx '/runtime-app' <<<"$role_init_mounts" || fail 'role-init lacks app secret mount'
probe_id=$(compose run -d --no-deps --entrypoint /bin/sh probe -ec 'sleep 30')
probe_mounts=$(docker inspect -f '{{range .Mounts}}{{println .Destination}}{{end}}' "$probe_id")
probe_env=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$probe_id")
! rg -q 'runtime-owner|runtime-app' <<<"$probe_mounts" || fail 'HTTP probe unnecessarily receives database credential mount'
! rg -q '^NEROCD_DATABASE_URL_FILE=' <<<"$probe_env" || fail 'HTTP probe unnecessarily receives database credential environment'
docker rm -f "$probe_id" >/dev/null || fail 'could not remove credential-free probe inspection container'
record 'credential_mount_separation=server_app_only,migrator_owner_only,scheduler_owner_backup_only,role_init_ephemeral_both,probe_none'

migrations=$(find "$root/db/migrations" -maxdepth 1 -type f -name '*.sql' | wc -l | tr -d ' ')
applied=$(owner_psql -Atc 'select count(*) from schema_migrations')
[[ "$applied" == "$migrations" ]] || fail "migration count=$applied expected=$migrations"
users=$(owner_psql -Atc 'select count(*) from users')
projects=$(owner_psql -Atc 'select count(*) from projects')
runners=$(owner_psql -Atc 'select count(*) from runners')
[[ "$users" == 0 && "$projects" == 0 && "$runners" == 0 ]] || fail "production migration introduced seed rows users=$users projects=$projects runners=$runners"
record "initial_ready=true migrations=$applied users=$users projects=$projects runners=$runners development_seed_absent=true"

# Role-init gives the server credential only application DML.  Exercise the
# same tables/sequences/functions used by the domain store, then prove that it
# cannot acquire migration-owner powers or rewrite the immutable audit trail.
app_psql -c "INSERT INTO audit_events (id, actor_id, action, target_id, metadata) VALUES ('production-role-audit', 'production-role-app', 'role_probe', 'production-role-target', '{}'::jsonb)" >"$dir/app-audit-insert.log"
app_psql -Atc "SELECT action FROM audit_events WHERE id='production-role-audit'" | rg -qx 'role_probe' || fail 'application role audit insert/read failed'
require_cross_auth_denied owner_role_with_app_password "$app_password" "$owner_role"
require_cross_auth_denied app_role_with_owner_password "$owner_password" "$app_role"
require_app_denied create_table 'CREATE TABLE app_must_not_create (id integer)'
require_app_denied alter_table 'ALTER TABLE audit_events ADD COLUMN app_must_not_add integer'
require_app_denied drop_table 'DROP TABLE audit_events'
require_app_denied update_audit "UPDATE audit_events SET action='tampered' WHERE id='production-role-audit'"
require_app_denied delete_audit "DELETE FROM audit_events WHERE id='production-role-audit'"
owner_psql -c "CREATE TABLE production_role_future (id integer PRIMARY KEY, value text NOT NULL); CREATE SEQUENCE production_role_future_seq; CREATE FUNCTION production_role_future_fn() RETURNS integer LANGUAGE sql AS 'SELECT 42';" >"$dir/owner-default-privileges.log"
app_psql -c "INSERT INTO production_role_future (id, value) VALUES (1, 'application-write')" >"$dir/app-domain-write.log"
app_psql -Atc "SELECT value FROM production_role_future WHERE id=1" | rg -qx 'application-write' || fail 'application role domain read/write failed'
app_psql -Atc "SELECT nextval('production_role_future_seq'), production_role_future_fn()" | rg -qx '1\|42' || fail 'application role default sequence/function privileges failed'
require_app_denied alter_future_table 'ALTER TABLE production_role_future ADD COLUMN forbidden integer'
record 'app_role_domain_write_read=true audit_insert_only=true future_table_sequence_function_privileges=true ddl_denied=true'

# Readiness is stricter than liveness even after the server has started.  The
# temporary compatibility replacement happens only in this disposable project;
# the exact owner definition is restored before the durability assertions.
owner_psql -Atc "SELECT pg_get_functiondef('nerocd_repository_policy_schema_compatible()'::regprocedure)" >"$dir/repository-policy-compatible.sql"
[[ -s "$dir/repository-policy-compatible.sql" ]] || fail 'could not capture compatibility function for restoration'
owner_psql -c "CREATE OR REPLACE FUNCTION nerocd_repository_policy_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS 'SELECT false'" >"$dir/readiness-incompatible.log"
compose run --rm --no-deps --entrypoint nerocd probe health --addr http://server:8080 >"$dir/liveness-incompatible.log" 2>&1 || fail 'liveness failed while schema was intentionally incompatible'
if compose run --rm --no-deps --entrypoint nerocd probe ready --addr http://server:8080 >"$dir/readiness-incompatible.out" 2>&1; then
  fail 'readiness accepted intentionally incompatible schema'
fi
rg -q '503' "$dir/readiness-incompatible.out" || fail 'incompatible readiness did not return 503'
cat "$dir/repository-policy-compatible.sql" | owner_psql >"$dir/readiness-restore.log"
compose run --rm --no-deps probe >"$dir/readiness-recovered.log" 2>&1 || fail 'readiness did not recover after compatibility restoration'
record 'liveness_200_ready_503_incompatible=true readiness_200_recovered=true'

# Exercise the actual server process rather than a test-only endpoint. SIGTERM
# must be normalized to exit 0, Docker must restart the service, and the normal
# proxy-network readiness request must recover against durable state.
slow_probe=$(compose run -d --no-deps --entrypoint /bin/sh probe -ec 'body='\''{'\''; { printf '\''POST /api/v1/sessions HTTP/1.1\r\nHost: server\r\nContent-Type: application/json\r\nContent-Length: %s\r\n\r\n'\'' "${#body}"; sleep 1; printf '\''%s'\'' "$body"; } | nc -w 5 server 8080')
sleep 0.2
docker kill --signal TERM "$server_id" >/dev/null || fail 'could not send SIGTERM to production server'
for i in {1..30}; do
  [[ $(docker inspect -f '{{.State.Status}}' "$server_id") == exited ]] && break
  sleep 1
done
[[ $(docker inspect -f '{{.State.Status}}' "$server_id") == exited ]] || fail 'server did not exit after SIGTERM'
[[ $(docker inspect -f '{{.State.ExitCode}}' "$server_id") == 0 ]] || fail 'server SIGTERM did not exit cleanly'
slow_exit=$(docker wait "$slow_probe")
[[ "$slow_exit" == 0 ]] || fail 'in-flight normal request did not complete during graceful drain'
docker logs "$slow_probe" >"$dir/sigterm-inflight-response.log" 2>&1
rg -q 'HTTP/1.1 400' "$dir/sigterm-inflight-response.log" || fail 'in-flight normal request did not receive its application response'
docker rm "$slow_probe" >/dev/null || fail 'could not remove in-flight request probe'
docker start "$server_id" >/dev/null || fail 'could not restart server after clean SIGTERM exit'
for i in {1..30}; do [[ $(docker inspect -f '{{.State.Status}}' "$server_id") == running ]] && break; sleep 1; done
[[ $(docker inspect -f '{{.State.Status}}' "$server_id") == running ]] || fail 'server did not start after clean SIGTERM exit'
for i in {1..30}; do compose run --rm --no-deps probe >"$dir/sigterm-probe.log" 2>&1 && break; sleep 1; done
compose run --rm --no-deps probe >"$dir/sigterm-probe-final.log" 2>&1 || fail 'server was not ready after SIGTERM drain/restart'
sigterm_applied=$(owner_psql -Atc 'select count(*) from schema_migrations')
[[ "$sigterm_applied" == "$migrations" ]] || fail 'migration state changed across SIGTERM restart'
record 'sigterm_inflight_normal_request_completed=true sigterm_clean_exit=true sigterm_restart_ready=true state_durable_after_sigterm=true'

# Restart without deleting named volumes. The app must remain durable and the
# one-shot migration remains completed rather than being re-applied.
compose restart postgres >"$dir/restart-postgres.log" 2>&1 || fail 'postgres restart failed'
for i in {1..30}; do [[ $(docker inspect -f '{{.State.Health.Status}}' "$(compose ps -q postgres)") == healthy ]] && break; sleep 1; done
[[ $(docker inspect -f '{{.State.Health.Status}}' "$(compose ps -q postgres)") == healthy ]] || fail 'postgres did not become healthy after restart'
compose restart server >"$dir/restart-server.log" 2>&1 || fail 'server restart failed'
for i in {1..30}; do compose run --rm --no-deps probe >"$dir/restart-probe.log" 2>&1 && break; sleep 1; done
compose run --rm --no-deps probe >"$dir/restart-probe-final.log" 2>&1 || fail 'server did not become ready after durable restart'
applied_after=$(owner_psql -Atc 'select count(*) from schema_migrations')
[[ "$applied_after" == "$migrations" ]] || fail 'migration state changed across restart'
[[ -n "$migrator_id" && $(docker inspect -f '{{.State.ExitCode}}' "$migrator_id") == 0 ]] || fail 'migrator did not complete exactly once'

compose logs --no-color >"$dir/logs.txt"
docker inspect "$server_id" "$postgres_id" >"$dir/inspect.json"
! rg -Fq "$owner_password" "$dir/logs.txt" "$dir/inspect.json" "$dir"/*.log || fail 'owner secret disclosed in production evidence'
! rg -Fq "$app_password" "$dir/logs.txt" "$dir/inspect.json" "$dir"/*.log || fail 'app secret disclosed in production evidence'
record 'restart_ready=true migration_state_durable=true no_host_db_or_metrics_ports=true distinct_owner_app_credentials=true no_secret_disclosure=true'
pass=true
