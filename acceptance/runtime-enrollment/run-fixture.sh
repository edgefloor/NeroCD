#!/bin/sh
set -eu
printf '%s\n' "$$" > /state/enrollment.leader.pid
/bin/sh -c 'trap "" TERM INT; printf "%s\n" "$$" > /state/enrollment.child.pid; while :; do sleep 1; done' &
printf '%s\n' "$!" > /state/enrollment.spawned.pid
: > /state/enrollment.process.started
while :; do
  printf 'enrollment-run-live\n'
  sleep 1
done
