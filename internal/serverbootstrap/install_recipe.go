package serverbootstrap

// The only interpolated shell text is this constant itself. All request fields
// arrive as bounded stdin data, are validated, and are used only as quoted data.
const ownedRecipe = `set -fu
umask 077
fail() { printf '{"code":"%s"}\n' "$1"; exit 1; }
line() { IFS= read -r value || fail invalid_manifest; [ "${#value}" -le 512 ] || fail invalid_manifest; }
line; [ "$value" = nerocd_owned_v1 ] || fail invalid_manifest
line; action=$value; case "$action" in install|inspect|restart|cleanup) ;; *) fail invalid_manifest;; esac
line; installation=$value
line; operation=$value
line; initial=$value
for value in "$installation" "$operation" "$initial"; do
 [ "${#value}" = 32 ] || fail invalid_manifest
 case "$value" in *[!a-f0-9]*) fail invalid_manifest;; esac
done
line; size=$value
line; digest=$value
line; architecture=$value
line; manifest_size=$value
line; manifest_digest=$value
line; enrollment_size=$value
for value in "$size" "$manifest_size" "$enrollment_size"; do
 case "$value" in ''|*[!0-9]*) fail invalid_manifest;; esac
 [ "${#value}" -le 9 ] || fail invalid_manifest
done
[ "$size" -gt 0 ] && [ "$size" -le 268435456 ] || fail invalid_artifact
[ "$manifest_size" -gt 0 ] && [ "$manifest_size" -le 4096 ] || fail invalid_manifest
case "$enrollment_size" in 0|68) ;; *) fail invalid_manifest;; esac
for value in "$digest" "$manifest_digest"; do
 [ "${#value}" = 64 ] || fail invalid_manifest
 case "$value" in *[!a-f0-9]*) fail invalid_manifest;; esac
done
case "$architecture" in amd64|arm64) ;; *) fail invalid_artifact;; esac
manifest=$(dd bs=1 count="$manifest_size" 2>/dev/null) || fail partial_transfer
[ "$(printf %s "$manifest" | wc -c | tr -d ' ')" = "$manifest_size" ] || fail partial_transfer
[ "$(printf %s "$manifest" | sha256sum | cut -d' ' -f1)" = "$manifest_digest" ] || fail invalid_manifest
base=/var/lib/nerocd-bootstrap
home=$base/installations/$installation
unit=/etc/systemd/system/nerocd-agent-$installation.service
wants=/etc/systemd/system/multi-user.target.wants/nerocd-agent-$installation.service
lock=/run/nerocd-bootstrap.lock
regular() { [ ! -L "$1" ] && [ -f "$1" ] && [ "$(stat -c '%u:%a' "$1")" = "0:$2" ]; }
directory() { [ ! -L "$1" ] && [ -d "$1" ] && [ "$(stat -c '%u:%a' "$1")" = "0:700" ]; }
result() { printf '{"inspection":{"installation_id":"%s","manifest_sha256":"%s","operation_id":"%s","state":"%s","artifact_matches":false,"service_active":false,"service_job_pending":false,"invocation_id":"","enrollment_file_exists":false,"credential_file_exists":false}}\n' "$installation" "$manifest_digest" "$operation" "$1"; }
if [ "$action" = inspect ] && [ ! -e "$base" ] && [ ! -L "$base" ]; then
 [ ! -e "$unit" ] && [ ! -L "$unit" ] && [ ! -e "$wants" ] && [ ! -L "$wants" ] || fail ownership_conflict
 result absent; exit 0
fi
if [ "$action" != inspect ] && [ ! -e "$lock" ] && [ ! -L "$lock" ]; then
 (set -C; : >"$lock") || fail remote_busy
fi
regular "$lock" 600 || fail ownership_conflict
inode=$(stat -c '%d:%i' "$lock")
exec 9<"$lock"
[ "$(stat -Lc '%d:%i' /proc/self/fd/9)" = "$inode" ] || fail ownership_conflict
flock -n 9 || fail remote_busy
if [ ! -e "$base" ] && [ ! -L "$base" ]; then
 [ "$action" = install ] || fail ownership_conflict
 mkdir -m 700 "$base" || fail ownership_conflict
 (set -C; printf 'nerocd_owned_v1\n' >"$base/protocol") || fail ownership_conflict
 mkdir -m 700 "$base/installations" "$base/receipts" || fail ownership_conflict
fi
directory "$base" && directory "$base/installations" && directory "$base/receipts" || fail ownership_conflict
regular "$base/protocol" 600 && [ "$(cat "$base/protocol")" = nerocd_owned_v1 ] || fail ownership_conflict
claim="$installation $initial $manifest_digest $digest $size $architecture"
if [ -e "$base/receipts/$installation.removed" ] || [ -L "$base/receipts/$installation.removed" ]; then
 regular "$base/receipts/$installation.removed" 600 || fail ownership_conflict
 tombstone=$(cat "$base/receipts/$installation.removed")
 removed_operation=$(printf %s "$tombstone" | cut -d' ' -f7)
 [ "${#removed_operation}" = 32 ] || fail ownership_conflict
 case "$removed_operation" in *[!a-f0-9]*) fail ownership_conflict;; esac
 [ "$tombstone" = "$claim $removed_operation" ] || fail ownership_conflict
 case "$action" in inspect) ;; cleanup) [ "$operation" = "$removed_operation" ] || fail ownership_conflict;; *) fail ownership_conflict;; esac
 for path in "$unit" "$wants" "$home/agent" "$home/credential" "$home/enrollment"; do [ ! -e "$path" ] && [ ! -L "$path" ] || fail uncertain; done
 operation=$removed_operation
 result removed; exit 0
fi
if [ ! -e "$home" ] && [ ! -L "$home" ]; then
 if [ "$action" = inspect ]; then
  [ ! -e "$unit" ] && [ ! -L "$unit" ] && [ ! -e "$wants" ] && [ ! -L "$wants" ] || fail ownership_conflict
  result absent; exit 0
 fi
 [ "$action" = install ] && [ "$operation" = "$initial" ] || fail ownership_conflict
 [ ! -e "$unit" ] && [ ! -L "$unit" ] && [ ! -e "$wants" ] && [ ! -L "$wants" ] || fail ownership_conflict
 mkdir -m 700 "$home" || fail ownership_conflict
 (set -C; printf '%s\n' "$claim" >"$home/claim") || fail ownership_conflict
 (set -C; printf %s "$manifest" >"$home/manifest.json") || fail ownership_conflict
 sync -f "$home" || fail remote_failure
 (set -C; dd bs=1048576 iflag=count_bytes,fullblock count="$size" conv=excl,fsync oflag=nofollow of="$home/agent.pending" 2>/dev/null) || fail partial_transfer
 [ "$(stat -c %s "$home/agent.pending")" = "$size" ] || fail partial_transfer
 [ "$(sha256sum "$home/agent.pending" | cut -d' ' -f1)" = "$digest" ] || fail partial_transfer
 header=$(od -An -tx1 -N20 "$home/agent.pending" | tr -d ' \n')
 [ "${#header}" = 40 ] && [ "$(printf %s "$header" | cut -c1-14)" = 7f454c46020101 ] || fail invalid_artifact
 case "$(printf %s "$header" | cut -c15-16)" in 00|03) ;; *) fail invalid_artifact;; esac
 case "$(printf %s "$header" | cut -c33-36)" in 0200|0300) ;; *) fail invalid_artifact;; esac
 case "$architecture:$(printf %s "$header" | cut -c37-40)" in amd64:3e00|arm64:b700) ;; *) fail invalid_artifact;; esac
 chmod 700 "$home/agent.pending" || fail remote_failure
 mv -n "$home/agent.pending" "$home/agent" || fail ownership_conflict
 if [ "$enrollment_size" = 68 ]; then
  enrollment=$(dd bs=1 count=68 2>/dev/null) || fail partial_transfer
  [ "${#enrollment}" = 68 ] || fail partial_transfer
  case "$enrollment" in nce_*) ;; *) fail invalid_manifest;; esac
  value=$(printf %s "$enrollment" | cut -c5-68)
  case "$value" in *[!a-f0-9]*) fail invalid_manifest;; esac
  (set -C; printf %s "$enrollment" >"$home/enrollment") || fail ownership_conflict
  unset enrollment value
 else fail invalid_manifest; fi
 sync -f "$home" || fail remote_failure
else
 directory "$home" && regular "$home/claim" 600 || fail ownership_conflict
 [ "$(cat "$home/claim")" = "$claim" ] || fail ownership_conflict
 if [ "$action" = install ]; then dd bs=1048576 iflag=count_bytes,fullblock count="$((size+enrollment_size))" of=/dev/null 2>/dev/null || fail partial_transfer; fi
fi
regular "$home/manifest.json" 600 || fail ownership_conflict
[ "$(sha256sum "$home/manifest.json" | cut -d' ' -f1)" = "$manifest_digest" ] || fail ownership_conflict
for path in "$home/work" "$home/journal" "$home/secrets"; do
 if [ -e "$path" ] || [ -L "$path" ]; then directory "$path" || fail ownership_conflict; fi
done
if [ ! -e "$home/agent" ]; then
 if [ "$action" = inspect ]; then result staged; exit 0; fi
 if [ "$action" = cleanup ]; then
  [ ! -e "$unit" ] && [ ! -L "$unit" ] && [ ! -e "$wants" ] && [ ! -L "$wants" ] || fail ownership_conflict
  for path in "$home/claim" "$home/manifest.json" "$home/agent.pending"; do
   if [ -e "$path" ] || [ -L "$path" ]; then regular "$path" 600 || fail ownership_conflict; fi
  done
  (set -C; printf '%s %s\n' "$claim" "$operation" >"$base/receipts/$installation.removed") || fail ownership_conflict
  sync -f "$base/receipts" || fail remote_failure
  for path in "$home/agent.pending" "$home/manifest.json" "$home/claim"; do if [ -e "$path" ]; then rm -- "$path" || fail uncertain; fi; done
  rmdir "$home" || fail uncertain
  result removed; exit 0
 fi
 fail uncertain
fi
regular "$home/agent" 700 || fail ownership_conflict
[ "$(stat -c %s "$home/agent")" = "$size" ] && [ "$(sha256sum "$home/agent" | cut -d' ' -f1)" = "$digest" ] || fail ownership_conflict
printf '{"action":"%s","operation_id":"%s","manifest":%s}' "$action" "$operation" "$manifest" | "$home/agent" bootstrap-agent owned
`
