#!/bin/bash
set -euo pipefail
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
