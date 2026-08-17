#!/bin/sh
set -eu

test -n "${RUNTIME_SECRET:-}"
test -n "${RUN_MARKER:-}"
test -n "${EXPECTED_SECRET_CLASS:-}"
case "$RUNTIME_SECRET" in
  v1-*) actual_secret_class=v1 ;;
  v2-*) actual_secret_class=v2 ;;
  *) exit 41 ;;
esac
test "$actual_secret_class" = "$EXPECTED_SECRET_CLASS"
printf '%s\n' started > "/state/${RUN_MARKER}.next"
mv "/state/${RUN_MARKER}.next" "/state/${RUN_MARKER}"
if [ "${WAIT_FOR_RELEASE:-false}" = true ]; then
  until [ -e /state/release-output ]; do sleep 0.1; done
fi

printf '%s\n' safe-before-secret
printf '%s' "$RUNTIME_SECRET"
printf '%s\n' ''
printf '%s\n' "$RUNTIME_SECRET" >&2
secret_length=${#RUNTIME_SECRET}
split_at=$((secret_length / 2))
first=$(printf '%s' "$RUNTIME_SECRET" | cut -c "1-${split_at}")
second=$(printf '%s' "$RUNTIME_SECRET" | cut -c "$((split_at + 1))-" )
printf '%s' "$first"
sleep 1
printf '%s\n' "$second"
printf '%s' "$RUNTIME_SECRET" | base64 | tr -d '\n'
printf '\n'
printf '%s' "$RUNTIME_SECRET" | base64 | tr '+/' '-_' | tr -d '=\n'
printf '\n'
printf '%s' "$RUNTIME_SECRET" | od -An -v -tx1 | tr -d ' \n'
printf '\n'
printf '%s\n' safe-after-secret
printf '%s\n' "revision-$actual_secret_class" > "/state/${RUN_MARKER}.revision.next"
mv "/state/${RUN_MARKER}.revision.next" "/state/${RUN_MARKER}.revision"
