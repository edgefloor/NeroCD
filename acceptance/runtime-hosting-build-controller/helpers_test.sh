#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/helpers.sh"

mode=socket_only
docker() {
 if [[ "$*" == *'psql '* ]]; then
  [[ "$*" == *'--host=127.0.0.1'* && "$*" == *'--port=5432'* && "$*" == *'--dbname=nerocd_build_controller'* && "$*" == *'--no-psqlrc'* && "$*" == *'--set=ON_ERROR_STOP=1'* ]] || return 99
  case "$mode" in
   socket_only) return 1 ;;
   wrong_database) printf 'postgres\n' ;;
   ready) printf 'nerocd_build_controller\n' ;;
  esac
  return
 fi
 if [[ "$*" == *'image inspect --format {{.Id}}'* ]]; then
  if [[ "$mode" == missing ]]; then return 1; fi
  if [[ "$mode" == changed_id && "$1" == exec ]]; then
   printf 'sha256:%064d\n' 2
  else printf 'sha256:%064d\n' 1; fi
 elif [[ "$*" == *'image inspect --format {{.Os}}/{{.Architecture}}'* ]]; then
  if [[ "$mode" == wrong_platform ]]; then printf 'linux/amd64\n'; else printf 'linux/arm64\n'; fi
 elif [[ "$*" == *'image inspect --format {{range .RepoDigests}}'* ]]; then
  if [[ "$mode" == wrong_digest ]]; then printf 'postgres@sha256:wrong\n'
  else printf 'postgres@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94\n'; fi
 elif [[ "$*" == *'image save'* ]]; then printf 'owned archive bytes'
 elif [[ "$*" == *'image load'* ]]; then cat >/dev/null
 else echo 'unexpected helper command' >&2; return 99
 fi
}

# A successful Unix-socket init server must not satisfy the TCP probe.
if controller_postgres_ready outer pg; then echo 'temporary server admitted' >&2; exit 1; fi
mode=wrong_database
if controller_postgres_ready outer pg; then echo 'wrong database admitted' >&2; exit 1; fi
mode=ready
controller_postgres_ready outer pg
[[ $(controller_postgres_image outer arm64 0) == 'postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94' ]]
[[ $(controller_postgres_image outer arm64 1) == "sha256:$(printf '%064d' 1)" ]]
for mode in missing wrong_digest wrong_platform changed_id; do
 if controller_postgres_image outer arm64 1 >/dev/null 2>&1; then echo "invalid cache admitted: $mode" >&2; exit 1; fi
done
if controller_postgres_image outer arm64 invalid >/dev/null 2>&1; then exit 1; fi
printf 'controller fixture helper checks passed\n'
