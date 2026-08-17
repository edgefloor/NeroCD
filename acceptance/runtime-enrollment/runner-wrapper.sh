#!/bin/sh
set -eu
slot=${RUNNER_SLOT:?}
rm -f "/state/runner-${slot}.exit"
while [ ! -e /state/release-runners ]; do sleep 0.05; done
set -- nerocd runner --server http://proxy:8081 --credential-file /identity/credential --journal-dir /journal --id runner_enrollment_runtime --tags enrollment-runtime --capabilities shell --poll-interval 1s --cancel-poll-interval 1s --work-dir /work
if [ -e /identity/enrollment ]; then
  set -- "$@" --enrollment-file /identity/enrollment
fi
"$@" &
runner_pid=$!
set +e
wait "$runner_pid"
status=$?
set -e
printf '%s\n' "$status" > "/state/runner-${slot}.exit.next"
mv "/state/runner-${slot}.exit.next" "/state/runner-${slot}.exit"
while :; do sleep 3600; done
