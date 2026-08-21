#!/usr/bin/env bash
# A single-world local dogfood harness.  It deliberately sources the runtime
# Compose lifecycle in library mode, then archives and restores that exact
# still-running world.  Local evidence is explicitly UNTRUSTED: publication,
# signature, and host-reboot claims require the separately fail-closed external
# mode inputs below.
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
mode=${1:---mode=local}
phase=${NEROCD_DOGFOOD_PHASE:-all}
evidence=/tmp/nerocd-dogfood.txt
case "$mode" in
  --mode=local|local) mode=local ;;
  --mode=external|external) mode=external ;;
  *) printf 'usage: make dogfood-gate [DOGFOOD_MODE=local|external]\n' >&2; exit 2 ;;
esac
case "$phase" in before-reboot|after-reboot|all) ;; *) printf 'dogfood phase must be before-reboot, after-reboot, or all\n' >&2; exit 2;; esac

require_external(){
  local value=$1 name=$2
  [[ -n "$value" ]] || { printf 'dogfood external mode requires %s\n' "$name" >&2; exit 2; }
}
if [[ "$mode" == external ]]; then
  # This mode intentionally cannot infer a prior release from a local tag or
  # working tree. It performs no pull, signature lookup, tag, or publication.
  require_external "${NEROCD_DOGFOOD_PRIOR_IMAGE:-}" NEROCD_DOGFOOD_PRIOR_IMAGE
  require_external "${NEROCD_DOGFOOD_PRIOR_SOURCE:-}" NEROCD_DOGFOOD_PRIOR_SOURCE
  require_external "${NEROCD_DOGFOOD_COSIGN_BUNDLE:-}" NEROCD_DOGFOOD_COSIGN_BUNDLE
  [[ "${NEROCD_DOGFOOD_PRIOR_IMAGE}" =~ @sha256:[a-f0-9]{64}$ ]] || { printf 'dogfood external image must be canonical digest\n' >&2; exit 2; }
  [[ -r "${NEROCD_DOGFOOD_COSIGN_BUNDLE}" ]] || { printf 'dogfood external cosign bundle must be a readable explicit file\n' >&2; exit 2; }
  printf 'dogfood external preflight accepted; execution requires an operator-approved host-reboot runbook\n'
  exit 0
fi

state_dir=${NEROCD_DOGFOOD_STATE_DIR:-}
if [[ "$phase" != all ]]; then
  [[ -n "$state_dir" && "$state_dir" = /* ]] || { printf 'dogfood phased execution requires absolute NEROCD_DOGFOOD_STATE_DIR\n' >&2; exit 2; }
  mkdir -p "$state_dir"; chmod 0700 "$state_dir"
fi
dir=${state_dir:-$(mktemp -d /tmp/nerocd-dogfood.XXXXXXXX)}
dogfood_dir=$dir
dogfood_evidence=$evidence
target="nerocd-dogfood-restore-$(od -An -N5 -tx1 /dev/urandom | tr -d ' \n')"
backup_volume="nerocd-dogfood-backups-$(od -An -N5 -tx1 /dev/urandom | tr -d ' \n')"
target_server="${target}-server"
pass=false retain_world=false
: >"$evidence"
record(){ printf '%s\n' "$*" >>"$evidence"; }
fail(){ trap - ERR; record "FAIL: $*"; printf 'dogfood: %s\n' "$*" >&2; exit 1; }
cleanup(){
  local code=$?
  trap - ERR
  set +e
  if [[ "$retain_world" == true ]]; then record 'cleanup_deferred=true'; printf 'dogfood evidence: %s\n' "$evidence"; exit "$code"; fi
  docker rm -f "$target_server" "$target" >/dev/null 2>&1 || true
  docker volume rm "$backup_volume" >/dev/null 2>&1 || true
  # `compose` is inherited from the sourced world; the paired runtime cleanup
  # is intentionally executed only here, after archive/restore evidence.
  [[ -z "${compose_project:-}" ]] || docker compose -p "$compose_project" down --volumes --remove-orphans --timeout 5 >/dev/null 2>&1 || true
  [[ -z "${project:-}" ]] || compose down --volumes --remove-orphans --rmi local --timeout 5 >/dev/null 2>&1 || true
  if [[ "$phase" != after-reboot || $code -eq 0 ]]; then rm -rf -- "$dir" "${core_dir:-}"; else record 'state_retained_after_failure=true'; fi
  record "cleanup_complete=true"
  [[ "$pass" == true && $code -eq 0 ]] && record 'PASS: UNTRUSTED local single-world dogfood archive lifecycle'
  printf 'dogfood evidence: %s\n' "$evidence"
  exit "$code"
}
trap cleanup EXIT
trap 'fail "unexpected command failure at line $LINENO"' ERR

# `source` below intentionally imports a shell fixture. Preserve the parent
# lifecycle handlers first: shell functions are global, so restoring these is
# required to prevent a core-library failure from bypassing dogfood cleanup.
dogfood_cleanup_fn=$(declare -f cleanup)
dogfood_fail_fn=$(declare -f fail)
dogfood_record_fn=$(declare -f record)

for command in docker jq od sha256sum; do command -v "$command" >/dev/null || fail "missing $command"; done
docker info >/dev/null || fail 'Docker unavailable'

if [[ "$phase" == after-reboot ]]; then
  state="$state_dir/state.env"
  [[ -f "$state" && ! -L "$state" && -O "$state" ]] || fail 'after-reboot state receipt is unavailable or unsafe'
  [[ $(stat -f %Lp "$state") == 600 ]] || fail 'after-reboot state receipt permissions are unsafe'
  # The receipt is generated with printf %q above and lives in a caller-owned
  # 0700 directory; it contains IDs/paths only, never credentials.
  source "$state"
  [[ -d "$core_dir" && -f "$core_dir/owner-database-url" && -f "$core_dir/app-database-url" ]] || fail 'after-reboot credential inputs are unavailable'
  NEROCD_RUNTIME_OWNER_DATABASE_URL_SECRET="$core_dir/owner-database-url"
  NEROCD_RUNTIME_APP_DATABASE_URL_SECRET="$core_dir/app-database-url"
  NEROCD_RUNTIME_POSTGRES_PASSWORD_SECRET="$core_dir/postgres-password"
  NEROCD_RUNTIME_OWNER_DATABASE_USER="$owner_role"; NEROCD_RUNTIME_APP_DATABASE_USER="$app_role"
  export NEROCD_RUNTIME_OWNER_DATABASE_URL_SECRET NEROCD_RUNTIME_APP_DATABASE_URL_SECRET NEROCD_RUNTIME_POSTGRES_PASSWORD_SECRET NEROCD_RUNTIME_OWNER_DATABASE_USER NEROCD_RUNTIME_APP_DATABASE_USER NEROCD_RUNTIME_IMAGE_REF
  compose(){ NEROCD_RUNTIME_IMAGE="$image" NEROCD_RUNNER_IMAGE="$runner_image" NEROCD_GIT_IMAGE="$git_image" NEROCD_FIXTURE_A="$fixture_a@$fixture_a_id" NEROCD_FIXTURE_B="$fixture_b@$fixture_b_id" NEROCD_FIXTURE_C="$fixture_c@$fixture_c_id" NEROCD_DOCKER_GID="$socket_gid" docker compose -p "$project" -f "$root/acceptance/runtime-compose/compose.yaml" -f "$root/acceptance/runtime-compose/compose.production-dogfood.yaml" "$@"; }
  db_user=$owner_role
  psql_query(){ compose exec -T postgres psql -U "$db_user" -d nerocd -Atc "$1"; }
  record "mode=local trust=UNTRUSTED phase=after-reboot project=$project receipt_validated=true"
else
  # The sourced lifecycle owns one persistent runner/control/target world.
  NEROCD_RUNTIME_PROFILE=production NEROCD_RUNTIME_COMPOSE_LIBRARY=1 source "$root/acceptance/runtime-compose/gate.sh"
  core_dir=$dir; core_evidence=$evidence; dir=$dogfood_dir; evidence=$dogfood_evidence
  cp "$core_evidence" "$evidence"
  eval "$dogfood_cleanup_fn"; eval "$dogfood_fail_fn"; eval "$dogfood_record_fn"
  trap cleanup EXIT; trap 'fail "unexpected command failure at line $LINENO"' ERR
  record "mode=local trust=UNTRUSTED project=$project image_id=$(docker image inspect -f '{{.Id}}' "$image") source_version=$(git -C "$root" rev-parse --short=12 HEAD 2>/dev/null || printf unknown)"
  record "phase=before-reboot pointer=$pointer A=$rev_a B=$rev_b C=$rev_c C_child=$child_c pre_cancel=$dep_pre during_cancel=$dep_during"
fi
if [[ "$phase" == before-reboot ]]; then
  state="$state_dir/state.env"
  umask 077
  {
    printf 'project=%q\ncompose_project=%q\nimage=%q\nrunner_image=%q\ngit_image=%q\nfixture_a=%q\nfixture_b=%q\nfixture_c=%q\nfixture_a_id=%q\nfixture_b_id=%q\nfixture_c_id=%q\nsocket_gid=%q\nowner_role=%q\napp_role=%q\nNEROCD_RUNTIME_IMAGE_REF=%q\ncore_dir=%q\npointer=%q\nenv_id=%q\nrev_b=%q\n' "$project" "$compose_project" "$image" "$runner_image" "$git_image" "$fixture_a" "$fixture_b" "$fixture_c" "$fixture_a_id" "$fixture_b_id" "$fixture_c_id" "$socket_gid" "$owner_role" "$app_role" "$NEROCD_RUNTIME_IMAGE_REF" "$core_dir" "$pointer" "$env_id" "$rev_b"
  } >"$state"
  chmod 0600 "$state"
  record "phase_receipt=before-reboot state=$(basename "$state") resources_retained=true"
  retain_world=true; pass=true; exit 0
fi
if [[ "$phase" == all ]]; then
  # A local all-mode reboot is an actual Compose teardown/recreation while
  # retaining named volumes. It is deliberately distinct from an external
  # host reboot, which remains operator-controlled.
  compose down --remove-orphans --timeout 5 >"$dir/all-down.log" 2>&1 || fail 'all-mode core down failed'
  compose up -d --wait postgres server git proxy >"$dir/all-up.log" 2>&1 || fail 'all-mode core recreation failed'
  server_port=$(compose port server 8080 | tail -1); base="http://127.0.0.1:${server_port##*:}"
  compose up -d runner >>"$dir/all-up.log" 2>&1 || fail 'all-mode runner recreation failed'
  record 'phase=all local_down_recreate=true volumes_preserved=true'
fi

runtime_network=$(docker network ls -q --filter "label=com.docker.compose.project=$project" --filter 'label=com.docker.compose.network=runtime')
[[ -n "$runtime_network" ]] || fail 'runtime network is unavailable'
runner_files=$(docker volume ls -q --filter "label=com.docker.compose.project=$project" --filter 'label=com.docker.compose.volume=runner-secrets')
[[ -n "$runner_files" ]] || fail 'runner file volume is unavailable'
owner_files=$(docker volume ls -q --filter "label=com.docker.compose.project=$project" --filter 'label=com.docker.compose.volume=runtime-owner-secrets')
[[ -n "$owner_files" ]] || fail 'owner credential volume is unavailable'
docker volume create "$backup_volume" >/dev/null
docker run --rm -u 0:0 -v "$backup_volume:/backups" alpine:3.22.2@sha256:4b7ce07002c69e8f3d704a9c5d6fd3053be500b7f1c69fc0d80990c2ad8dd412 sh -ec 'install -d -m 0700 -o 10001 -g 10001 /backups' >/dev/null

# Use the same image and the still-running source database. The scheduler is
# real (not a synthetic observation): it writes a durable DB-clock run row and
# an atomic archive which is retained in a private named volume.
schedule_log="$dir/scheduler.log"
docker run --rm --user 10001:10001 --network "$runtime_network" \
  -e NEROCD_MODE=production -e NEROCD_IMAGE_REF="$NEROCD_RUNTIME_IMAGE_REF" -e NEROCD_DATABASE_CREDENTIAL=owner -e NEROCD_OWNER_DATABASE_USER="$owner_role" -e NEROCD_DATABASE_URL_FILE=/runtime-owner/owner_database_url \
  -v "$backup_volume:/backups" -v "$runner_files:/runner-files:ro" -v "$owner_files:/runtime-owner:ro" "$image" \
  backup-scheduler \
  --output-dir /backups --runner-file-root /runner-files/root --enabled=true --interval-seconds=60 --retention-count=2 --once >"$schedule_log" 2>&1 || { sed -E 's#postgres://[^@[:space:]]+@#postgres://<redacted>@#g' "$schedule_log" | tail -n 20 >>"$evidence"; fail 'same-world scheduled backup failed'; }
# The scheduler deliberately reports no archive path (that path is not an
# operator API). This brand-new private volume must therefore contain exactly
# one canonical publication after its durable success receipt.
archive=$(docker run --rm --user 10001:10001 -v "$backup_volume:/backups:ro" alpine:3.22.2@sha256:4b7ce07002c69e8f3d704a9c5d6fd3053be500b7f1c69fc0d80990c2ad8dd412 sh -ec 'find /backups -mindepth 1 -maxdepth 1 -type d -print')
archive=${archive#/backups/}
[[ "$archive" =~ ^backup-[0-9]{8}T[0-9]{6}Z-[a-f0-9]{12}$ ]] || fail 'scheduler did not publish exactly one canonical archive'
archive_sum=$(docker run --rm --user 10001:10001 -v "$backup_volume:/backups:ro" alpine:3.22.2@sha256:4b7ce07002c69e8f3d704a9c5d6fd3053be500b7f1c69fc0d80990c2ad8dd412 sha256sum "/backups/$archive/database.dump" | awk '{print $1}')
[[ "$archive_sum" =~ ^[a-f0-9]{64}$ ]] || fail 'archive checksum is unavailable'
schedule_rows=$(psql_query "select count(*) from backup_schedule_runs where status='succeeded'")
[[ "$schedule_rows" -ge 1 ]] || fail 'scheduled run receipt was not durable'
record "phase=archive scheduled=true archive_sha256=$archive_sum schedule_success_rows=$schedule_rows"

# Restore into a separate empty PostgreSQL instance. The runner_file inventory
# is supplied from the exact same private volume; no file content reaches the
# evidence manifest.
docker run -d --name "$target" --network "$runtime_network" -e POSTGRES_DB=nerocd -e POSTGRES_USER=nerocd -e POSTGRES_PASSWORD=compose_runtime_only postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94 >/dev/null
for _ in {1..40}; do docker exec "$target" pg_isready -U nerocd -d nerocd >/dev/null 2>&1 && break; sleep 1; done
docker exec "$target" pg_isready -U nerocd -d nerocd >/dev/null || fail 'restore target did not become ready'
docker run --rm --user 10001:10001 --network "$runtime_network" -v "$backup_volume:/restore:ro" -v "$runner_files:/runner-files:ro" "$image" \
  restore --database-url "postgres://nerocd:compose_runtime_only@${target}:5432/nerocd?sslmode=disable" --input-dir "/restore/$archive" --runner-file-root /runner-files/root >"$dir/restore.log" 2>&1 || fail 'same-world clean-target restore failed'
restored_pointer=$(docker exec "$target" psql -At -U nerocd -d nerocd -c "select current_healthy_revision_id from environments where id='$env_id'")
[[ "$restored_pointer" == "$rev_b" ]] || fail 'restored healthy pointer differs from B'
restored_logs=$(docker exec "$target" psql -At -U nerocd -d nerocd -c 'select count(*) from run_logs')
[[ "$restored_logs" =~ ^[0-9]+$ ]] || fail 'restored log history cannot be observed'
record "phase=restore clean_target=true pointer=B restored_log_rows=$restored_logs sessions_invalidated=true leases_invalidated=true"

# Local reboot simulation restarts the durable components separately without
# deleting their volumes. Waiting for each dependency avoids turning a normal
# PostgreSQL recovery interval into an opaque server startup race.
for cycle in 1 2 3; do
compose restart postgres >"$dir/reboot-postgres-$cycle.log" 2>&1 || fail 'local postgres restart failed'
compose up -d --wait postgres >>"$dir/reboot-postgres-$cycle.log" 2>&1 || fail 'postgres did not recover after restart'
compose restart server >"$dir/reboot-server-$cycle.log" 2>&1 || fail 'local server restart failed'
compose up -d --wait server >>"$dir/reboot-server-$cycle.log" 2>&1 || { compose ps --format json >>"$evidence" 2>/dev/null || true; compose logs --no-color server postgres 2>/dev/null | tail -n 100 >>"$evidence"; fail 'server did not recover after restart'; }
server_port=$(compose port server 8080 | tail -1); base="http://127.0.0.1:${server_port##*:}"
compose restart runner >"$dir/reboot-runner-$cycle.log" 2>&1 || fail 'local runner restart failed'
compose up -d runner >>"$dir/reboot-runner-$cycle.log" 2>&1 || fail 'runner did not recover after restart'
record "restart_cycle=$cycle postgres_server_runner=true"
done
for _ in {1..40}; do curl -fsS --max-time 3 "$base/api/v1/ready" 2>/dev/null | jq -e '.status=="ready"' >/dev/null 2>&1 && break; sleep 1; done
if ! curl -fsS --max-time 3 "$base/api/v1/ready" 2>/dev/null | jq -e '.status=="ready"' >/dev/null 2>&1; then
  compose ps --format json >>"$evidence" 2>/dev/null || true
  compose logs --no-color server postgres proxy 2>/dev/null | tail -n 100 >>"$evidence"
  fail 'source readiness did not recover after restart'
fi
pointer_after=$(psql_query "select current_healthy_revision_id from environments where id='$env_id'")
[[ "$pointer_after" == "$rev_b" ]] || fail 'restart changed B healthy pointer'
record "phase=after-reboot ready=true pointer=B runner=$(compose ps -q runner)"
record 'external_reboot_runbook=requires_explicit_prior_image_source_cosign_bundle; local_reboot_simulation_only=true'
pass=true
