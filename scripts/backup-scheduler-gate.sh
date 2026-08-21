#!/usr/bin/env bash
# AC16b: exercise the local owner-only scheduler against real PostgreSQL.
# The script deliberately uses the shipped production command, pg_dump, and
# pg_restore. Direct SQL is restricted to moving the durable DB-clock due time
# between accelerated test phases and reading independent postconditions.
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
evidence=/tmp/nerocd-backup-scheduler.txt
work=$(mktemp -d /tmp/nerocd-backup-scheduler.XXXXXXXX)
suffix=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')
network="nerocd-backup-scheduler-$suffix"
source="nerocd-backup-scheduler-source-$suffix"
target="nerocd-backup-scheduler-target-$suffix"
image_tag="nerocd-backup-scheduler:$suffix"
schedule_a="nerocd-backup-scheduler-a-$suffix"
schedule_b="nerocd-backup-scheduler-b-$suffix"
pass=false
: >"$evidence"
record(){ printf '%s\n' "$*" >>"$evidence"; }
fail(){ trap - ERR; record "FAIL: $*"; printf 'backup-scheduler: %s\n' "$*" >&2; exit 1; }
cleanup(){
  local code=$?
  trap - ERR
  set +e
  docker rm -f "$schedule_a" "$schedule_b" "$source" "$target" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  docker image rm -f "$image_tag" >/dev/null 2>&1 || true
  rm -rf -- "$work"
  [[ "$pass" == true && $code -eq 0 ]] && record 'PASS: durable local backup scheduler gate'
  printf 'backup scheduler evidence: %s\n' "$evidence"
  exit "$code"
}
trap cleanup EXIT
trap 'fail "unexpected command failure at line $LINENO"' ERR

for command in docker jq od; do command -v "$command" >/dev/null || fail "missing dependency $command"; done
docker info >/dev/null || fail 'Docker unavailable'
docker build -t "$image_tag" "$root" >"$work/build.log" 2>&1 || fail 'scheduler image build failed'
image_ref=$(docker image inspect --format '{{index .RepoDigests 0}}' "$image_tag")
[[ "$image_ref" =~ ^[a-z0-9][a-z0-9._/-]*@sha256:[a-f0-9]{64}$ ]] || fail 'local image has no canonical digest reference'
docker network create "$network" >/dev/null

owner="owner_$suffix"
password="p$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
printf 'postgres://%s:%s@%s:5432/nerocd?sslmode=disable\n' "$owner" "$password" "$source" >"$work/source-url"
printf 'postgres://%s:%s@%s:5432/nerocd?sslmode=disable\n' "$owner" "$password" "$target" >"$work/target-url"
chmod 0400 "$work/source-url" "$work/target-url"
mkdir -m 0700 "$work/backups"

postgres_image='postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94'
for container in "$source" "$target"; do
  docker run -d --name "$container" --network "$network" \
    -e POSTGRES_DB=nerocd -e POSTGRES_USER="$owner" -e POSTGRES_PASSWORD="$password" \
    "$postgres_image" >/dev/null
  ready=false
  for _ in {1..40}; do
    if docker exec "$container" pg_isready -U "$owner" -d nerocd >/dev/null 2>&1; then ready=true; break; fi
    sleep 1
  done
  [[ "$ready" == true ]] || { docker logs "$container" 2>&1 | tail -n 30 >>"$evidence" || true; fail "PostgreSQL did not become ready: $container"; }
done

run_owner(){
  local url_file=$1
  shift
  docker run --rm --user "$(id -u):$(id -g)" --network "$network" \
    -e NEROCD_MODE=production -e NEROCD_IMAGE_REF="$image_ref" \
    -e NEROCD_DATABASE_CREDENTIAL=owner -e NEROCD_OWNER_DATABASE_USER="$owner" \
    -e NEROCD_DATABASE_URL_FILE=/runtime/owner-url -v "$url_file:/runtime/owner-url:ro" "$@"
}
run_owner "$work/source-url" "$image_ref" migrate --seed=false >"$work/migrate.log" 2>&1 || fail 'source migration failed'

scheduler_args=(backup-scheduler --output-dir /backups --enabled=true --interval-seconds 60 --retention-count 1 --once)
run_scheduler(){
  run_owner "$work/source-url" -v "$work/backups:/backups" "$image_ref" "${scheduler_args[@]}"
}
schedule_due(){ docker exec "$source" psql -v ON_ERROR_STOP=1 -U "$owner" -d nerocd -c "UPDATE backup_schedule SET enabled=true, next_run_at=clock_timestamp() WHERE singleton" >/dev/null; }
schedule_counts(){ docker exec "$source" psql -At -U "$owner" -d nerocd -c "SELECT count(*) FILTER (WHERE status='succeeded') || ':' || count(*) FILTER (WHERE status='failed') || ':' || count(*) FILTER (WHERE status='running') FROM backup_schedule_runs"; }
load_archives(){
  archives=()
  while IFS= read -r archive; do archives+=("$archive"); done < <(find "$work/backups" -mindepth 1 -maxdepth 1 -type d -name 'backup-*' -print | sort)
}

# First due run proves the production scheduler writes a secure archive and
# advances its durable DB-clock schedule.
run_scheduler >"$work/first.log" 2>&1 || fail 'first scheduled backup failed'
[[ "$(schedule_counts)" == '1:0:0' ]] || fail 'first scheduler ledger was not exactly one success'
load_archives
[[ ${#archives[@]} == 1 && -f "${archives[0]}/database.dump" && -f "${archives[0]}/manifest.json" ]] || fail 'first archive was not atomically published'
record 'first_due_success=true db_clock_advance=true archive_published=true'

# A restart at the unchanged durable next_run_at must not duplicate an archive.
run_scheduler >"$work/restart.log" 2>&1 || fail 'scheduler restart decision failed'
[[ "$(schedule_counts)" == '1:0:0' ]] || fail 'restart duplicated a not-due run'
record 'restart_no_duplicate=true'

# Two independent processes contend on a session advisory lock. Exactly one
# obtains the due work; the other exits after observing the lock held.
schedule_due
for item in a b; do
  name_var="schedule_$item"
  docker run -d --name "${!name_var}" --user "$(id -u):$(id -g)" --network "$network" \
    -e NEROCD_MODE=production -e NEROCD_IMAGE_REF="$image_ref" -e NEROCD_DATABASE_CREDENTIAL=owner -e NEROCD_OWNER_DATABASE_USER="$owner" \
    -e NEROCD_DATABASE_URL_FILE=/runtime/owner-url -v "$work/source-url:/runtime/owner-url:ro" -v "$work/backups:/backups" \
    "$image_ref" "${scheduler_args[@]}" >/dev/null
done
code_a=$(docker wait "$schedule_a")
code_b=$(docker wait "$schedule_b")
[[ "$code_a" == 0 && "$code_b" == 0 ]] || fail 'overlap scheduler process failed'
[[ "$(schedule_counts)" == '2:0:0' ]] || fail 'advisory lock admitted overlapping scheduled backups'
load_archives
[[ ${#archives[@]} == 1 ]] || fail 'rotation did not retain exactly one verified archive'
record 'overlap_single_admission=true retention_one_verified_archive=true'

# The malformed oldest directory is deliberately descriptor-valid but cannot
# be removed; this exercises a post-publication rotation failure and durable
# exponential backoff without mocking pg_dump.
mkdir -m 0700 "$work/backups/backup-00000000T000000Z-malformed"
printf x >"$work/backups/backup-00000000T000000Z-malformed/database.dump"
printf x >"$work/backups/backup-00000000T000000Z-malformed/manifest.json"
chmod 0600 "$work/backups/backup-00000000T000000Z-malformed"/*
printf x >"$work/backups/backup-00000000T000000Z-malformed/unexpected"
chmod 0600 "$work/backups/backup-00000000T000000Z-malformed/unexpected"
schedule_due
if run_scheduler >"$work/failure.log" 2>&1; then fail 'rotation failure unexpectedly succeeded'; fi
state=$(docker exec "$source" psql -At -U "$owner" -d nerocd -c "SELECT consecutive_failures || ':' || (next_run_at > clock_timestamp()) FROM backup_schedule WHERE singleton")
record "rotation_failure_state=$state schedule_counts=$(schedule_counts)"
[[ "$state" == '1:true' ]] || fail 'failure did not persist bounded backoff'
[[ "$(schedule_counts)" == '2:1:0' ]] || fail 'failed schedule run was not durably recorded'
record 'rotation_failure_backoff=true no_running_schedule_row=true'

# Recovery runs only after a new DB-clock due point; it removes all verified
# old archives, resets failures, and leaves a single restoreable archive.
rm -rf -- "$work/backups/backup-00000000T000000Z-malformed"
schedule_due
run_scheduler >"$work/recovery.log" 2>&1 || fail 'scheduler did not recover after rotation repair'
state=$(docker exec "$source" psql -At -U "$owner" -d nerocd -c "SELECT consecutive_failures FROM backup_schedule WHERE singleton")
[[ "$state" == 0 && "$(schedule_counts)" == '3:1:0' ]] || fail 'successful recovery did not reset failure state'
load_archives
[[ ${#archives[@]} == 1 ]] || fail 'recovery retention did not leave one archive'

# The surviving scheduler-created archive must restore through the unchanged
# production command into a separately clean database.
run_owner "$work/target-url" -v "${archives[0]}:/restore:ro" "$image_ref" restore --input-dir /restore >"$work/restore.log" 2>&1 || fail 'scheduler archive did not restore into clean target'
tables=$(docker exec "$target" psql -At -U "$owner" -d nerocd -c "SELECT count(*) FROM pg_tables WHERE schemaname='public'")
[[ "$tables" -gt 0 ]] || fail 'restored target has no public schema'
record 'failure_recovery=true scheduler_archive_restore=true clean_target_schema_restored=true'

# No secret or archive path is copied to durable scheduler rows or evidence.
rows=$(docker exec "$source" psql -At -U "$owner" -d nerocd -c "SELECT coalesce(string_agg(reason, ','),'') FROM backup_schedule_runs")
[[ "$rows" =~ ^[a-z,]+$ ]] || fail 'scheduler row contains an unbounded reason'
! grep -Fq "$password" "$evidence" "$work"/*.log || fail 'scheduler evidence disclosed owner credential'
pass=true
