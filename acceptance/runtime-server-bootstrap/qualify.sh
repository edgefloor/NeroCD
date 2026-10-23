#!/usr/bin/env bash
set -euo pipefail

# Explicit guards are required: callers deliberately invoke cleanup under ||.
remove_owned() {
 local kind=$1 id=$2 name=$3 identity remaining
 [[ -n "$id" && -n "$name" && -n "$owner" ]] || return 1
 case "$kind" in
 container)
  if ! identity=$(docker container inspect --format "{{.Id}} {{.Name}} {{index .Config.Labels \"$label\"}}" "$id"); then return 1; fi
  [[ "$identity" == "$id /$name $owner" ]] || return 1
  docker container rm --force --volumes "$id" >/dev/null || return 1
  remaining=$(docker container ls --all --quiet --no-trunc --filter "id=$id") || return 1
  ;;
 volume|network)
  if [[ "$kind" == volume ]]; then
   identity=$(docker volume inspect --format "{{.Name}} {{index .Labels \"$label\"}}" "$id") || return 1
   [[ "$id" == "$name" && "$identity" == "$name $owner" ]] || return 1
  else
   identity=$(docker network inspect --format "{{.Id}} {{.Name}} {{index .Labels \"$label\"}}" "$id") || return 1
   [[ "$identity" == "$id $name $owner" ]] || return 1
  fi
  docker "$kind" rm "$id" >/dev/null || return 1
  remaining=$(docker "$kind" ls --quiet --filter "name=^$name$") || return 1
  ;;
 image)
  identity=$(docker image inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}} {{join .RepoTags \",\"}}" "$id") || return 1
  [[ "$identity" == "$id $owner $name:fixture" ]] || return 1
  docker image rm "$id" >/dev/null || return 1
  remaining=$(docker image ls --quiet --no-trunc --filter "label=$label=$owner") || return 1
  ;;
 *) return 1;;
 esac
 [[ -z "$remaining" ]] || return 1
 printf 'removed owned %s id=%s name=%s; successful absence readback\n' "$kind" "$id" "$name"
}

# A failed create may have reached the daemon. Recover only the exact named,
# labelled resource; never treat failed inspect as proof that it is absent.
recover_owned() {
 local kind=$1 name=$2 identity
 case "$kind" in
 container) identity=$(docker container inspect --format "{{.Id}} {{.Name}} {{index .Config.Labels \"$label\"}}" "$name") || return 1
  [[ "$identity" == *" /$name $owner" ]] || return 1;;
 network) identity=$(docker network inspect --format "{{.Id}} {{.Name}} {{index .Labels \"$label\"}}" "$name") || return 1
  [[ "$identity" == *" $name $owner" ]] || return 1;;
 volume) identity=$(docker volume inspect --format "{{.Name}} {{index .Labels \"$label\"}}" "$name") || return 1
  [[ "$identity" == "$name $owner" ]] || return 1;;
 *) return 1;;
 esac
 printf '%s\n' "${identity%% *}"
}

main() {
 image_id=''; container_id=''; network_id=''; volume_id=''; pg_id=''; pg_volume=''; relay_pid=''; result=0
 root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)
 work=$(mktemp -d /tmp/nerocd-server-bootstrap.XXXXXXXX)
 work=$(cd "$work" && pwd -P)
 chmod 0700 "$work"
 # macOS launch environments may deny Bash heredoc files in inherited TMPDIR.
 export TMPDIR="$work"
 owner=$(basename "$work" | tr '[:upper:]' '[:lower:]')
 label=io.nerocd.server-bootstrap.owner
 evidence="$root/.cache/$owner"
 mkdir -p "$evidence"
 donor='docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489'
 cleanup() {
  result=$?; trap - EXIT HUP INT TERM
  if [[ -n "$relay_pid" ]]; then
   kill -TERM "$relay_pid" 2>/dev/null || true
   wait "$relay_pid" || result=1
   cat "$evidence/relay.log"
  fi
  if [[ -n "$container_id" ]]; then remove_owned container "$container_id" "$owner" || result=1; fi
  if [[ -n "$pg_id" ]]; then remove_owned container "$pg_id" "$owner-pg" || result=1; fi
  if [[ -n "$pg_volume" ]]; then remove_owned volume "$pg_volume" "$owner-pgdata" || result=1; fi
  if [[ -n "$volume_id" ]]; then remove_owned volume "$volume_id" "$owner-data" || result=1; fi
  if [[ -n "$network_id" ]]; then remove_owned network "$network_id" "$owner-net" || result=1; fi
  if [[ -n "$image_id" ]]; then remove_owned image "$image_id" "$owner" || result=1; fi
  rm -rf -- "$work"
  printf 'fixture cleanup exit=%s evidence=%s\n' "$result" "$evidence"
  exit "$result"
 }
 trap cleanup EXIT
 trap 'exit 129' HUP; trap 'exit 130' INT; trap 'exit 143' TERM
 exec > >(tee "$evidence/qualification.log") 2>&1
 printf 'fixture-only source=%s owner=%s label=%s\n' "$(git -C "$root" rev-parse HEAD)" "$owner" "$label"
 shasum -a 256 "$root"/acceptance/runtime-server-bootstrap/* >"$evidence/source-sha256.txt"
 shasum -a 256 "$root"/internal/serverbootstrap/*.go "$root/go.mod" "$root/docs/hosting-ssh-bootstrap.md" >>"$evidence/source-sha256.txt"
 shasum -a 256 "$root"/cmd/nerocd/*.go >>"$evidence/source-sha256.txt"
 go version
 go list -m golang.org/x/crypto
 go test -c -o "$work/serverbootstrap.test" ./internal/serverbootstrap
 if [[ ${NEROCD_BOOTSTRAP_ONBOARDING:-} == owned-systemd-target ]]; then
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -tags nerocd_bootstrap_fixture -c -o "$work/api.test" ./internal/api
  shasum -a 256 "$work/api.test" >"$evidence/onboarding-test-sha256.txt"
  python3 -B scripts/skills/ui_manifest.py >"$evidence/ui-manifest.json"
 fi
 shasum -a 256 "$work/serverbootstrap.test" >"$evidence/probe-binary-sha256.txt"
 docker version --format 'host Docker={{.Server.Version}} API={{.Server.APIVersion}} arch={{.Server.Arch}}'
 for key in host client loss-client wrong-client wrong-host; do ssh-keygen -q -t ed25519 -N '' -f "$work/$key"; done
 docker build --label "$label=$owner" --tag "$owner:fixture" "$root/acceptance/runtime-server-bootstrap"
 image_id=$(docker image inspect --format '{{.Id}}' "$owner:fixture")
 # The donor is the pinned native sentinel, saved before runtime isolation.
 docker pull "$donor"
 donor_arch=$(docker image inspect --format '{{.Architecture}}' "$donor")
 [[ "$donor_arch" == arm64 || "$donor_arch" == amd64 ]]
 GOOS=linux GOARCH="$donor_arch" CGO_ENABLED=0 go build -trimpath -o "$work/agent" ./cmd/nerocd
 if [[ ${NEROCD_BOOTSTRAP_ONBOARDING:-} == owned-systemd-target ]]; then
  GOOS=linux GOARCH="$donor_arch" CGO_ENABLED=0 go build -trimpath -ldflags '-X main.version=v0.0.0-fixture' -o "$work/onboarding-agent" ./cmd/nerocd
  shasum -a 256 "$work/onboarding-agent" >"$evidence/onboarding-agent-sha256.txt"
  printf 'onboarding artifact version=v0.0.0-fixture explicit main.version build pin\n' >"$evidence/onboarding-artifact-version.txt"
 fi
 GOOS=linux GOARCH="$donor_arch" CGO_ENABLED=0 go test -c -o "$work/target.test" ./internal/serverbootstrap
 shasum -a 256 "$work/agent" "$work/target.test" >"$evidence/installer-binary-sha256.txt"
 postgres='postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94'
 docker pull "$postgres"
 docker save --platform "linux/$donor_arch" --output "$work/sentinel.tar" "$donor"
 donor_id=$(python3 -B "$root/acceptance/runtime-server-bootstrap/sentinel-config.py" "$work/sentinel.tar" "$donor_arch")
 [[ "$donor_id" =~ ^sha256:[a-f0-9]{64}$ ]]
 printf 'pinned sentinel source=%s native_arch=%s config_id=%s\n' "$donor" "$donor_arch" "$donor_id"
 if ! network_id=$(docker network create --internal --label "$label=$owner" "$owner-net"); then
  network_id=$(recover_owned network "$owner-net") || return 1
  return 1
 fi
 if ! volume_id=$(docker volume create --label "$label=$owner" "$owner-data"); then
  volume_id=$(recover_owned volume "$owner-data") || return 1
  return 1
 fi
 if ! pg_volume=$(docker volume create --label "$label=$owner" "$owner-pgdata"); then
  pg_volume=$(recover_owned volume "$owner-pgdata") || return 1
  return 1
 fi
 if ! pg_id=$(docker create --name "$owner-pg" --label "$label=$owner" --network "$network_id" --env POSTGRES_HOST_AUTH_METHOD=trust --mount "type=volume,src=$pg_volume,dst=/var/lib/postgresql/data" "$postgres"); then
  pg_id=$(recover_owned container "$owner-pg") || return 1
  return 1
 fi
 docker inspect "$pg_id" >"$evidence/postgres-inspect.json"
 docker volume inspect "$pg_volume" >"$evidence/postgres-volume-inspect.json"
 printf 'owned PostgreSQL container=%s volume=%s internal-only trust-auth disposable fixture\n' "$pg_id" "$pg_volume"
 docker start "$pg_id" >/dev/null
 for _ in $(seq 1 120); do if docker exec "$pg_id" pg_isready -U postgres >/dev/null 2>&1; then break; fi; sleep 0.5; done
 if ! docker exec "$pg_id" pg_isready -U postgres; then
  docker logs "$pg_id" >"$evidence/postgres-boot.log" 2>&1
  cat "$evidence/postgres-boot.log"
  return 1
 fi
 pg_address=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$pg_id")
 [[ "$pg_address" =~ ^[0-9.]+$ ]]
 if ! container_id=$(docker create --name "$owner" --label "$label=$owner" --network "$network_id" --cgroupns=private --cap-add SYS_ADMIN --cap-add NET_ADMIN --security-opt seccomp=unconfined --tmpfs /run --tmpfs /run/lock --mount "type=volume,src=$volume_id,dst=/var/lib/docker" "$owner:fixture"); then
  container_id=$(recover_owned container "$owner") || return 1
  return 1
 fi
 docker inspect "$container_id" >"$evidence/container-inspect.json"
 printf 'owned image=%s container=%s network=%s volume=%s\n' "$image_id" "$container_id" "$network_id" "$volume_id"
 python3 "$root/acceptance/runtime-server-bootstrap/check-isolation.py" "$evidence/container-inspect.json" "$volume_id"
 docker network inspect "$network_id" >"$evidence/network-inspect.json"
 python3 -c 'import json,sys; n=json.load(open(sys.argv[1]))[0]; assert n["Id"] == sys.argv[2] and n["Internal"] and n["Driver"] == "bridge"; assert n["Labels"]["io.nerocd.server-bootstrap.owner"] == sys.argv[3]' "$evidence/network-inspect.json" "$network_id" "$owner"
 docker volume inspect "$volume_id" >"$evidence/volume-inspect.json"
 docker cp "$work/host" "$container_id:/fixture/host"
 docker cp "$work/client.pub" "$container_id:/root/.ssh/authorized_keys"
 printf 'command="/fixture/remote-check.sh response-loss",restrict %s\n' "$(cat "$work/loss-client.pub")" >"$work/loss-authorized-key"
 docker cp "$work/loss-authorized-key" "$container_id:/fixture/loss-authorized-key"
 docker cp "$root/acceptance/runtime-server-bootstrap/remote-check.sh" "$container_id:/fixture/remote-check.sh"
 docker cp "$work/sentinel.tar" "$container_id:/fixture/sentinel.tar"
 docker cp "$work/target.test" "$container_id:/fixture/target.test"
 docker cp "$root/db/seeds/dev.sql" "$container_id:/fixture/dev.sql"
 if [[ ${NEROCD_BOOTSTRAP_ONBOARDING:-} == owned-systemd-target ]]; then
  [[ "$donor_arch" == arm64 ]] || { echo 'onboarding policy requires native arm64'; return 1; }
  docker cp "$work/api.test" "$container_id:/fixture/api.test"
  docker cp "$work/onboarding-agent" "$container_id:/fixture/onboarding-agent"
  docker cp "$work/client" "$container_id:/fixture/onboarding-client"
 fi
 docker start "$container_id" >/dev/null
 local ready=false
 for _ in $(seq 1 40); do
  if [[ $(docker inspect --format '{{.State.Running}}' "$container_id") != true ]]; then break; fi
  if docker exec "$container_id" systemctl is-active --quiet ssh && docker exec "$container_id" systemctl is-active --quiet fixture-docker && docker exec "$container_id" systemctl is-active --quiet fixture-sentinel && docker exec "$container_id" docker info >/dev/null 2>&1; then ready=true; break; fi
  sleep 1
 done
 docker logs "$container_id" >"$evidence/boot.log" 2>&1
 if [[ "$ready" != true ]]; then
  cat "$evidence/boot.log"
  docker exec "$container_id" journalctl --unit fixture-docker --unit ssh --no-pager >"$evidence/service-journal.log" || true
  cat "$evidence/service-journal.log"
  echo 'BLOCKED: real systemd/sshd/private Docker did not become ready under frozen isolation'
  return 1
 fi
 docker exec "$container_id" /bin/sh -c 'chown root:root /fixture/remote-check.sh; chmod 0700 /fixture/remote-check.sh; cat /fixture/loss-authorized-key >>/root/.ssh/authorized_keys; rm /fixture/loss-authorized-key'
 docker exec --detach --env NEROCD_BOOTSTRAP_INSTALL_CONTROL=owned-systemd-target --env "NEROCD_BOOTSTRAP_DATABASE_URL=postgresql://postgres@$pg_address/postgres?sslmode=disable" "$container_id" /bin/sh -c 'exec /fixture/target.test -test.v -test.run "^TestInstallerControlFixture$" -test.timeout=160s >/fixture/control.log 2>&1'
 control_ready=false
 for _ in $(seq 1 100); do if docker exec "$container_id" test -f /fixture/control.ready; then control_ready=true; break; fi; sleep 0.2; done
 if [[ "$control_ready" != true ]]; then docker cp "$container_id:/fixture/control.log" "$evidence/control.log"; cat "$evidence/control.log"; return 1; fi
 docker exec "$container_id" /fixture/target.test -test.v -test.run '^TestOwned(FilesystemBoundaries|StopEvidence|UnitState)$' -test.timeout=10s >"$evidence/owned-filesystem.log" 2>&1
 cat "$evidence/owned-filesystem.log"
 docker cp "$container_id:/fixture/enrollment" "$work/enrollment"
 chmod 0600 "$work/enrollment"
 docker cp "$container_id:/fixture/loss-enrollment" "$work/loss-enrollment"
 chmod 0600 "$work/loss-enrollment"
 local address port fingerprint
 python3 -B "$root/acceptance/runtime-server-bootstrap/relay.py" "$container_id" "$owner" "$work/relay-port" >"$evidence/relay.log" 2>&1 &
 relay_pid=$!
 for _ in $(seq 1 50); do
  if [[ -s "$work/relay-port" ]]; then break; fi
  kill -0 "$relay_pid" || return 1
  sleep 0.1
 done
 port=$(cat "$work/relay-port")
 [[ "$port" =~ ^[0-9]+$ ]]
 address="127.0.0.1:$port"
 printf 'owned relay pid=%s transport=fixture-Docker-exec-TCP endpoint=%s\n' "$relay_pid" "$address"
 fingerprint=$(ssh-keygen -lf "$work/host.pub" -E sha256 | awk '{print $2}')
 printf 'SSH endpoint=%s independently pinned host fingerprint=%s\n' "$address" "$fingerprint"
 printf '[127.0.0.1]:%s %s\n' "$port" "$(cat "$work/host.pub")" >"$work/known_hosts"
 printf '[127.0.0.1]:%s %s\n' "$port" "$(cat "$work/wrong-host.pub")" >"$work/wrong_known_hosts"
 local ssh_options=(-F /dev/null -o BatchMode=yes -o IdentitiesOnly=yes -o IdentityAgent=none -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o ConnectTimeout=5 -p "$port")
 if ssh -vv "${ssh_options[@]}" -o "UserKnownHostsFile=$work/wrong_known_hosts" -i "$work/client" root@127.0.0.1 true >"$work/wrong-host.log" 2>&1; then echo 'wrong host accepted'; return 1; fi
 grep -q 'REMOTE HOST IDENTIFICATION HAS CHANGED' "$work/wrong-host.log"
 if grep -q 'Offering public key' "$work/wrong-host.log"; then echo 'client key offered before host verification'; return 1; fi
 printf 'wrong host key rejected before client key offer\n'
 sed "s|$work|<ephemeral-key-directory>|g" "$work/wrong-host.log" >"$evidence/wrong-host.log"
 if ssh "${ssh_options[@]}" -o "UserKnownHostsFile=$work/known_hosts" -i "$work/wrong-client" root@127.0.0.1 true >"$work/wrong-client.log" 2>&1; then echo 'wrong client accepted'; return 1; fi
 grep -q 'Permission denied (publickey)' "$work/wrong-client.log"
 printf 'wrong client key rejected\n'
 cp "$work/wrong-client.log" "$evidence/wrong-client.log"
 if ! ssh "${ssh_options[@]}" -o "UserKnownHostsFile=$work/known_hosts" -i "$work/client" root@127.0.0.1 "bash -se -- $donor_id" <"$root/acceptance/runtime-server-bootstrap/remote-check.sh"; then
  docker exec "$container_id" journalctl --unit ssh --no-pager >"$evidence/ssh-journal.log" || true
  cat "$evidence/ssh-journal.log"
  return 1
 fi
 # Fixed read-only assertions surround the single package integration entry point.
 snapshot_command="sha256sum /fixture/sentinel; systemctl show fixture-sentinel -p MainPID --value; systemctl is-active fixture-sentinel; docker inspect --format '{{.Id}} {{.State.Running}}' unrelated-sentinel; wc -l /fixture/marker; systemctl is-active fixture-marker || test \"\$(systemctl is-active fixture-marker)\" = inactive"
 ssh "${ssh_options[@]}" -o "UserKnownHostsFile=$work/known_hosts" -i "$work/client" root@127.0.0.1 "$snapshot_command" >"$evidence/sentinels-before-probe.txt"
 NEROCD_BOOTSTRAP_PROBE_FIXTURE=owned-systemd-target NEROCD_BOOTSTRAP_PROBE_PORT="$port" NEROCD_BOOTSTRAP_PROBE_KEY_FILE="$work/client" NEROCD_BOOTSTRAP_PROBE_FINGERPRINT="$fingerprint" "$work/serverbootstrap.test" -test.v -test.run '^TestPreparedHostFixture$' -test.timeout=45s >"$evidence/go-probe.log" 2>&1
 cat "$evidence/go-probe.log"
 ssh "${ssh_options[@]}" -o "UserKnownHostsFile=$work/known_hosts" -i "$work/client" root@127.0.0.1 "$snapshot_command" >"$evidence/sentinels-after-probe.txt"
 cmp "$evidence/sentinels-before-probe.txt" "$evidence/sentinels-after-probe.txt"
 printf 'actual Go SSH probe preserves file/service/container/marker sentinels\n'
 # Prepared-host fixture setup only: the product adapter never installs plugins.
 docker exec "$container_id" /bin/sh -c 'mkdir -p /usr/local/libexec/docker/cli-plugins; install -m 0755 /fixture/compose-plugin /usr/local/libexec/docker/cli-plugins/docker-compose; sha256sum /usr/local/libexec/docker/cli-plugins/docker-compose; /usr/local/libexec/docker/cli-plugins/docker-compose version' >"$evidence/compose-profile.txt"
 cat "$evidence/compose-profile.txt"
 NEROCD_BOOTSTRAP_INSTALL_FIXTURE=owned-systemd-target NEROCD_BOOTSTRAP_PROBE_PORT="$port" NEROCD_BOOTSTRAP_PROBE_KEY_FILE="$work/client" NEROCD_BOOTSTRAP_PROBE_FINGERPRINT="$fingerprint" NEROCD_BOOTSTRAP_INSTALL_TOKEN_FILE="$work/enrollment" NEROCD_BOOTSTRAP_INSTALL_LOSS_TOKEN_FILE="$work/loss-enrollment" NEROCD_BOOTSTRAP_INSTALL_LOSS_KEY_FILE="$work/loss-client" NEROCD_BOOTSTRAP_INSTALL_ARTIFACT="$work/agent" NEROCD_BOOTSTRAP_INSTALL_ARCH="$donor_arch" "$work/serverbootstrap.test" -test.v -test.run '^TestInstallerPreparedHostFixture$' -test.timeout=110s >"$evidence/go-installer.log" 2>&1 || { cat "$evidence/go-installer.log"; docker cp "$container_id:/fixture/control.log" "$evidence/control.log"; cat "$evidence/control.log"; return 1; }
 cat "$evidence/go-installer.log"
 docker cp "$container_id:/fixture/response-loss.public" "$evidence/ssh-loss-public.json"
 docker cp "$container_id:/fixture/response-loss.exit" "$evidence/ssh-loss-exit.txt"
 docker exec "$container_id" /bin/sh -c 'for file in /fixture/response-loss.timeout.pid /fixture/response-loss.child.pid; do pid=$(cat "$file"); case "$pid" in ""|*[!0-9]*) exit 1;; esac; for i in $(seq 1 60); do test ! -e "/proc/$pid" && break; sleep 0.1; done; test ! -e "/proc/$pid" || exit 1; printf "fixture response-loss process pid=%s absent\n" "$pid"; done' >"$evidence/ssh-loss-processes.txt"
 cat "$evidence/ssh-loss-processes.txt"
 docker exec "$container_id" touch /fixture/control.finish
 control_done=false
 for _ in $(seq 1 50); do if docker exec "$container_id" test -f /fixture/control.done; then control_done=true; break; fi; sleep 0.2; done
 docker cp "$container_id:/fixture/control.log" "$evidence/control.log"
 cat "$evidence/control.log"
 [[ "$control_done" == true ]] || return 1
 ssh "${ssh_options[@]}" -o "UserKnownHostsFile=$work/known_hosts" -i "$work/client" root@127.0.0.1 "$snapshot_command" >"$evidence/sentinels-after-installer.txt"
 cmp "$evidence/sentinels-before-probe.txt" "$evidence/sentinels-after-installer.txt"
 printf 'actual installer preserves unrelated file/service/container/marker sentinels\n'
 if [[ ${NEROCD_BOOTSTRAP_ONBOARDING:-} == owned-systemd-target ]]; then
  # A different database separates the complete onboarding controller from the
  # accepted adapter fixture. No host PostgreSQL service or published port.
  docker exec "$pg_id" createdb -U postgres onboarding
  docker exec "$container_id" /bin/sh -ec 'mkdir -p /db/seeds /internal/api; cp /fixture/dev.sql /db/seeds/dev.sql; chmod 0600 /fixture/onboarding-client'
  docker exec --workdir /internal/api "$container_id" /fixture/api.test -test.v -test.run '^TestServerBootstrapRuntimeForwarderJoins$' -test.timeout=10s >"$evidence/onboarding-forwarder.log" 2>&1 || { cat "$evidence/onboarding-forwarder.log"; return 1; }
  cat "$evidence/onboarding-forwarder.log"
  shasum -a 256 "$root"/internal/api/server_bootstrap_runtime_integration_test.go "$root"/internal/app/server_bootstrap*.go "$root"/internal/store/postgres_server_bootstrap*.go "$root"/internal/api/server_bootstrap*.go >"$evidence/onboarding-source-sha256.txt"
  if [[ ${NEROCD_BOOTSTRAP_BROWSER:-} == 1 ]]; then
   bash "$root/web/app/scripts/hosting-server-bootstrap-fixture.sh" --drive-owned "$container_id" "$owner" "postgresql://postgres@$pg_address/onboarding?sslmode=disable" "$fingerprint" "$evidence" "$work/https-relay-port"
  else
   docker exec --workdir /internal/api --env NEROCD_BOOTSTRAP_ONBOARDING=owned-systemd-target --env "NEROCD_BOOTSTRAP_PROBE_FINGERPRINT=$fingerprint" --env "NEROCD_TEST_DATABASE_URL=postgresql://postgres@$pg_address/onboarding?sslmode=disable" "$container_id" /fixture/api.test -test.v -test.run '^TestServerBootstrapRuntime$' -test.timeout=7m >"$evidence/onboarding-runtime.log" 2>&1 || { cat "$evidence/onboarding-runtime.log"; return 1; }
   cat "$evidence/onboarding-runtime.log"
  fi
  docker exec "$container_id" /bin/sh -ec 'sha256sum /fixture/sentinel; systemctl show fixture-sentinel -p MainPID --value; systemctl is-active fixture-sentinel; docker inspect --format "{{.Id}} {{.State.Running}}" unrelated-sentinel; wc -l /fixture/marker; systemctl is-active fixture-marker || test "$(systemctl is-active fixture-marker)" = inactive' >"$evidence/sentinels-after-onboarding.txt"
  cmp "$evidence/sentinels-before-probe.txt" "$evidence/sentinels-after-onboarding.txt"
  printf 'PASS: real API/River/SSH/systemd/enrollment/registration/grant/readiness onboarding; same-host fixture TLS only\n'
 fi
 docker cp "$container_id:/fixture-packages.txt" "$evidence/packages.txt"
 printf 'PASS: native SSH/systemd/private Docker prerequisite and bounded installer fixture only; same-host fixture TLS\n'
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then main "$@"; fi
