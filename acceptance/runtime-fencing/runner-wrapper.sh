#!/bin/sh
set -eu

runner_name=$1
shift
while [ ! -e /state/start-runners ]; do
  sleep 0.1
done
"$@" &
runner_pid=$!
set +e
wait "$runner_pid"
runner_status=$?
set -e
printf '%s\n' "$runner_status" > "/state/${runner_name}.exit"
while :; do
  sleep 3600
done
