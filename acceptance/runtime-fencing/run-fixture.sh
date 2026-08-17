#!/bin/sh
set -eu

if mkdir /state/attempt1.once 2>/dev/null; then
  printf '%s\n' "$$" > /state/attempt1.leader.pid
  /bin/sh -c '
    trap "" TERM INT
    printf "%s\n" "$$" > /state/attempt1.descendant.pid
    counter=0
    while :; do
      counter=$((counter + 1))
      printf "%s %s\n" "$counter" "$(date +%s)" > /state/liveness.next
      mv /state/liveness.next /state/liveness
      sleep 1
    done
  ' &
  printf '%s\n' "$!" > /state/attempt1.child.pid
  : > /state/attempt1.ready
  while :; do
	  printf 'attempt1-live-%s\n' "$(date +%s)"
	  sleep 1
  done
fi

printf '%s\n' revision-b > /state/revision.next
mv /state/revision.next /state/revision
: > /state/attempt2.reconciled
while [ ! -e /state/allow-attempt2-complete ]; do
  sleep 0.1
done
