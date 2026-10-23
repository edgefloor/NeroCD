package serverbootstrap

// This recipe is constant and receives no target or caller-controlled text.
// env -i removes Docker/client/shell configuration from the recipe environment.
// The SSH account and fixed system binaries remain a trusted host-admin boundary.
const probeRecipe = `set -f
printf 'nerocd_probe_v1\n'
arch=$(uname -m 2>/dev/null) || arch=unknown
kernel=$(uname -s 2>/dev/null) || kernel=unknown
if [ "$kernel" != Linux ]; then
 printf 'os\tunsupported\t-\t-\t%s\n' "$arch"
elif [ -r /etc/os-release ]; then
 id=$(sed -n 's/^ID=//p' /etc/os-release | tr -d '"')
 version=$(sed -n 's/^VERSION_ID=//p' /etc/os-release | tr -d '"')
 if [ -n "$id" ] && [ -n "$version" ]; then printf 'os\tavailable\t%s\t%s\t%s\n' "$id" "$version" "$arch"; else printf 'os\tmissing\t-\t-\t%s\n' "$arch"; fi
else
 printf 'os\tmissing\t-\t-\t%s\n' "$arch"
fi
if [ "$(cat /proc/1/comm 2>/dev/null)" != systemd ]; then
 systemd=not_systemd
elif ! command -v systemctl >/dev/null 2>&1; then
 systemd=missing
else
 status=$(systemctl is-system-running 2>/dev/null)
 case "$status" in running|degraded) systemd=$status;; initializing|starting) systemd=starting;; *) systemd=offline;; esac
fi
printf 'systemd\t%s\n' "$systemd"
if ! command -v docker >/dev/null 2>&1; then
 printf 'docker\tmissing\t-\n'
elif api=$(docker --config /proc/self/fd --host unix:///var/run/docker.sock version --format '{{.Server.APIVersion}}' 2>/dev/null); then
 printf 'docker\tavailable\t%s\n' "$api"
else
 printf 'docker\tunavailable\t-\n'
fi
for tool in compose buildx; do
 plugin=
 for base in /usr/libexec/docker/cli-plugins /usr/lib/docker/cli-plugins /usr/local/libexec/docker/cli-plugins /usr/local/lib/docker/cli-plugins; do
  if [ -x "$base/docker-$tool" ]; then plugin=$base/docker-$tool; break; fi
 done
 if [ -z "$plugin" ]; then
  printf '%s\tmissing\t-\n' "$tool"
 elif [ "$tool" = compose ]; then
  if version=$("$plugin" version --short 2>/dev/null); then printf '%s\tavailable\t%s\n' "$tool" "$version"; else printf '%s\tunavailable\t-\n' "$tool"; fi
 else
  if version=$("$plugin" version 2>/dev/null); then
   version=$(printf '%s\n' "$version" | awk 'NF == 3 {print $2}')
   printf '%s\tavailable\t%s\n' "$tool" "$version"
  else printf '%s\tunavailable\t-\n' "$tool"; fi
 fi
done
if ! command -v df >/dev/null 2>&1 || ! command -v awk >/dev/null 2>&1; then
 printf 'disk\tmissing\t-\t-\n'
elif disk=$(df -Pk / 2>/dev/null | awk 'NR == 2 {print $2 "\t" $4}'); [ -n "$disk" ]; then
 printf 'disk\tavailable\t%s\n' "$disk"
else
 printf 'disk\tunavailable\t-\t-\n'
fi
printf 'end\n'
`
