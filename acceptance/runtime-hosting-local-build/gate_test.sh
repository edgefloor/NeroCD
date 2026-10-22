#!/bin/bash
# Exercise the real gate command builder without starting Docker or a fixture.
set -euo pipefail
repo_root=$(pwd -P)
gate="$repo_root/acceptance/runtime-hosting-local-build/gate.sh"
umask 077
test_dir=$(mktemp -d /tmp/nerocd-local-build-args.XXXXXX)
trap 'rm -rf "$test_dir"' EXIT
# Source only the production function, not the gate's expensive setup commands.
awk '/^run_fixture_container\(\) \{$/ { found=1 } found { print } found && /^}$/ { exit }' "$gate" > "$test_dir/container-function.sh"
source "$test_dir/container-function.sh"
declare -F run_fixture_container >/dev/null
printf '%s\n' '#!/bin/sh' 'set -eu' 'printf "%s\0" "$@" > "$NEROCD_TEST_ARGUMENTS"' 'printf "%s\n" "fixture-container-id"' > "$test_dir/docker"
chmod 0700 "$test_dir/docker"
export PATH="$test_dir:$PATH"
# Spaces prove that the actual gate preserves each mount argument as one value.
repo_root="/fixture/NeroCD root with spaces"
owner="fixture-owner"
name="fixture-owner-dind"
image="fixture-owner:fixture"
for browser_mode in 0 1; do
 export NEROCD_TEST_ARGUMENTS="$test_dir/arguments-$browser_mode"
 container_id=$(run_fixture_container)
 [[ "$container_id" == fixture-container-id ]]
 python3 -c '
import pathlib,sys
mode=sys.argv[2]
raw=pathlib.Path(sys.argv[1]).read_bytes()
assert raw.endswith(b"\0"), "argument framing was lost"
actual=[value.decode() for value in raw[:-1].split(b"\0")]
expected=["run"]
if mode=="1": expected += ["--publish", "127.0.0.1::18080"]
expected += ["--detach", "--privileged", "--pid=host", "--mount", "type=bind,src=/fixture/NeroCD root with spaces/acceptance/runtime-proxy-control/pebble.json,dst=/workspace/acceptance/runtime-proxy-control/pebble.json,readonly", "--add-host", "example.com:127.0.0.1", "--name", "fixture-owner-dind", "--label", "io.nerocd.runtime-hosting-local-build.owner=fixture-owner", "--env", "DOCKER_TLS_CERTDIR=", "fixture-owner:fixture", "dockerd", "--host=unix:///var/run/docker.sock"]
assert actual==expected, f"mode {mode}: actual arguments {actual!r} != expected {expected!r}"
assert "" not in actual, "spurious empty Docker argument"
print(f"mode {mode}: exact Docker arguments passed")
' "$NEROCD_TEST_ARGUMENTS" "$browser_mode"
done
printf 'gate argument regression passed with Bash %s\n' "$BASH_VERSION"
