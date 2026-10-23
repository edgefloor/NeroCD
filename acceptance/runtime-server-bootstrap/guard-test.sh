#!/usr/bin/env bash
# Execute the actual sourceable cleanup/recovery helper, including || callers.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/qualify.sh"
owner=fixture-test
label=io.nerocd.server-bootstrap.owner
mutations=0
scenario=ok
docker() {
 local operation="$1 $2"
 case "$operation" in
 'container inspect')
  case "$scenario" in
   inspect-failure) printf 'full-id /fixture-test fixture-test\n'; return 1;;
   wrong-id) printf 'other-id /fixture-test fixture-test\n';;
   wrong-owner) printf 'full-id /fixture-test other-owner\n';;
   wrong-name) printf 'full-id /other-name fixture-test\n';;
   *) printf 'full-id /fixture-test fixture-test\n';;
  esac;;
 'volume inspect')
  case "$scenario" in
   inspect-failure) printf 'fixture-test-data fixture-test\n'; return 1;;
   wrong-owner) printf 'fixture-test-data other-owner\n';;
   wrong-name) printf 'other-volume fixture-test\n';;
   *) printf 'fixture-test-data fixture-test\n';;
  esac;;
 'network inspect')
  case "$scenario" in
   inspect-failure) printf 'network-id fixture-test-net fixture-test\n'; return 1;;
   wrong-id) printf 'other-id fixture-test-net fixture-test\n';;
   wrong-owner) printf 'network-id fixture-test-net other-owner\n';;
   *) printf 'network-id fixture-test-net fixture-test\n';;
  esac;;
 'image inspect')
  case "$scenario" in
   inspect-failure) printf 'image-id fixture-test fixture-test:fixture\n'; return 1;;
   wrong-id) printf 'other-id fixture-test fixture-test:fixture\n';;
   wrong-owner) printf 'image-id other-owner fixture-test:fixture\n';;
   wrong-name) printf 'image-id fixture-test other:fixture\n';;
   *) printf 'image-id fixture-test fixture-test:fixture\n';;
  esac;;
 'container rm'|'volume rm'|'network rm'|'image rm') mutations=$((mutations+1)); [[ "$scenario" != removal-failure ]];;
 'container ls'|'volume ls'|'network ls'|'image ls')
  [[ "$scenario" != readback-failure ]] || return 1
  if [[ "$scenario" == still-present ]]; then printf 'still-present\n'; fi;;
 *) echo "unexpected fake Docker call $operation" >&2; return 1;;
 esac
}
check_cleanup() {
 local kind=$1 id=$2 name=$3 mode=$4 expected_mutations=$5 outcome=0
 scenario=$mode; mutations=0
 remove_owned "$kind" "$id" "$name" >/dev/null || outcome=$?
 [[ "$outcome" != 0 && "$mutations" == "$expected_mutations" ]] || { echo "FAIL $kind/$mode mutations=$mutations result=$outcome"; exit 1; }
 printf 'PASS fail-closed %s/%s mutations=%s\n' "$kind" "$mode" "$mutations"
}
for kind in container network image; do
 case "$kind" in container) id=full-id; name=fixture-test;; network) id=network-id; name=fixture-test-net;; image) id=image-id; name=fixture-test;; esac
 for mode in inspect-failure wrong-id wrong-owner; do check_cleanup "$kind" "$id" "$name" "$mode" 0; done
 if [[ "$kind" != network ]]; then check_cleanup "$kind" "$id" "$name" wrong-name 0; fi
 for mode in removal-failure readback-failure still-present; do check_cleanup "$kind" "$id" "$name" "$mode" 1; done
 scenario=ok; mutations=0; remove_owned "$kind" "$id" "$name" >/dev/null
 [[ "$mutations" == 1 ]]
done
for mode in inspect-failure wrong-owner wrong-name; do check_cleanup volume fixture-test-data fixture-test-data "$mode" 0; done
for mode in removal-failure readback-failure still-present; do check_cleanup volume fixture-test-data fixture-test-data "$mode" 1; done
scenario=ok; mutations=0; remove_owned volume fixture-test-data fixture-test-data >/dev/null
[[ "$mutations" == 1 ]]
for kind in container network volume; do
 case "$kind" in container) name=fixture-test; expected=full-id;; network) name=fixture-test-net; expected=network-id;; volume) name=fixture-test-data; expected=fixture-test-data;; esac
 scenario=ok
 [[ $(recover_owned "$kind" "$name") == "$expected" ]]
 for scenario in inspect-failure wrong-owner; do
  outcome=0; recover_owned "$kind" "$name" >/dev/null || outcome=$?
  [[ "$outcome" != 0 ]]
 done
done
echo 'PASS actual cleanup and create-response-loss recovery helper guards'
