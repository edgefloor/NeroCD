#!/usr/bin/env bash
# Functions only: sourcing this file starts no fixture or network operation.

controller_postgres_image() {
 local outer=$1 goarch=$2 preload=$3
 local pin='postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94'
 local digest='postgres@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94'
 local expected_id expected_platform loaded_id loaded_platform
 case "$preload" in
  0) printf '%s\n' "$pin"; return ;;
  1) ;;
  *) echo 'invalid PostgreSQL preload option' >&2; return 1 ;;
 esac
 expected_id=$(docker image inspect --format '{{.Id}}' "$pin") || return 1
 expected_platform=$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$pin") || return 1
 [[ "$expected_id" =~ ^sha256:[0-9a-f]{64}$ && "$expected_platform" == "linux/$goarch" ]] || return 1
 docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$pin" | grep -Fx "$digest" >&2 || return 1
 # Save the verified image identity, not a mutable tag. Loading may omit
 # RepoDigests; compare the retained identity/platform rather than inventing one.
 docker image save "$expected_id" | docker exec -i "$outer" docker image load >&2 || return 1
 loaded_id=$(docker exec "$outer" docker image inspect --format '{{.Id}}' "$expected_id") || return 1
 loaded_platform=$(docker exec "$outer" docker image inspect --format '{{.Os}}/{{.Architecture}}' "$expected_id") || return 1
 [[ "$loaded_id" == "$expected_id" && "$loaded_platform" == "$expected_platform" ]] || return 1
 printf 'verified PostgreSQL cache original_pin=%s image_id=%s platform=%s\n' "$pin" "$loaded_id" "$loaded_platform" >&2
 printf '%s\n' "$loaded_id"
}

controller_postgres_ready() {
 local outer=$1 pg=$2 answer
 # The image's temporary init server accepts Unix sockets but disables TCP.
 # Query over TCP and require the intended database, not just a live socket.
 answer=$(docker exec "$outer" docker exec --env PGPASSWORD=isolated_fixture_only --env PGCONNECT_TIMEOUT=2 --env 'PGOPTIONS=-c statement_timeout=2000' "$pg" psql --host=127.0.0.1 --port=5432 --username=nerocd --dbname=nerocd_build_controller --no-psqlrc --tuples-only --no-align --set=ON_ERROR_STOP=1 --command='SELECT current_database()') || return 1
 [[ "$answer" == 'nerocd_build_controller' ]]
}
