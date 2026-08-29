#!/usr/bin/env bash
# AC16a live gate: backup a disposable source with the shipped offline tool and
# restore it only into a separate, initially empty PostgreSQL target.
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
evidence=/tmp/nerocd-backup-restore.txt
dir=$(mktemp -d /tmp/nerocd-backup-restore.XXXXXXXX)
suffix=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')
network="nerocd-backup-$suffix"
source="nerocd-backup-source-$suffix"
target="nerocd-backup-target-$suffix"
source_server="nerocd-backup-server-$suffix"
target_server="nerocd-backup-restored-server-$suffix"
tls_proxy="nerocd-backup-tls-$suffix"
image_tag="nerocd-backup-tool:$suffix"
pass=false
: >"$evidence"
record(){ printf '%s\n' "$*" >>"$evidence"; }
fail(){ trap - ERR; record "FAIL: $*"; printf 'backup-restore: %s\n' "$*" >&2; exit 1; }
cleanup(){
  local code=$?
  trap - ERR
  set +e
  docker rm -f "$tls_proxy" "$source_server" "$target_server" "$source" "$target" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  docker image rm -f "$image_tag" >/dev/null 2>&1 || true
  rm -rf -- "$dir"
  [[ "$pass" == true && $code -eq 0 ]] && record 'PASS: source-to-empty-target backup/restore gate'
  printf 'backup/restore evidence: %s\n' "$evidence"
  exit "$code"
}
trap cleanup EXIT
trap 'fail "unexpected command failure at line $LINENO"' ERR

for x in docker jq od rg bun openssl; do command -v "$x" >/dev/null || fail "missing dependency $x"; done
docker info >/dev/null || fail 'Docker unavailable'

docker build -t "$image_tag" "$root" >"$dir/build.log" 2>&1 || fail 'backup tool image build failed'
image_ref=$(docker image inspect --format '{{index .RepoDigests 0}}' "$image_tag")
[[ "$image_ref" =~ ^[a-z0-9][a-z0-9._/-]*@sha256:[a-f0-9]{64}$ ]] || fail 'local build has no canonical image digest reference'
docker pull caddy:2.10.2-alpine >"$dir/tls-proxy-pull.log" 2>&1 || fail 'TLS proxy image pull failed'
tls_proxy_ref=$(docker image inspect --format '{{index .RepoDigests 0}}' caddy:2.10.2-alpine)
[[ "$tls_proxy_ref" =~ ^[a-z0-9][a-z0-9._/-]*@sha256:[a-f0-9]{64}$ ]] || fail 'TLS proxy image has no canonical digest reference'
docker network create "$network" >/dev/null
# Use one short-lived TLS host port for the browser's real same-origin session.
# It is selected before the server starts because production validates the
# public origin and rejects a mismatched browser Origin header.
tls_host_port=$((20000 + (RANDOM % 20000)))
base="https://localhost:${tls_host_port}"

owner="owner_$suffix"
app="app_$suffix"
password="p$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
app_password="a$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
bootstrap_password="b$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
printf 'postgres://%s:%s@%s:5432/nerocd?sslmode=disable\n' "$owner" "$password" "$source" >"$dir/source-url"
printf 'postgres://%s:%s@%s:5432/nerocd?sslmode=disable\n' "$owner" "$password" "$target" >"$dir/target-url"
printf 'postgres://%s:%s@%s:5432/nerocd?sslmode=disable\n' "$app" "$app_password" "$source" >"$dir/source-app-url"
printf 'postgres://%s:%s@%s:5432/nerocd?sslmode=disable\n' "$app" "$app_password" "$target" >"$dir/target-app-url"
printf '%s\n' "$bootstrap_password" >"$dir/bootstrap-password"
chmod 0400 "$dir/source-url" "$dir/target-url" "$dir/source-app-url" "$dir/target-app-url" "$dir/bootstrap-password"
mkdir -m 0700 "$dir/backups" "$dir/runner-files" "$dir/wrong-runner-files"
for runner_file in runner.json template.json template-workflow.json run-root.json run-nested.json; do
  printf 'fixture runner credential must not be copied\n' >"$dir/runner-files/$runner_file"
  chmod 0400 "$dir/runner-files/$runner_file"
done

postgres_image='postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94'
for container in "$source" "$target"; do
  ready=false
  for start_attempt in 1 2; do
    docker rm -f "$container" >/dev/null 2>&1 || true
    docker run -d --name "$container" --network "$network" \
      -e POSTGRES_DB=nerocd -e POSTGRES_USER="$owner" -e POSTGRES_PASSWORD="$password" \
      "$postgres_image" >/dev/null
    for _ in {1..40}; do docker exec "$container" pg_isready -U "$owner" -d nerocd >/dev/null 2>&1 && { ready=true; break; }; sleep 1; done
    [[ "$ready" == true ]] && break
    docker logs "$container" 2>&1 | sed -E 's/(password|POSTGRES_PASSWORD)=[^[:space:]]+/\1=<redacted>/Ig' | tail -n 40 >>"$evidence" || true
  done
  [[ "$ready" == true ]] || fail "PostgreSQL did not become ready after bounded retry: $container"
done

run_tool(){
  local url_file=$1
  shift
  docker run --rm --user "$(id -u):$(id -g)" --network "$network" \
    -e NEROCD_MODE=production -e NEROCD_IMAGE_REF="$image_ref" \
    -e NEROCD_DATABASE_CREDENTIAL=owner -e NEROCD_OWNER_DATABASE_USER="$owner" \
    -e NEROCD_DATABASE_URL_FILE=/runtime/owner-url \
    -v "$url_file:/runtime/owner-url:ro" "$@"
}
run_tool "$dir/source-url" "$image_ref" migrate --seed=false >"$dir/migrate.log" 2>&1 || fail 'source migration failed'

# Establish production-shaped application credentials, one-time bootstrap, and
# authenticated public API state. SQL below is intentionally reserved for the
# narrow active-lease invalidation assertion after API-created durable state.
docker run --rm --user "$(id -u):$(id -g)" --network "$network" \
  -e NEROCD_OWNER_DATABASE_USER="$owner" -e NEROCD_APP_DATABASE_USER="$app" \
  -v "$dir/source-url:/runtime/owner-url:ro" -v "$dir/source-app-url:/runtime/app-url:ro" \
  "$image_ref" provision-app-role --owner-file /runtime/owner-url --app-file /runtime/app-url >"$dir/provision-app.log" 2>&1 || fail 'source application role provision failed'
run_app(){
  docker run --rm --user "$(id -u):$(id -g)" --network "$network" \
    -e NEROCD_MODE=production -e NEROCD_IMAGE_REF="$image_ref" -e NEROCD_DATABASE_CREDENTIAL=app -e NEROCD_APP_DATABASE_USER="$app" -e NEROCD_PUBLIC_ORIGIN=https://backup.example.invalid \
    -e NEROCD_DATABASE_URL_FILE=/runtime/app-url -v "$dir/source-app-url:/runtime/app-url:ro" "$@"
}
# Start the real production server before bootstrap. The browser can see only
# the intentionally tiny public lifecycle bit and the CLI-only instruction.
docker run -d --name "$source_server" --user "$(id -u):$(id -g)" --network "$network" -p 127.0.0.1::8080 \
  -e NEROCD_MODE=production -e NEROCD_IMAGE_REF="$image_ref" -e NEROCD_DATABASE_CREDENTIAL=app -e NEROCD_APP_DATABASE_USER="$app" \
  -e NEROCD_DATABASE_URL_FILE=/runtime/app-url -e NEROCD_PUBLIC_ORIGIN="$base" \
  -v "$dir/source-app-url:/runtime/app-url:ro" "$image_ref" server --addr :8080 >/dev/null
source_port=$(docker port "$source_server" 8080/tcp | tail -1)
for _ in {1..30}; do curl -fsS --max-time 3 "http://localhost:${source_port##*:}/api/v1/health" >/dev/null 2>&1 && break; sleep 1; done
curl -fsS --max-time 3 "http://localhost:${source_port##*:}/api/v1/health" | jq -e '.status == "ok"' >/dev/null || fail 'pre-bootstrap production server was not live'
# Browser authentication is intentionally exercised through TLS: production
# does not permit an insecure session-cookie override. The local CA is scoped
# to this disposable gate and Chromium still enforces the Secure cookie.
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=localhost' -keyout "$dir/tls.key" -out "$dir/tls.crt" >/dev/null 2>&1 || fail 'local TLS certificate generation failed'
chmod 0600 "$dir/tls.key" "$dir/tls.crt"
cat >"$dir/Caddyfile" <<EOF
https://:8443 {
  tls /etc/caddy/tls/tls.crt /etc/caddy/tls/tls.key
  reverse_proxy $source_server:8080
}
EOF
docker run -d --name "$tls_proxy" --network "$network" -p "127.0.0.1:${tls_host_port}:8443" -v "$dir/Caddyfile:/etc/caddy/Caddyfile:ro" -v "$dir:/etc/caddy/tls:ro" "$tls_proxy_ref" >/dev/null
for _ in {1..30}; do curl -kfsS --max-time 3 "$base/api/v1/health" >/dev/null 2>&1 && break; sleep 1; done
if ! curl -kfsS --max-time 3 "$base/api/v1/health" | jq -e '.status == "ok"' >/dev/null; then docker logs "$tls_proxy" 2>&1 | tail -n 40 >>"$evidence" || true; fail 'TLS production browser seam was not live'; fi
admin_email="backup-${suffix}@example.invalid"
umask 077; printf '%s\n%s\n' "$admin_email" "$bootstrap_password" >"$dir/browser.credentials"; chmod 0600 "$dir/browser.credentials"
cd "$root/web/app" && bun "$root/acceptance/system-operations/browser.mjs" "$base" "$dir/browser.credentials" required "$dir/browser-required.json" >"$dir/browser-required.log" 2>&1 || { tail -n 40 "$dir/browser-required.log" >>"$evidence"; fail 'browser did not render CLI-only pre-bootstrap guidance'; }
run_app -v "$dir/bootstrap-password:/runtime/bootstrap-password:ro" "$image_ref" bootstrap-admin --email "$admin_email" --name 'Backup Admin' --password-file /runtime/bootstrap-password >"$dir/bootstrap.log" 2>&1 || fail 'supported source bootstrap failed'
api_post(){
  local path=$1 body=$2 token=${3:-}
  if [[ -z "$token" ]]; then
    docker run --rm --network "$network" alpine:3.22.2 wget -qO- --header 'Content-Type: application/json' --post-data "$body" "http://$source_server:8080$path"
  else
    docker run --rm --network "$network" alpine:3.22.2 wget -qO- --header 'Content-Type: application/json' --header "Authorization: Bearer $token" --post-data "$body" "http://$source_server:8080$path"
  fi
}
api_get(){
  local path=$1 token=$2
  docker run --rm --network "$network" alpine:3.22.2 wget -qO- --header "Authorization: Bearer $token" "http://$source_server:8080$path"
}
for _ in {1..30}; do docker run --rm --network "$network" alpine:3.22.2 wget -qO- "http://$source_server:8080/api/v1/ready" >/dev/null 2>&1 && break; sleep 1; done
docker run --rm --network "$network" alpine:3.22.2 wget -qO- "http://$source_server:8080/api/v1/ready" | jq -e '.status == "ready"' >/dev/null || fail 'source server was not ready'
session=$(api_post /api/v1/sessions "{\"email\":\"$admin_email\",\"password\":\"$bootstrap_password\"}") || fail 'source API login failed'
token=$(jq -r '.token' <<<"$session")
session_id=$(jq -r '.session.id' <<<"$session")
[[ "$token" != null && -n "$token" ]] || fail 'source API did not return session token'
runner=$(api_post /api/v1/runners/register '{"name":"backup-runner","tags":[],"capabilities":["compose-deploy"]}' "$token") || fail 'source runner API registration failed'
runner_id=$(jq -r '.runner.id' <<<"$runner")
project=$(api_post /api/v1/projects '{"name":"Backup gate","description":"production-shaped backup fixture"}' "$token") || fail 'source project API create failed'
project_id=$(jq -r '.id' <<<"$project")
repository=$(api_post /api/v1/repositories "{\"project_id\":\"$project_id\",\"name\":\"fixture\",\"url\":\"https://example.invalid/fixture.git\",\"provider\":\"git\",\"default_ref\":\"main\"}" "$token") || fail 'source repository API create failed'
repository_id=$(jq -r '.id' <<<"$repository")
service=$(api_post /api/v1/services "{\"project_id\":\"$project_id\",\"name\":\"fixture\",\"repository_id\":\"$repository_id\",\"compose_path\":\"compose.yaml\",\"profiles\":[]}" "$token") || fail 'source service API create failed'
service_id=$(jq -r '.id' <<<"$service")
environment=$(api_post /api/v1/environments "{\"service_id\":\"$service_id\",\"name\":\"prod\",\"runner_selector\":[],\"compose_project\":\"backup-$suffix\",\"health_policy\":{},\"confirmation_required\":false,\"timeout_seconds\":30,\"secret_bindings\":[{\"name\":\"FIXTURE_SECRET\",\"provider\":\"runner_file\",\"reference\":\"runner.json\",\"target\":\"env:FIXTURE_SECRET\",\"required\":true,\"version\":\"v1\"}],\"rollback_safe\":true}" "$token") || fail 'source environment API create failed'
environment_id=$(jq -r '.id' <<<"$environment")
revision=$(api_post /api/v1/revisions "{\"service_id\":\"$service_id\",\"requested_ref\":\"main\"}" "$token") || fail 'source revision API create failed'
revision_id=$(jq -r '.id' <<<"$revision")
deployment=$(api_post /api/v1/deployments "{\"environment_id\":\"$environment_id\",\"desired_revision_id\":\"$revision_id\",\"idempotency_key\":\"backup-fixture\"}" "$token") || fail 'source deployment API create failed'
deployment_id=$(jq -r '.id' <<<"$deployment")
# These two supported API requests cover both persisted RunSpec carriers. The
# template has a runner_file binding absent from the environment, and the
# generic run has a recursively nested workflow step binding. The collector
# must inventory all of them, not just environment bindings.
template=$(api_post /api/v1/templates "{\"project_id\":\"$project_id\",\"name\":\"backup inventory template\",\"kind\":\"shell\",\"run_spec\":{\"type\":\"shell\",\"inputs\":{},\"process\":{\"command\":[\"true\"]},\"secrets\":[{\"name\":\"TEMPLATE_SECRET\",\"provider\":\"runner_file\",\"reference\":\"template.json\",\"target\":\"env:TEMPLATE_SECRET\",\"required\":true,\"version\":\"v1\"}]},\"workflow\":{\"steps\":[{\"id\":\"template-nested\",\"name\":\"template nested\",\"run_spec\":{\"type\":\"shell\",\"inputs\":{},\"process\":{\"command\":[\"true\"]},\"secrets\":[{\"name\":\"TEMPLATE_WORKFLOW_SECRET\",\"provider\":\"runner_file\",\"reference\":\"template-workflow.json\",\"target\":\"env:TEMPLATE_WORKFLOW_SECRET\",\"required\":true,\"version\":\"v2\"}]}}]},\"runner_tags\":[],\"requires_ack\":false}" "$token") || fail 'source template API create failed'
template_id=$(jq -r '.id' <<<"$template")
generic_run=$(api_post /api/v1/runs "{\"project_id\":\"$project_id\",\"run_spec\":{\"type\":\"shell\",\"inputs\":{},\"process\":{\"command\":[\"true\"]},\"secrets\":[{\"name\":\"RUN_ROOT_SECRET\",\"provider\":\"runner_file\",\"reference\":\"run-root.json\",\"target\":\"env:RUN_ROOT_SECRET\",\"required\":true,\"version\":\"v1\"}]},\"workflow\":{\"steps\":[{\"id\":\"run-nested\",\"name\":\"run nested\",\"run_spec\":{\"type\":\"shell\",\"inputs\":{},\"process\":{\"command\":[\"true\"]},\"secrets\":[{\"name\":\"RUN_NESTED_SECRET\",\"provider\":\"runner_file\",\"reference\":\"run-nested.json\",\"target\":\"env:RUN_NESTED_SECRET\",\"required\":true,\"version\":\"v3\"}]}}]},\"runner_tags\":[],\"requires_ack\":false}" "$token") || fail 'source generic workflow run API create failed'
generic_run_id=$(jq -r '.id' <<<"$generic_run")
[[ -n "$project_id" && -n "$service_id" && -n "$environment_id" && -n "$revision_id" && -n "$deployment_id" && -n "$runner_id" && -n "$template_id" && -n "$generic_run_id" ]] || fail 'source API fixture identifiers missing'
# The only direct SQL fixture is the transient runner authority whose restore
# invalidation is the security property under test. All durable domain objects
# above were created through the supported bootstrap/authenticated API.
user_id=$(docker exec "$source" psql -At -U "$owner" -d nerocd -c "SELECT id FROM users WHERE email='$admin_email'")
docker exec -i "$source" psql -v ON_ERROR_STOP=1 -U "$owner" -d nerocd >"$dir/transient-authority.log" <<SQL
INSERT INTO task_runs (id, project_id, status, requested_by) VALUES ('backup-run', '$project_id', 'running', '$user_id');
INSERT INTO run_leases (id, run_id, runner_id, status, expires_at, attempt, fence) VALUES ('backup-lease', 'backup-run', '$runner_id', 'active', clock_timestamp() + interval '1 day', 1, 'backup-fence');
SQL
cd "$root/web/app" && bun "$root/acceptance/system-operations/browser.mjs" "$base" "$dir/browser.credentials" none "$dir/browser-none.json" >"$dir/browser-none.log" 2>&1 || { tail -n 40 "$dir/browser-none.log" >>"$evidence"; fail 'browser could not authenticate and render initial operations status'; }
record 'system_operations_browser_prebootstrap_cli_guidance=true cli_bootstrap=true admin_login=true ready=true backup_initially_none=true'
record 'source_server_ready=true supported_bootstrap_and_authenticated_api_fixture=true'

backup(){
  run_tool "$dir/source-url" -v "$dir/backups:/backups" -v "$dir/runner-files:/runner-files:ro" "$image_ref" backup --output-dir /backups --runner-file-root /runner-files
}
if ! backup >"$dir/backup.log" 2>&1; then
  record "backup_error=$(tr '\n' ' ' <"$dir/backup.log")"
  fail 'backup command failed'
fi
backup_name=$(basename "$(tail -n1 "$dir/backup.log")")
backup_dir="$dir/backups/$backup_name"
[[ -f "$backup_dir/database.dump" && -f "$backup_dir/manifest.json" ]] || fail 'atomic backup files missing'
mkdir -m 0700 "$dir/off-host"
# Export uses an operator-mounted destination rather than a cloud SDK. The
# source is verified before copy and again before its single atomic publish.
run_tool "$dir/source-url" -v "$backup_dir:/restore:ro" -v "$dir/off-host:/off-host" "$image_ref" backup-export --input-dir /restore --output-dir /off-host >"$dir/export.log" 2>&1 || fail 'verified off-host backup export failed'
export_name=$(basename "$(tail -n1 "$dir/export.log")")
export_dir="$dir/off-host/$export_name"
[[ -f "$export_dir/database.dump" && -f "$export_dir/manifest.json" ]] || fail 'off-host export files missing'
run_tool "$dir/source-url" -v "$export_dir:/restore:ro" "$image_ref" backup-verify --input-dir /restore >"$dir/export-verify.log" 2>&1 || fail 'off-host backup verification failed'
docker run --rm -v "$export_dir:/backup" alpine:3.22.2 sh -ec 'printf tamper >> /backup/database.dump; chmod 600 /backup/database.dump'
if run_tool "$dir/source-url" -v "$export_dir:/restore:ro" "$image_ref" backup-verify --input-dir /restore >"$dir/export-tamper.log" 2>&1; then fail 'backup verification accepted tampered off-host export'; fi
record 'off_host_export_atomic=true exported_archive_verified=true exported_archive_tamper_rejected=true'
jq -e '.version == 1 and .files[0].path == "database.dump" and (.files[0].sha256|length == 64) and .runner_file_inventory == [{provider:"runner_file",reference:"run-nested.json",version:"v3"},{provider:"runner_file",reference:"run-root.json",version:"v1"},{provider:"runner_file",reference:"runner.json",version:"v1"},{provider:"runner_file",reference:"template-workflow.json",version:"v2"},{provider:"runner_file",reference:"template.json",version:"v1"}]' "$backup_dir/manifest.json" >/dev/null || fail 'backup manifest is incomplete or omitted persisted run specifications'
! rg -Fq 'fixture runner credential' "$backup_dir/manifest.json" "$dir/backup.log" || fail 'runner file content leaked into manifest or tool output'
if docker run --rm --network "$network" alpine:3.22.2 wget -qO- "http://$source_server:8080/metrics" >"$dir/metrics-anonymous.out" 2>&1; then fail 'anonymous metrics unexpectedly succeeded'; fi
api_get /metrics "$token" >"$dir/metrics.txt" || fail 'authenticated source metrics scrape failed'
rg -q '^nerocd_backup_last_result\{outcome="success"\} 1$' "$dir/metrics.txt" || fail 'successful backup was absent from metrics'
rg -q '^nerocd_backup_last_reason\{reason="none"\} 1$' "$dir/metrics.txt" || fail 'successful backup reason was absent from metrics'
! rg -Fq "$password" "$dir/metrics.txt" && ! rg -Fq "$app_password" "$dir/metrics.txt" && ! rg -Fq "$runner_id" "$dir/metrics.txt" || fail 'metrics disclosed backup credential or runtime identity'
record 'observability_backup_scrape authenticated=true anonymous_denied=true success_result=true fixed_labels=true'
record 'source_backup_atomic_manifest_checksum=true runner_file_inventory_metadata_only=true'
cd "$root/web/app" && bun "$root/acceptance/system-operations/browser.mjs" "$base" "$dir/browser.credentials" success "$dir/browser-success.json" >"$dir/browser-success.log" 2>&1 || { tail -n 40 "$dir/browser-success.log" >>"$evidence"; fail 'browser did not observe successful backup status'; }
# The API keeps its all-or-nothing administrative response intentionally
# non-enumerating. Anonymous callers get no snapshot, and an incompatible
# schema produces the same bounded 503 before a partial response is encoded.
anonymous_code=$(curl -ksS --max-time 5 -o "$dir/operations-anonymous.json" -w '%{http_code}' "$base/api/v1/operations/status")
[[ "$anonymous_code" == 401 ]] || fail 'anonymous operations status did not return 401'
! rg -q 'snapshot|postgres:|schema_' "$dir/operations-anonymous.json" || fail 'anonymous operations status disclosed diagnostic data'
owner_function=$(docker exec "$source" psql -At -U "$owner" -d nerocd -c "SELECT pg_get_functiondef('nerocd_repository_policy_schema_compatible()'::regprocedure)")
[[ -n "$owner_function" ]] || fail 'could not retain schema compatibility function'
docker exec "$source" psql -v ON_ERROR_STOP=1 -U "$owner" -d nerocd -c "CREATE OR REPLACE FUNCTION nerocd_repository_policy_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS 'SELECT false'" >"$dir/operations-incompatible.log"
for _ in {1..20}; do code=$(curl -ksS --max-time 5 -o "$dir/operations-incompatible.json" -w '%{http_code}' -H "Authorization: Bearer $(jq -r .token <<<"$session")" "$base/api/v1/operations/status"); [[ "$code" == 503 ]] && break; sleep .2; done
[[ "$code" == 503 ]] || fail 'incompatible operations status did not return 503'
! rg -q 'snapshot|postgres:|schema_' "$dir/operations-incompatible.json" || fail 'incompatible operations response disclosed a partial snapshot'
cd "$root/web/app" && bun "$root/acceptance/system-operations/browser.mjs" "$base" "$dir/browser.credentials" unavailable "$dir/browser-unavailable.json" >"$dir/browser-unavailable.log" 2>&1 || { tail -n 40 "$dir/browser-unavailable.log" >>"$evidence"; fail 'browser did not render bounded unavailable operations state'; }
printf '%s\n' "$owner_function" | docker exec -i "$source" psql -v ON_ERROR_STOP=1 -U "$owner" -d nerocd >"$dir/operations-compatible-restore.log"
for _ in {1..20}; do curl -kfsS --max-time 5 "$base/api/v1/ready" | jq -e '.status == "ready"' >/dev/null && break; sleep .2; done
curl -kfsS --max-time 5 "$base/api/v1/ready" | jq -e '.status == "ready"' >/dev/null || fail 'readiness did not recover after compatibility restoration'
record 'system_operations_browser_backup_success_age=true schema_incompatible_ready_503=true recovery_ready_200=true anonymous_denied_no_partial=true browser_mobile_keyboard=true'

if run_tool "$dir/target-url" -v "$backup_dir:/restore:ro" "$image_ref" restore --input-dir /restore >"$dir/missing-runner-files.out" 2>&1; then fail 'restore accepted missing runner-file inventory'; fi
rg -q 'runner-file' "$dir/missing-runner-files.out" || fail 'missing runner-file inventory did not fail closed'
if run_tool "$dir/target-url" -v "$backup_dir:/restore:ro" -v "$dir/wrong-runner-files:/runner-files:ro" "$image_ref" restore --input-dir /restore --runner-file-root /runner-files >"$dir/wrong-runner-files.out" 2>&1; then fail 'restore accepted wrong runner-file inventory'; fi
rg -q 'runner-file' "$dir/wrong-runner-files.out" || fail 'wrong runner-file inventory did not fail closed'

# Application/build and embedded migration identity are admission checks before
# a target connection is allowed to mutate anything.
docker run --rm -v "$backup_dir:/backup" alpine:3.22.2 sh -ec "sed -i 's/\"application_version\": \"[^\"]*\"/\"application_version\": \"incompatible-build\"/' /backup/manifest.json; chmod 600 /backup/manifest.json"
if run_tool "$dir/target-url" -v "$backup_dir:/restore:ro" -v "$dir/runner-files:/runner-files:ro" "$image_ref" restore --input-dir /restore --runner-file-root /runner-files >"$dir/build-mismatch.out" 2>&1; then fail 'restore accepted incompatible application build manifest'; fi
rg -q 'compatibility' "$dir/build-mismatch.out" || fail 'build mismatch did not fail closed'
backup >"$dir/backup-compatible.log" 2>&1 || fail 'replacement backup after compatibility negative failed'
backup_name=$(basename "$(tail -n1 "$dir/backup-compatible.log")")
backup_dir="$dir/backups/$backup_name"

# Strict manifest admission is entirely before target mutation. The archive is
# replaced after each in-container edit to avoid Desktop bind-cache ambiguity.
for manifest_case in unknown trailing oversized; do
  case "$manifest_case" in
    unknown) docker run --rm -v "$backup_dir:/backup" alpine:3.22.2 sh -ec "sed -i 's/{/{\"unexpected\":true,/' /backup/manifest.json; chmod 600 /backup/manifest.json" ;;
    trailing) docker run --rm -v "$backup_dir:/backup" alpine:3.22.2 sh -ec 'printf "{}" >> /backup/manifest.json' ;;
    oversized) docker run --rm -v "$backup_dir:/backup" alpine:3.22.2 sh -ec 'head -c 70000 /dev/zero | tr "\\000" x >> /backup/manifest.json' ;;
  esac
  if run_tool "$dir/target-url" -v "$backup_dir:/restore:ro" -v "$dir/runner-files:/runner-files:ro" "$image_ref" restore --input-dir /restore --runner-file-root /runner-files >"$dir/manifest-$manifest_case.out" 2>&1; then fail "restore accepted $manifest_case manifest"; fi
  [[ $(wc -c <"$dir/manifest-$manifest_case.out") -le 1024 ]] || fail "manifest $manifest_case diagnostic was unbounded"
  [[ $(docker exec "$target" psql -At -U "$owner" -d nerocd -c "SELECT count(*) FROM pg_tables WHERE schemaname='public'") == 0 ]] || fail "manifest $manifest_case mutated target"
  backup >"$dir/backup-$manifest_case-replacement.log" 2>&1 || fail "replacement backup after $manifest_case manifest failed"
  backup_name=$(basename "$(tail -n1 "$dir/backup-$manifest_case-replacement.log")")
  backup_dir="$dir/backups/$backup_name"
done

# Descriptor-relative path admission rejects both an intermediate directory
# link and a final manifest link before the target is opened.
docker run --rm -v "$dir/backups:/parent" alpine:3.22.2 sh -ec "ln -s '$backup_name' /parent/intermediate-link"
if run_tool "$dir/target-url" -v "$dir/backups:/parent:ro" -v "$dir/runner-files:/runner-files:ro" "$image_ref" restore --input-dir /parent/intermediate-link --runner-file-root /runner-files >"$dir/intermediate-symlink.out" 2>&1; then fail 'restore accepted intermediate symlink'; fi
[[ $(docker exec "$target" psql -At -U "$owner" -d nerocd -c "SELECT count(*) FROM pg_tables WHERE schemaname='public'") == 0 ]] || fail 'intermediate symlink mutated target'
docker run --rm -v "$backup_dir:/backup" alpine:3.22.2 sh -ec 'rm /backup/manifest.json; ln -s database.dump /backup/manifest.json'
if run_tool "$dir/target-url" -v "$backup_dir:/restore:ro" -v "$dir/runner-files:/runner-files:ro" "$image_ref" restore --input-dir /restore --runner-file-root /runner-files >"$dir/final-symlink.out" 2>&1; then fail 'restore accepted final manifest symlink'; fi
[[ $(docker exec "$target" psql -At -U "$owner" -d nerocd -c "SELECT count(*) FROM pg_tables WHERE schemaname='public'") == 0 ]] || fail 'final symlink mutated target'
backup >"$dir/backup-symlink-replacement.log" 2>&1 || fail 'replacement backup after symlink negatives failed'
backup_name=$(basename "$(tail -n1 "$dir/backup-symlink-replacement.log")")
backup_dir="$dir/backups/$backup_name"

# Private archive inputs are mandatory; a world-readable final manifest is
# rejected through the descriptor check before parsing or target mutation.
docker run --rm -v "$backup_dir:/backup" alpine:3.22.2 chmod 0644 /backup/manifest.json
if run_tool "$dir/target-url" -v "$backup_dir:/restore:ro" -v "$dir/runner-files:/runner-files:ro" "$image_ref" restore --input-dir /restore --runner-file-root /runner-files >"$dir/permissive-manifest.out" 2>&1; then fail 'restore accepted permissive manifest'; fi
[[ $(docker exec "$target" psql -At -U "$owner" -d nerocd -c "SELECT count(*) FROM pg_tables WHERE schemaname='public'") == 0 ]] || fail 'permissive manifest mutated target'
backup >"$dir/backup-permission-replacement.log" 2>&1 || fail 'replacement backup after permissions negative failed'
backup_name=$(basename "$(tail -n1 "$dir/backup-permission-replacement.log")")
backup_dir="$dir/backups/$backup_name"

# Docker's root fixture can express the owner mismatch that a normal developer
# account cannot. Modes remain private; only the EUID ownership check fails.
docker run --rm -u 0:0 -v "$backup_dir:/backup" alpine:3.22.2 sh -ec 'chown 65534:65534 /backup/manifest.json; chmod 600 /backup/manifest.json'
observed_owner=$(docker run --rm -v "$backup_dir:/backup:ro" alpine:3.22.2 stat -c '%u' /backup/manifest.json)
if [[ "$observed_owner" == 65534 ]]; then
  if run_tool "$dir/target-url" -v "$backup_dir:/restore:ro" -v "$dir/runner-files:/runner-files:ro" "$image_ref" restore --input-dir /restore --runner-file-root /runner-files >"$dir/wrong-owner-manifest.out" 2>&1; then fail 'restore accepted wrong-owner manifest'; fi
  [[ $(docker exec "$target" psql -At -U "$owner" -d nerocd -c "SELECT count(*) FROM pg_tables WHERE schemaname='public'") == 0 ]] || fail 'wrong-owner manifest mutated target'
else
  record 'wrong_owner_fixture_skipped=docker_desktop_bind_owner_mapping'
fi
backup >"$dir/backup-owner-replacement.log" 2>&1 || fail 'replacement backup after ownership negative failed'
backup_name=$(basename "$(tail -n1 "$dir/backup-owner-replacement.log")")
backup_dir="$dir/backups/$backup_name"

# This malformed archive has a matching manifest checksum/size, forcing the
# failure into pg_restore. Its --single-transaction invocation must leave the
# still-empty target without a partial public schema.
docker run --rm -v "$backup_dir:/backup" alpine:3.22.2 sh -ec 'size=$(wc -c < /backup/database.dump); truncate -s $((size / 2)) /backup/database.dump; sum=$(sha256sum /backup/database.dump | cut -d " " -f1); bytes=$(wc -c < /backup/database.dump); sed -i "s@sha256\": \"[a-f0-9]*@sha256\": \"$sum@; s@\"bytes\": [0-9]*@\"bytes\": $bytes@" /backup/manifest.json; chmod 600 /backup/manifest.json'
if run_tool "$dir/target-url" -v "$backup_dir:/restore:ro" -v "$dir/runner-files:/runner-files:ro" "$image_ref" restore --input-dir /restore --runner-file-root /runner-files >"$dir/truncated-archive.out" 2>&1; then fail 'restore accepted truncated archive'; fi
rg -q 'pg_restore failed' "$dir/truncated-archive.out" || fail 'truncated archive did not reach pg_restore'
[[ $(docker exec "$target" psql -At -U "$owner" -d nerocd -c "SELECT count(*) FROM pg_tables WHERE schemaname='public'") == 0 ]] || fail 'truncated archive left partial target schema'
backup >"$dir/backup-truncated-replacement.log" 2>&1 || fail 'replacement backup after truncated archive failed'
backup_name=$(basename "$(tail -n1 "$dir/backup-truncated-replacement.log")")
backup_dir="$dir/backups/$backup_name"

# A checksum failure is detected before the restore subprocess runs.
expected_bytes=$(jq -r '.files[0].bytes' "$backup_dir/manifest.json")
expected_checksum=$(jq -r '.files[0].sha256' "$backup_dir/manifest.json")
# Mutate from inside Docker rather than through a host-side bind-cache. That
# makes this negative deterministic on Docker Desktop as well as Linux.
docker run --rm -v "$backup_dir:/backup" alpine:3.22.2 sh -ec 'printf x >> /backup/database.dump' >/dev/null
mounted_state=$(docker run --rm -v "$backup_dir:/restore:ro" alpine:3.22.2 sh -ec 'wc -c < /restore/database.dump; sha256sum /restore/database.dump | awk "{print \$1}"')
actual_bytes=$(head -n1 <<<"$mounted_state" | tr -d ' ')
actual_checksum=$(tail -n1 <<<"$mounted_state")
[[ "$actual_bytes" == "$((expected_bytes + 1))" ]] || fail 'tamper fixture did not alter the dump visible to restore tool'
[[ "$actual_checksum" != "$expected_checksum" ]] || fail 'tamper fixture did not alter the checksum visible to restore tool'
if run_tool "$dir/target-url" -v "$backup_dir:/restore:ro" -v "$dir/runner-files:/runner-files:ro" "$image_ref" restore --input-dir /restore --runner-file-root /runner-files >"$dir/tamper.out" 2>&1; then fail 'restore accepted a tampered dump'; fi
rg -q 'checksum' "$dir/tamper.out" || fail 'tampered dump error did not identify checksum failure'
# Keep the invalid archive in this disposable parent; the new archive below has
# a distinct atomic name and is the only one considered for restoration.
backup >"$dir/backup-2.log" 2>&1 || fail 'replacement backup failed'
backup_name=$(basename "$(tail -n1 "$dir/backup-2.log")")
backup_dir="$dir/backups/$backup_name"

# A table created in the target proves the strict empty-target check happens
# before pg_restore; cleanup restores the target to its initial empty state.
docker exec "$target" psql -v ON_ERROR_STOP=1 -U "$owner" -d nerocd -c 'CREATE TABLE restore_must_refuse (id integer)' >"$dir/nonempty-fixture.log"
if run_tool "$dir/target-url" -v "$backup_dir:/restore:ro" -v "$dir/runner-files:/runner-files:ro" "$image_ref" restore --input-dir /restore --runner-file-root /runner-files >"$dir/nonempty.out" 2>&1; then fail 'restore accepted a nonempty target'; fi
rg -q 'empty database' "$dir/nonempty.out" || fail 'nonempty target did not return strict empty-target error'
docker exec "$target" psql -v ON_ERROR_STOP=1 -U "$owner" -d nerocd -c 'DROP TABLE restore_must_refuse' >"$dir/nonempty-cleanup.log"

if ! run_tool "$dir/target-url" -v "$backup_dir:/restore:ro" -v "$dir/runner-files:/runner-files:ro" "$image_ref" restore --input-dir /restore --runner-file-root /runner-files >"$dir/restore.log" 2>&1; then
  [[ $(wc -c <"$dir/restore.log") -le 1024 ]] || fail 'clean-target restore failed with oversized diagnostic'
  ! rg -Fq "$password" "$dir/restore.log" || fail 'restore diagnostic disclosed a secret'
  record "restore_error=$(tr '\n' ' ' <"$dir/restore.log")"
  fail 'clean-target restore failed'
fi
session_revoked=$(docker exec "$target" psql -At -U "$owner" -d nerocd -c "SELECT revoked_at IS NOT NULL FROM sessions WHERE id='$session_id'")
lease_expired=$(docker exec "$target" psql -At -U "$owner" -d nerocd -c "SELECT status = 'expired' AND completed_at IS NOT NULL FROM run_leases WHERE id='backup-lease'")
[[ "$session_revoked" == t && "$lease_expired" == t ]] || fail 'restore did not invalidate session and active lease'
ledger_source=$(docker exec "$source" psql -At -U "$owner" -d nerocd -c 'SELECT version || chr(58) || checksum FROM schema_migrations ORDER BY version')
ledger_target=$(docker exec "$target" psql -At -U "$owner" -d nerocd -c 'SELECT version || chr(58) || checksum FROM schema_migrations ORDER BY version')
[[ "$ledger_source" == "$ledger_target" ]] || fail 'restore migration compatibility ledger differs from source'
docker run --rm --user "$(id -u):$(id -g)" --network "$network" \
  -e NEROCD_OWNER_DATABASE_USER="$owner" -e NEROCD_APP_DATABASE_USER="$app" \
  -v "$dir/target-url:/runtime/owner-url:ro" -v "$dir/target-app-url:/runtime/app-url:ro" \
  "$image_ref" provision-app-role --owner-file /runtime/owner-url --app-file /runtime/app-url >"$dir/target-provision-app.log" 2>&1 || fail 'restored application role provision failed'
docker run -d --name "$target_server" --user "$(id -u):$(id -g)" --network "$network" \
  -e NEROCD_MODE=production -e NEROCD_IMAGE_REF="$image_ref" -e NEROCD_DATABASE_CREDENTIAL=app -e NEROCD_APP_DATABASE_USER="$app" \
  -e NEROCD_DATABASE_URL_FILE=/runtime/app-url -e NEROCD_PUBLIC_ORIGIN=https://backup.example.invalid \
  -v "$dir/target-app-url:/runtime/app-url:ro" "$image_ref" server --addr :8080 >/dev/null
for _ in {1..30}; do docker run --rm --network "$network" alpine:3.22.2 wget -qO- "http://$target_server:8080/api/v1/ready" >/dev/null 2>&1 && break; sleep 1; done
docker run --rm --network "$network" alpine:3.22.2 wget -qO- "http://$target_server:8080/api/v1/ready" | jq -e '.status == "ready"' >/dev/null || fail 'restored server was not ready'
restored_api_post(){
  docker run --rm --network "$network" alpine:3.22.2 wget -qO- --header 'Content-Type: application/json' --post-data "$2" "http://$target_server:8080$1"
}
restored_session=$(restored_api_post /api/v1/sessions "{\"email\":\"$admin_email\",\"password\":\"$bootstrap_password\"}") || fail 'restored credential login failed'
restored_token=$(jq -r '.token' <<<"$restored_session")
[[ -n "$restored_token" && "$restored_token" != null ]] || fail 'restored login did not issue a fresh session'
restored_get(){ docker run --rm --network "$network" alpine:3.22.2 wget -qO- --header "Authorization: Bearer $restored_token" "http://$target_server:8080$1"; }
for endpoint in "/api/v1/projects" "/api/v1/services?project_id=$project_id" "/api/v1/environments?service_id=$service_id" "/api/v1/revisions?service_id=$service_id" "/api/v1/deployments?environment_id=$environment_id" "/api/v1/runners" "/api/v1/audit-events"; do
  restored_get "$endpoint" | jq -e '.items | length > 0' >/dev/null || fail "restored API history is missing for $endpoint"
done
record 'clean_target_restore=true migration_ledger_exact=true sessions_revoked=true active_leases_expired=true restored_server_ready=true fresh_login_and_public_history_verified=true'
pass=true
