#!/usr/bin/env bash
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
gate="$root/scripts/production-profile-gate.sh"
temp_root=$(mktemp -d /tmp/nerocd-production-profile-redaction.XXXXXXXX)

cleanup() {
  local code=$?
  rm -rf -- "$temp_root"
  exit "$code"
}
trap cleanup EXIT

owner_password='owner-secret-sentinel-9f4c'
app_password='app-secret-sentinel-72bd'
input="$temp_root/diagnostics.txt"
output="$temp_root/redacted.txt"
printf 'up output postgres://owner:%s@postgres/nerocd\nserver received %s\n' "$owner_password" "$app_password" >"$input"

bash "$gate" --redact-stdin "$owner_password" "$app_password" <"$input" >"$output"

if rg -Fq "$owner_password" "$output" || rg -Fq "$app_password" "$output"; then
  printf '%s\n' 'production profile redaction test: sentinel secret remained in diagnostics' >&2
  exit 1
fi
rg -Fqx 'up output postgres://owner:[REDACTED_OWNER_PASSWORD]@postgres/nerocd' "$output"
rg -Fqx 'server received [REDACTED_APP_PASSWORD]' "$output"

for line in {1..82}; do
  printf 'compose-up-line-%03d\n' "$line" >>"$input"
done
printf 'compose-up-line-083 owner=%s app=%s\n' "$owner_password" "$app_password" >>"$input"
bash "$gate" --redact-tail-stdin "$owner_password" "$app_password" <"$input" >"$output"
! rg -Fq 'compose-up-line-001' "$output"
! rg -Fq 'compose-up-line-003' "$output"
rg -Fq 'compose-up-line-004' "$output"
rg -Fqx 'compose-up-line-083 owner=[REDACTED_OWNER_PASSWORD] app=[REDACTED_APP_PASSWORD]' "$output"
if rg -Fq "$owner_password" "$output" || rg -Fq "$app_password" "$output"; then
  printf '%s\n' 'production profile redaction test: bounded diagnostics retained a sentinel secret' >&2
  exit 1
fi

mock_bin="$temp_root/mock-bin"
cleanup_trace="$temp_root/cleanup-trace.txt"
mkdir "$mock_bin"
cat >"$mock_bin/docker" <<'EOF'
#!/bin/sh
printf 'docker %s\n' "$*" >>"$NEROCD_CLEANUP_TEST_TRACE"
case "${NEROCD_CLEANUP_TEST_MODE:-success}:$*" in
  survivor:ps\ -aq\ --filter\ *) printf '%s\n' surviving-container ;;
  survivor:rm\ -f\ surviving-container) exit 1 ;;
  scan-error:ps\ -aq\ --filter\ *) exit 1 ;;
  registry-success:image\ inspect\ *)
    if rg -Fqx "docker image rm $3" "$NEROCD_CLEANUP_TEST_TRACE"; then printf '%s\n' 'Error: No such object' >&2; exit 1; fi
    printf '%s\n' '{"present":true}' ;;
  registry-success:inspect\ *)
    if rg -Fqx "docker rm -f $2" "$NEROCD_CLEANUP_TEST_TRACE"; then printf '%s\n' 'Error: No such container' >&2; exit 1; fi
    printf '%s\n' '{"present":true}' ;;
  registry-survivor:image\ rm\ *) exit 1 ;;
  registry-survivor:rm\ -f\ *) exit 1 ;;
  registry-survivor:image\ inspect\ *) printf '%s\n' '{"present":true}' ;;
  registry-survivor:inspect\ *) printf '%s\n' '{"present":true}' ;;
esac
exit 0
EOF
chmod +x "$mock_bin/docker"
cat >"$mock_bin/rm" <<'EOF'
#!/bin/sh
case "${NEROCD_CLEANUP_TEST_MODE:-success}" in
  dir-rm-failure) exit 1 ;;
  dir-survivor) exit 0 ;;
esac
exec /bin/rm "$@"
EOF
chmod +x "$mock_bin/rm"
if PATH="$mock_bin:$PATH" NEROCD_CLEANUP_TEST_TRACE="$cleanup_trace" bash "$gate" --cleanup-pre-compose-test "$cleanup_trace"; then
  printf '%s\n' 'production profile cleanup test: pre-compose failure unexpectedly succeeded' >&2
  exit 1
fi
rg -Fqx 'local_registry_cleanup' "$cleanup_trace"
rg -Fq 'docker image rm -f nerocd-production-server:' "$cleanup_trace"
! rg -Fq 'docker compose' "$cleanup_trace"
rg -Fqx 'cleanup_complete=true' /tmp/nerocd-production-profile.txt

: >"$cleanup_trace"
if PATH="$mock_bin:$PATH" NEROCD_CLEANUP_TEST_TRACE="$cleanup_trace" NEROCD_CLEANUP_TEST_MODE=survivor bash "$gate" --cleanup-pre-compose-test "$cleanup_trace"; then
  printf '%s\n' 'production profile cleanup test: surviving resource unexpectedly succeeded' >&2
  exit 1
fi
rg -Fqx 'local_registry_cleanup' "$cleanup_trace"
rg -Fqx 'docker rm -f surviving-container' "$cleanup_trace"
rg -Fqx 'cleanup_complete=false' /tmp/nerocd-production-profile.txt

: >"$cleanup_trace"
if PATH="$mock_bin:$PATH" NEROCD_CLEANUP_TEST_TRACE="$cleanup_trace" NEROCD_CLEANUP_TEST_MODE=scan-error bash "$gate" --cleanup-pre-compose-test "$cleanup_trace"; then
  printf '%s\n' 'production profile cleanup test: failed post-cleanup scan unexpectedly succeeded' >&2
  exit 1
fi
rg -Fq 'docker ps -aq --filter label=com.docker.compose.project=' "$cleanup_trace"
rg -Fqx 'cleanup_complete=false' /tmp/nerocd-production-profile.txt

: >"$cleanup_trace"
if PATH="$mock_bin:$PATH" NEROCD_CLEANUP_TEST_TRACE="$cleanup_trace" NEROCD_CLEANUP_TEST_MODE=registry-cleanup-failure bash "$gate" --cleanup-pre-compose-test "$cleanup_trace"; then
  printf '%s\n' 'production profile cleanup test: local registry cleanup failure unexpectedly succeeded' >&2
  exit 1
fi
rg -Fqx 'local_registry_cleanup' "$cleanup_trace"
rg -Fqx 'cleanup_complete=false' /tmp/nerocd-production-profile.txt

: >"$cleanup_trace"
if PATH="$mock_bin:$PATH" NEROCD_CLEANUP_TEST_TRACE="$cleanup_trace" NEROCD_CLEANUP_TEST_MODE=compose-down-failure bash "$gate" --cleanup-pre-compose-test "$cleanup_trace"; then
  printf '%s\n' 'production profile cleanup test: compose down failure unexpectedly succeeded' >&2
  exit 1
fi
rg -Fqx 'compose down --volumes --remove-orphans --rmi local --timeout 10' "$cleanup_trace"
rg -Fqx 'cleanup_complete=false' /tmp/nerocd-production-profile.txt
! rg -Fq 'PASS: live production profile startup and durable restart gate' /tmp/nerocd-production-profile.txt

for mode in dir-rm-failure dir-survivor; do
  : >"$cleanup_trace"
  if PATH="$mock_bin:$PATH" NEROCD_CLEANUP_TEST_TRACE="$cleanup_trace" NEROCD_CLEANUP_TEST_MODE="$mode" bash "$gate" --cleanup-pre-compose-test "$cleanup_trace"; then
    printf 'production profile cleanup test: %s unexpectedly succeeded\n' "$mode" >&2
    exit 1
  fi
  cleanup_dir=$(awk -F= '/^cleanup_dir=/{print $2}' "$cleanup_trace")
  [[ "$cleanup_dir" =~ ^/tmp/nerocd-production-profile\.[A-Za-z0-9]+$ ]] || { printf '%s\n' 'production profile cleanup test: unsafe cleanup directory' >&2; exit 1; }
  [[ -d "$cleanup_dir" ]] || { printf '%s\n' 'production profile cleanup test: mocked directory did not survive' >&2; exit 1; }
  rg -Fqx 'cleanup_complete=false' /tmp/nerocd-production-profile.txt
  ! rg -Fq 'PASS: live production profile startup and durable restart gate' /tmp/nerocd-production-profile.txt
  /bin/rm -rf -- "$cleanup_dir"
done

registry_helper="$root/scripts/local-image-registry.sh"
: >"$cleanup_trace"
if ! PATH="$mock_bin:$PATH" NEROCD_CLEANUP_TEST_TRACE="$cleanup_trace" NEROCD_CLEANUP_TEST_MODE=registry-success bash -c '
  set -Eeuo pipefail
  source "$1"
  local_registry_container_id=registry-container
  local_registry_source_tag=registry-source
  local_registry_tag=registry-tag
  local_registry_image_ref=registry-image
  local_registry_cleanup
  [[ -z "$local_registry_container_id$local_registry_source_tag$local_registry_tag$local_registry_image_ref" ]]
' bash "$registry_helper"; then
  printf '%s\n' 'local registry cleanup test: successful exact cleanup was rejected' >&2
  exit 1
fi
rg -Fqx 'docker image rm registry-image' "$cleanup_trace"
rg -Fqx 'docker rm -f registry-container' "$cleanup_trace"
: >"$cleanup_trace"
if PATH="$mock_bin:$PATH" NEROCD_CLEANUP_TEST_TRACE="$cleanup_trace" NEROCD_CLEANUP_TEST_MODE=registry-survivor bash -c '
  set -Eeuo pipefail
  source "$1"
  local_registry_container_id=registry-container
  local_registry_source_tag=registry-source
  local_registry_tag=registry-tag
  local_registry_image_ref=registry-image
  if local_registry_cleanup; then exit 1; fi
  [[ "$local_registry_container_id" == registry-container ]]
  [[ "$local_registry_image_ref" == registry-image ]]
' bash "$registry_helper"; then
  printf '%s\n' 'local registry cleanup test: removal failure/survivor was accepted or IDs were cleared' >&2
  exit 1
fi
