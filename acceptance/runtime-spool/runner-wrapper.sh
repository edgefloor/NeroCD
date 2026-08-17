#!/bin/sh
set -eu

"$@" &
runner_pid=$!
set +e
wait "$runner_pid"
runner_status=$?
set -e
printf '%s\n' "$runner_status" > /state/runner.exit
while :; do
  sleep 3600
done
