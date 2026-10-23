#!/bin/bash
set -euo pipefail
# The host helper checks Docker configuration before starting this process.
# This mount is supplied by Docker in its private cgroup namespace, never bound.
[[ $(cat /proc/1/cgroup) == '0::/' ]] || { echo 'BLOCKED: cgroup namespace is not rooted at /'; exit 1; }
[[ $(stat -f -c %T /sys/fs/cgroup) == cgroup2fs ]] || { echo 'BLOCKED: no provided cgroup2 mount'; exit 1; }
[[ $(readlink /proc/1/ns/cgroup) != '' ]]
findmnt -n -t cgroup2 -o TARGET | grep -qx /sys/fs/cgroup
printf 'private cgroup namespace=%s membership=%s\n' "$(readlink /proc/1/ns/cgroup)" "$(cat /proc/1/cgroup)"
findmnt -n -t cgroup2 -o TARGET,SOURCE,FSROOT,OPTIONS
mount -o remount,rw /sys/fs/cgroup || { echo 'BLOCKED: private provided cgroup cannot be made writable'; exit 1; }
[[ -w /sys/fs/cgroup/cgroup.procs ]] || { echo 'BLOCKED: no writable delegated cgroup'; exit 1; }
chmod 0700 /root/.ssh
chown root:root /root/.ssh /fixture/host /root/.ssh/authorized_keys
chmod 0600 /fixture/host /root/.ssh/authorized_keys
exec /lib/systemd/systemd --unit=multi-user.target
