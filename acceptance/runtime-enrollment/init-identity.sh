#!/bin/sh
set -eu
umask 077
IFS= read -r enrollment
test -n "$enrollment"
chmod 0700 /identity /journal /state
chown 10001:10001 /identity /journal /state
printf '%s\n' "$enrollment" > /identity/enrollment
chmod 0600 /identity/enrollment
chown 10001:10001 /identity/enrollment
