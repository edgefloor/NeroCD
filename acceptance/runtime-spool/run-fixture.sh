#!/bin/sh
set -eu

index=1
while [ "$index" -le 18 ]; do
	if [ "$index" -eq 1 ]; then
		date +%s > /state/first-event-epoch
	fi
  printf 'spool-event-%03d\n' "$index"
  printf '%s\n' "$index" > /state/fixture-progress.next
  mv /state/fixture-progress.next /state/fixture-progress
  index=$((index + 1))
  sleep 1
done
printf '%s\n' revision-spooled > /state/revision.next
mv /state/revision.next /state/revision
