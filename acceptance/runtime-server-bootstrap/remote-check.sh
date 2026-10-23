#!/bin/bash
set -euo pipefail
response_command_allowed() {
 local expected actual
 [[ $(stat -c '%u:%a' /fixture/owned-command.sha256) == 0:600 && ! -L /fixture/owned-command.sha256 ]] || return 1
 expected=$(cat /fixture/owned-command.sha256)
 [[ "$expected" =~ ^[a-f0-9]{64}$ ]] || return 1
 actual=$(printf %s "${SSH_ORIGINAL_COMMAND-}" | sha256sum | cut -d' ' -f1)
 [[ "$actual" == "$expected" ]]
}
if [[ ${1-} == response-loss ]]; then
 response_command_allowed || exit 126
 umask 077
 exec /usr/bin/timeout --signal=TERM --kill-after=1s 132s /bin/bash "$0" response-loss-child
fi
if [[ ${1-} == response-loss-child ]]; then
 umask 077
 ulimit -f 16
 response_command_allowed || exit 126
 [[ $(stat -c '%u:%a' /fixture/owned-request.sha256) == 0:600 && ! -L /fixture/owned-request.sha256 ]]
 expected=$(cat /fixture/owned-request.sha256)
 [[ "$expected" =~ ^[a-f0-9]{64}$ ]]
 request=$(dd bs=1 count=6145 2>/dev/null)
 [[ ${#request} -le 6144 ]]
 actual=$(printf %s "$request" | sha256sum | cut -d' ' -f1)
 [[ "$actual" == "$expected" ]] || exit 126
 (set -C; printf '%s\n' "$PPID" >/fixture/response-loss.timeout.pid)
 (set -C; printf '%s\n' "$$" >/fixture/response-loss.child.pid)
 set +e
 (set -C; printf %s "$request" | /bin/sh -c "$SSH_ORIGINAL_COMMAND" >/fixture/response-loss.public 2>&1)
 child_exit=$?
 set -e
 unset request
 (set -C; printf '%s\n' "$child_exit" >/fixture/response-loss.exit)
 [[ "$child_exit" == 0 && $(stat -c %s /fixture/response-loss.public) -le 16384 ]]
 grep -q '"installation_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"' /fixture/response-loss.public
 grep -q '"operation_id":"dddddddddddddddddddddddddddddddd"' /fixture/response-loss.public
 grep -q '"state":"installed"' /fixture/response-loss.public
 (set -C; printf 'committed\n' >/fixture/response-loss.committed)
 sleep 5
 cat /fixture/response-loss.public
 exit 0
fi
[[ $(cat /proc/1/comm) == systemd ]]
printf 'PID1=%s arch=%s\n' "$(readlink /proc/1/exe)" "$(uname -m)"
cat /etc/os-release
systemctl --version | head -1
sshd -V 2>&1
docker version --format 'private Docker={{.Server.Version}} API={{.Server.APIVersion}} arch={{.Server.Arch}}'
[[ $(docker version --format '{{.Server.APIVersion}}') == 1.54 ]]
docker load -i /fixture/sentinel.tar >/dev/null
image=$1
[[ $(docker image inspect --format '{{.Id}}' "$image") == "$image" ]]
sentinel=$(docker run -d --network none --name unrelated-sentinel --entrypoint /bin/sleep "$image" infinity)
service_pid=$(systemctl show fixture-sentinel -p MainPID --value)
before=$(sha256sum /fixture/sentinel)
systemctl start fixture-marker
systemctl is-active --quiet fixture-marker
[[ $(wc -l </fixture/marker) == 1 ]]
systemctl restart fixture-marker
systemctl is-active --quiet fixture-marker
[[ $(wc -l </fixture/marker) == 2 ]]
systemctl stop fixture-marker
[[ $(systemctl is-active fixture-marker || true) == inactive ]]
[[ $(sha256sum /fixture/sentinel) == "$before" ]]
[[ $(systemctl show fixture-sentinel -p MainPID --value) == "$service_pid" ]]
systemctl is-active --quiet fixture-sentinel
[[ $(docker inspect --format '{{.Id}} {{.State.Running}}' unrelated-sentinel) == "$sentinel true" ]]
docker inspect --format 'unrelated sentinel mounts={{json .Mounts}}' unrelated-sentinel
printf 'marker start/status/restart/stop via SSH passed; unrelated file/service/container preserved id=%s\n' "$sentinel"
