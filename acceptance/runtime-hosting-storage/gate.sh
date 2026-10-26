#!/usr/bin/env bash
# Runs only on the explicitly authorized disposable local Docker target.
set -euo pipefail
repo_root=$(pwd -P)
test -f "$repo_root/internal/api/hosting_storage_runtime_integration_test.go"
[[ -z $(git status --porcelain) ]] || { echo 'gate requires a clean committed checkout' >&2; exit 1; }
head_before=$(git rev-parse HEAD)
tree_before=$(git rev-parse 'HEAD^{tree}')
owner=nerocd-storage-archive-runtime-20261024-f0a618
app_owner=nerocd-storage-archive-runtime-20261024-f0a618
label=io.nerocd.storage-runtime.owner
name=$owner-dind
volume=$owner-docker-data
docker_host=unix:///var/run/docker.sock
daemon=e37dd219-b13f-47dd-99ed-21d79442ed0c
archive=false
remote=false
retention=false
deletion=false
catalog=false
case ${1:-} in
 --catalog) catalog=true; archive=true; owner=nerocd-catalog-linkding-runtime-20261026; name=$owner-dind; volume=$owner-docker-data; test_pattern='^TestHostingCatalogLinkdingCompiledRunnerHTTPS$';;
 '') test_pattern='^(TestStorageRuntimeRollback|TestHostingStorageCompiledRunnerHTTPS)$';;
 --archive) archive=true; test_pattern='^(TestStorageRuntimeRollback|TestHostingStorageArchiveCompiledRunnerHTTPS)$';;
 --remote|--retention|--deletion)
  archive=true; remote=true
  owner=nerocd-storage-schedules-runtime-20261026-5d45412a
  name=$owner-dind; volume=$owner-docker-data
  test_pattern='^(TestStorageRuntimeRollback|TestStorageRuntimeDocker.*|TestHostingStorageArchiveCompiledRunnerHTTPS|TestHostingStorageRemote.*)$'
  if [[ "$1" == --retention || "$1" == --deletion ]]; then
   retention=true
   owner=nerocd-storage-retention-runtime-20261026-e4db0929
   name=$owner-dind; volume=$owner-docker-data
   test_pattern='^(TestStorageRuntimeRollback|TestStorageRuntimeDocker.*|TestHostingStorageArchiveCompiledRunnerHTTPS|TestHostingStorageRemote(ReceiptFaultContract|ObservationFaultContract|ProductionContract)|TestHostingStorageBackupRetentionCompiledRunnerHTTPS)$'
   if [[ "$1" == --deletion ]]; then
    deletion=true
    owner=nerocd-storage-deletion-runtime-20261026
    name=$owner-dind; volume=$owner-docker-data
    test_pattern='^(TestStorageRuntimeRollback|TestStorageRuntimeDocker.*|TestHostingStorageArchiveCompiledRunnerHTTPS|TestHostingStorageRemote(ReceiptFaultContract|ObservationFaultContract|ProductionContract)|TestHostingStorageBackupDeletionCompiledRunnerHTTPS)$'
   fi
  fi;;
 *) echo 'usage: gate.sh [--archive|--remote|--retention|--deletion|--catalog]' >&2; exit 2;;
esac
browser_publication=false
if [[ "$remote" == true || "$catalog" == true ]]; then browser_publication=true; fi
outer=''
pg=''
volume_attempted=false
outer_attempted=false
returned_outer=''
test_pid=''
dind='docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489'
if [[ "$remote" == true ]]; then
 evidence_parent="$HOME/.local/share/nerocd/acceptance/runtime-hosting-storage-schedules-20261026"
 if [[ "$retention" == true ]]; then evidence_parent="$HOME/.local/share/nerocd/acceptance/runtime-hosting-storage-retention-20261026"; fi
 if [[ "$deletion" == true ]]; then evidence_parent="$HOME/.local/share/nerocd/acceptance/runtime-hosting-storage-deletion-20261026"; fi
 [[ ! -L "$evidence_parent" ]] || { echo 'evidence parent is a symlink' >&2; exit 1; }
 mkdir -p "$evidence_parent"
 chmod 700 "$evidence_parent"
 evidence=$(mktemp -d "$evidence_parent/$head_before.XXXXXX")
else
 evidence=$(mktemp -d /tmp/nerocd-storage-runtime-20261024.XXXXXX)
fi
exec > >(tee "$evidence/gate.log") 2>&1
printf 'evidence=%s\nhead=%s\ntree=%s\nowner=%s\n' "$evidence" "$head_before" "$tree_before" "$owner"
host_docker() { docker --host "$docker_host" "$@"; }
inner_docker() { host_docker exec "$outer" docker --host unix:///var/run/docker.sock "$@"; }

# POSTGRES_CAPACITY_BEGIN
# Read only the exact labelled PostgreSQL service inside the verified owned guest.
postgres_capacity() {
 local stage=$1 observed status=0
 [[ -n "$pg" ]] || return 0
 [[ "$pg" =~ ^[a-f0-9]{64}$ ]] || return 1
 observed=$(host_docker exec "$outer" timeout 5 docker --host unix:///var/run/docker.sock inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}}" "$pg") || return 1
 [[ "$observed" == "$pg $owner" ]] || { echo 'PostgreSQL diagnostic ownership mismatch' >&2; return 1; }
 {
  echo "stage=$stage"
  host_docker exec "$outer" timeout 5 docker --host unix:///var/run/docker.sock exec "$pg" df -Pk /var/lib/postgresql/data || status=1
  host_docker exec "$outer" timeout 5 docker --host unix:///var/run/docker.sock exec "$pg" df -Pi /var/lib/postgresql/data || status=1
  # The image's local socket trust avoids passing a password to diagnostics.
  host_docker exec "$outer" timeout 5 docker --host unix:///var/run/docker.sock exec "$pg" psql --username=nerocd --dbname=nerocd_storage_runtime --no-psqlrc --set=ON_ERROR_STOP=1 --command="SELECT current_database(), pg_database_size(current_database()) AS database_bytes, (SELECT count(*) FROM pg_namespace WHERE nspname LIKE 'nerocd_%') AS fixture_schemas, (SELECT coalesce(sum(size),0) FROM pg_ls_waldir()) AS wal_bytes; SELECT name, setting, unit FROM pg_settings WHERE name IN ('max_wal_size','min_wal_size','shared_buffers','checkpoint_timeout','fsync','full_page_writes','synchronous_commit') ORDER BY name; SELECT relname, n_live_tup, n_dead_tup, pg_total_relation_size(relid) AS relation_bytes FROM pg_stat_sys_tables WHERE schemaname='pg_catalog' AND relname IN ('pg_attribute','pg_class','pg_type') ORDER BY relname;" || status=1
  echo "diagnostic_exit=$status"
 } > "$evidence/postgres-capacity-$stage" 2>&1
 return "$status"
}
# POSTGRES_CAPACITY_END

# IMAGE_PRELOAD_BEGIN
# Pull only an existing exact pin, then verify its native content and config
# against the host prerequisite. A saved image tar does not retain digest lookup.
preload_image() {
 reference=$1; host_manifest=$2; image_tag=$3
 inner_docker pull --platform linux/arm64 "$reference" > "$evidence/preload-$image_tag.log" 2>&1
 inner_docker image inspect "$reference" > "$evidence/preload-$image_tag.json"
 python3 - "$reference" "$host_manifest" "$evidence/preload-$image_tag.json" "$evidence/preload-$image_tag-proof.json" <<'VERIFY_PRELOAD'
import hashlib,json,re,sys
reference,host_path,guest_path,proof_path=sys.argv[1:]
def canonical(value):
 repository,separator,digest=value.partition("@")
 if not separator or not re.fullmatch("sha256:[a-f0-9]{64}",digest):raise SystemExit("unpinned image prerequisite")
 parts=repository.split("/")
 parts[-1]=parts[-1].split(":",1)[0]
 if "." not in parts[0] and ":" not in parts[0] and parts[0]!="localhost":parts.insert(0,"docker.io")
 if parts[0]=="docker.io" and len(parts)==2:parts.insert(1,"library")
 return "/".join(parts)+"@"+digest
pin=canonical(reference)
hosts=json.load(open(host_path));guests=json.load(open(guest_path))
if not isinstance(hosts,list) or not isinstance(guests,list) or len(guests)!=1:raise SystemExit("ambiguous image prerequisite")
def pinned(image):return any(canonical(value)==pin for value in image.get("RepoDigests",[]))
matching=[image for image in hosts if pinned(image)]
if len(matching)!=1:raise SystemExit("host prerequisite digest missing or ambiguous")
host,guest=matching[0],guests[0]
sha256=lambda value:isinstance(value,str) and re.fullmatch("sha256:[a-f0-9]{64}",value) is not None
platform=lambda image:(image.get("Os"),image.get("Architecture"),"" if image.get("Variant") is None else image["Variant"])
if platform(host)!=platform(guest) or platform(host) not in [("linux","arm64",""),("linux","arm64","v8")]:raise SystemExit("image prerequisite platform mismatch")
if not pinned(guest) or any(not sha256(image.get("Id")) for image in [host,guest]):raise SystemExit("image prerequisite digest/reported ID mismatch")
if not isinstance(guest.get("Config"),dict) or not guest["Config"] or guest["Config"]!=host.get("Config"):raise SystemExit("image prerequisite executable config mismatch")
def rootfs(image):
 value=image.get("RootFS")
 if not isinstance(value,dict) or value.get("Type")!="layers" or not isinstance(value.get("Layers"),list) or not value["Layers"] or any(not sha256(layer) for layer in value["Layers"]):raise SystemExit("invalid image prerequisite RootFS identity")
 return value
if rootfs(host)!=rootfs(guest):raise SystemExit("image prerequisite RootFS content mismatch")
# Docker Desktop's containerd image store reports the pinned OCI descriptor as
# Id. The qualified classic overlay2 guest reports its own image ID instead.
# Admit that representation only with descriptor evidence and identical content.
def descriptor_id(image):
 descriptor=image.get("Descriptor")
 return isinstance(descriptor,dict) and descriptor.get("digest")==image["Id"]==pin.split("@",1)[1] and descriptor.get("mediaType") in ["application/vnd.oci.image.index.v1+json","application/vnd.oci.image.manifest.v1+json"] and type(descriptor.get("size")) is int and descriptor["size"]>0
for image in [host,guest]:
 if image.get("Descriptor") is not None and not descriptor_id(image):raise SystemExit("unsupported image prerequisite descriptor identity")
relation="same_reported_id"
if host["Id"]!=guest["Id"]:
 if not descriptor_id(host) or guest.get("Descriptor") is not None or not isinstance(guest.get("GraphDriver"),dict) or guest["GraphDriver"].get("Name")!="overlay2":raise SystemExit("unexplained image prerequisite ID mismatch")
 relation="host_oci_descriptor_to_classic_overlay2"
def identity_hash(value):return "sha256:"+hashlib.sha256(json.dumps(value,sort_keys=True,separators=(",",":")).encode()).hexdigest()
proof={"canonical_pin":pin,"platform":list(platform(guest)),"host_reported_id":host["Id"],"guest_reported_id":guest["Id"],"id_relation":relation,"executable_config_hash":identity_hash(guest["Config"]),"rootfs_hash":identity_hash(rootfs(guest)),"rootfs_layers":len(guest["RootFS"]["Layers"])}
with open(proof_path,"w") as output:json.dump(proof,output,sort_keys=True);output.write("\n")
VERIFY_PRELOAD
}
# IMAGE_PRELOAD_END

# Inventory failures are uncertainty, never proof that an exact name is absent.
outer_inventory() {
 if ! host_docker ps -a --no-trunc --format '{{.ID}} {{.Names}}' > "$evidence/outer-inventory"; then
  echo 'outer inventory unavailable; preserving resources' >&2; return 1
 fi
 python3 -c '
import re,sys
rows=[]
for line in open(sys.argv[1]):
 fields=line.split()
 if len(fields)!=2 or not re.fullmatch("[a-f0-9]{64}",fields[0]):raise SystemExit("invalid container inventory")
 if fields[1]==sys.argv[2] or fields[0]==sys.argv[3]:rows.append(fields[0])
if len(rows)>1:raise SystemExit("ambiguous outer inventory")
if rows:print(rows[0])
' "$evidence/outer-inventory" "$name" "$outer"
}
volume_inventory() {
 if ! host_docker volume ls --format '{{.Name}}' > "$evidence/volume-inventory"; then
  echo 'volume inventory unavailable; preserving resources' >&2; return 1
 fi
 python3 -c '
import re,sys
names=open(sys.argv[1]).read().splitlines()
if any(not re.fullmatch("[A-Za-z0-9][A-Za-z0-9_.-]*",n) for n in names) or len(names)!=len(set(names)):raise SystemExit("invalid volume inventory")
print("present" if sys.argv[2] in names else "absent")
' "$evidence/volume-inventory" "$volume"
}
verify_volume() {
 if ! host_docker volume inspect "$volume" > "$evidence/volume-current-inspect"; then return 1; fi
 if ! host_docker ps -a --no-trunc --filter "volume=$volume" --format '{{.ID}}' > "$evidence/volume-current-users"; then return 1; fi
 python3 -c '
import json,re,sys
items=json.load(open(sys.argv[1]));users=open(sys.argv[2]).read().splitlines()
if len(items)!=1:raise SystemExit("ambiguous volume inspection")
v=items[0]
if v.get("Name")!=sys.argv[3] or v.get("Driver")!="local" or v.get("Scope")!="local" or v.get("Options") not in (None,{}) or v.get("Labels")!={sys.argv[4]:sys.argv[5]}:raise SystemExit("volume ownership/profile mismatch")
if not isinstance(v.get("Mountpoint"),str) or not v["Mountpoint"].startswith("/"):raise SystemExit("invalid volume mountpoint")
if any(not re.fullmatch("[a-f0-9]{64}",u) or u!=sys.argv[6] for u in users) or len(users)!=len(set(users)):raise SystemExit("volume has unowned users")
' "$evidence/volume-current-inspect" "$evidence/volume-current-users" "$volume" "$label" "$owner" "${1:-}"
}
verify_outer() {
 if ! host_docker inspect "$1" > "$evidence/outer-current-inspect"; then return 1; fi
 if [[ "$browser_publication" == true ]]; then
  python3 acceptance/runtime-hosting-storage/remote_evidence.py profile "$evidence" || return 1
 fi
 python3 -c '
import json,re,sys
items=json.load(open(sys.argv[1]));images=json.load(open(sys.argv[2]));volumes=json.load(open(sys.argv[3]))
if len(items)!=1 or len(images)!=3 or len(volumes)!=1:raise SystemExit("ambiguous outer dependencies")
c=items[0];image=images[0];v=volumes[0];cfg=c.get("Config",{});h=c.get("HostConfig",{});state=c.get("State",{})
identity,name,reference,label,owner,volume,remote=sys.argv[4:]
if not re.fullmatch("sha256:[a-f0-9]{64}",image.get("Id","")) or image.get("Os")!="linux" or image.get("Architecture")!="arm64":raise SystemExit("outer pinned image profile mismatch")
if not re.fullmatch("[a-f0-9]{64}",identity) or c.get("Id")!=identity or c.get("Name")!="/"+name or c.get("Image")!=image.get("Id") or cfg.get("Image")!=reference:raise SystemExit("outer image/name identity mismatch")
base=image.get("Config",{});labels=dict(base.get("Labels") or {});labels[label]=owner
if cfg.get("Labels")!=labels or cfg.get("Cmd")!=["dockerd","--host=unix:///var/run/docker.sock","--feature=containerd-snapshotter=false","--storage-driver=overlay2"] or cfg.get("Entrypoint")!=base.get("Entrypoint") or cfg.get("User","")!=base.get("User","") or cfg.get("WorkingDir","")!=base.get("WorkingDir",""):raise SystemExit("outer launch profile mismatch")
def environment(values):
 result={}
 for value in values:
  k,sep,val=value.partition("=")
  if not sep or k in result:raise SystemExit("ambiguous outer environment")
  result[k]=val
 return result
env=environment(base.get("Env") or []);env["DOCKER_TLS_CERTDIR"]=""
if environment(cfg.get("Env") or [])!=env:raise SystemExit("outer environment mismatch")
expected={"Privileged":True,"Memory":6<<30,"MemorySwap":12<<30,"NanoCpus":4000000000,"PidsLimit":2048,"NetworkMode":"bridge","PidMode":"","IpcMode":"private","UsernsMode":"","UTSMode":"","CgroupnsMode":"private","PublishAllPorts":False,"AutoRemove":False,"ReadonlyRootfs":False,"RestartPolicy":{"Name":"no","MaximumRetryCount":0}}
if any(h.get(k)!=value for k,value in expected.items()):raise SystemExit("outer resources/namespace profile mismatch")
if any(h.get(k) for k in ["Binds","VolumesFrom","Devices","DeviceRequests","CapAdd","CapDrop","GroupAdd","ExtraHosts"]):raise SystemExit("outer has extra host access")
if remote!="true" and h.get("PortBindings"):raise SystemExit("unexpected publication")
if h.get("Mounts")!=[{"Type":"volume","Source":volume,"Target":"/var/lib/docker"}]:raise SystemExit("outer requested mount mismatch")
mounts=c.get("Mounts") or []
if len(mounts)!=1:raise SystemExit("outer actual mount count mismatch")
m=mounts[0]
if any(m.get(k)!=value for k,value in {"Type":"volume","Name":volume,"Driver":"local","Destination":"/var/lib/docker","Source":v["Mountpoint"],"RW":True}.items()):raise SystemExit("outer actual mount mismatch")
if state.get("Paused") or state.get("Restarting") or state.get("Status") not in ["created","running","exited"] or state.get("Running")!=(state["Status"]=="running"):raise SystemExit("outer process state uncertain")
' "$evidence/outer-current-inspect" "$evidence/pinned-host-images" "$evidence/volume-current-inspect" "$1" "$name" "$dind" "$label" "$owner" "$volume" "$browser_publication"
}

collect_runtime_evidence() {
 mkdir -p "$evidence/runtime" || return 1
 host_docker cp "$outer:/fixture/evidence/." "$evidence/runtime"
}

cleanup() {
 result=$?
 trap - EXIT INT TERM
 if [[ -n "$test_pid" ]]; then
  if kill -0 "$test_pid" 2>/dev/null; then
   kill -TERM "$test_pid" || result=1
   for _ in $(seq 1 50); do if ! kill -0 "$test_pid" 2>/dev/null; then break; fi; sleep 0.1; done
   if kill -0 "$test_pid" 2>/dev/null; then kill -KILL "$test_pid" || result=1; fi
  fi
  wait "$test_pid" 2>/dev/null || true
  test_pid=''
 fi
 if [[ "$outer_attempted" == true ]]; then
  observed=$(outer_inventory) || exit 1
  if [[ -n "$observed" ]]; then
   if [[ -n "$outer" && "$outer" != "$observed" ]] || ! verify_volume "$observed" || ! verify_outer "$observed"; then
    echo 'outer identity/profile uncertain; preserving resources' >&2; exit 1
   fi
   outer=$observed
  else
   outer=''
  fi
 fi
 if [[ -n "$outer" ]]; then
  # Stop only binaries copied into this owned guest, then join before deleting it.
  running=$(python3 -c 'import json,sys;print(str(json.load(open(sys.argv[1]))[0]["State"]["Running"]).lower())' "$evidence/outer-current-inspect")
  if [[ "$running" == true ]] && ! host_docker exec "$outer" sh -ec '
   for exe in /proc/[0-9]*/exe; do
    case $(readlink "$exe" 2>/dev/null || true) in /fixture/api.test|/fixture/nerocd-proxy-fixture)
     pid=${exe#/proc/}; pid=${pid%/exe}; kill -TERM "$pid";; esac
   done
   for tick in $(seq 1 100); do
    live=false
    for exe in /proc/[0-9]*/exe; do
     case $(readlink "$exe" 2>/dev/null || true) in /fixture/api.test|/fixture/nerocd-proxy-fixture) live=true;; esac
    done
    if [ "$live" = false ]; then exit 0; fi
    sleep 0.1
   done
   exit 1'; then
   echo 'owned fixture processes did not join; preserving guest' >&2
   exit 1
  fi
  echo 'owned API and runner processes absent' | tee "$evidence/process-absence"
  if [[ "$running" == true && "$remote" == true ]]; then
   # Only the fixture's public archive and closed marker document are exported.
   collect_runtime_evidence || result=1
  fi
  if [[ "$running" == true ]]; then
  postgres_capacity final || result=1
  host_docker exec "$outer" df -Pk / /var/lib/docker /tmp > "$evidence/guest-filesystem-final" || result=1
  inner_docker ps -a --no-trunc --format '{{json .}}' > "$evidence/inner-containers-final" || result=1
  inner_docker volume ls --format '{{json .}}' > "$evidence/inner-volumes-final" || result=1
  inner_docker image ls --no-trunc --digests --format '{{json .}}' > "$evidence/inner-images-final" || result=1
  fi
  if ! verify_volume "$outer" || ! verify_outer "$outer"; then echo 'outer profile changed before removal; preserving guest' >&2; exit 1; fi
  cp "$evidence/outer-current-inspect" "$evidence/outer-final-inspect"
  host_docker rm --force "$outer" || result=1
  remaining=$(outer_inventory) || exit 1
  if [[ -n "$remaining" ]]; then echo 'owned outer absence not proven' >&2; exit 1; fi
  echo "owned outer absent: $outer"
 fi
 if [[ "$volume_attempted" == true ]]; then
  present=$(volume_inventory) || exit 1
  if [[ "$present" == present ]]; then
   if ! verify_volume; then echo 'outer storage ownership/users uncertain; preserving volume' >&2; exit 1; fi
   host_docker volume rm "$volume" || result=1
   present=$(volume_inventory) || exit 1
   if [[ "$present" != absent ]]; then echo 'owned volume absence not proven' >&2; exit 1; fi
  fi
  echo "owned outer volume absent: $volume"
 fi
 host_docker ps -a --no-trunc --format '{{json .}}' > "$evidence/host-containers-after" || result=1
 host_docker volume ls --format '{{json .}}' > "$evidence/host-volumes-after" || result=1
 host_docker ps -aq --no-trunc | sort > "$evidence/host-container-ids-after" || result=1
 host_docker volume ls --format '{{.Name}}' | sort > "$evidence/host-volume-names-after" || result=1
 if [[ "$remote" != true ]]; then
  cmp "$evidence/host-container-ids-before" "$evidence/host-container-ids-after" || result=1
 fi
 cmp "$evidence/host-volume-names-before" "$evidence/host-volume-names-after" || result=1
 git status --porcelain > "$evidence/status-after"
 [[ $(git rev-parse HEAD) == "$head_before" && $(git rev-parse 'HEAD^{tree}') == "$tree_before" && ! -s "$evidence/status-after" ]] || result=1
 printf 'gate_exit=%s\nouter_id=%s\nouter_volume=%s\n' "$result" "$outer" "$volume" > "$evidence/result"
 if [[ "$remote" == true || "$catalog" == true ]]; then
  if [[ -f "$evidence/preservation-before.private.json" ]]; then
   python3 acceptance/runtime-hosting-storage/remote_evidence.py after "$evidence" "$owner" "$name" "$volume" || result=1
  else
   echo 'baseline preservation evidence unavailable' >&2
   result=1
  fi
  printf 'gate_exit=%s\nouter_id=%s\nouter_volume=%s\n' "$result" "$outer" "$volume" > "$evidence/result"
  python3 acceptance/runtime-hosting-storage/remote_evidence.py receipt "$evidence" || result=1
 fi
 echo "gate exit=$result evidence=$evidence"
 exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
[[ $(host_docker info --format '{{.ID}} {{.OSType}} {{.Architecture}}') == "$daemon linux aarch64" ]]
observed=$(outer_inventory)
[[ -z "$observed" ]] || { echo 'outer name already exists' >&2; exit 1; }
present=$(volume_inventory)
[[ "$present" == absent ]] || { echo 'outer volume already exists' >&2; exit 1; }
df -Pk "$evidence" > "$evidence/host-filesystem-before"
df -Pi "$evidence" > "$evidence/host-inodes-before"
host_docker ps -a --no-trunc --format '{{json .}}' > "$evidence/host-containers-before"
host_docker ps -aq --no-trunc | sort > "$evidence/host-container-ids-before"
host_docker volume ls --format '{{.Name}}' | sort > "$evidence/host-volume-names-before"
host_docker volume ls --format '{{json .}}' > "$evidence/host-volumes-before"
if [[ "$remote" == true || "$catalog" == true ]]; then
 python3 acceptance/runtime-hosting-storage/remote_evidence.py before "$evidence"
 bun run --cwd web/app build > "$evidence/browser-ui-build.log" 2>&1
 python3 -B scripts/skills/ui_manifest.py > "$evidence/browser-ui-manifest.json"
 git ls-files -s web/app/src web/app/package.json web/app/bun.lock web/assets.go > "$evidence/browser-source-blobs"
fi
git ls-files -s acceptance/runtime-hosting-catalog internal/api/hosting_catalog_runtime_integration_test.go internal/domain/hosting_catalog.go internal/domain/hosting_catalog_linkding.yaml acceptance/runtime-hosting-storage acceptance/image-hosting-storage-fixture internal/api/hosting_storage_runtime_integration_test.go internal/api/hosting_storage_archive_runtime_integration_test.go internal/api/hosting_storage_remote_runtime* internal/api/hosting_storage_backup_retention_runtime_integration_test.go internal/api/hosting_storage_backup_deletion_runtime* Dockerfile.storage-archive cmd/nerocd-storage-archive internal/storagearchive > "$evidence/source-blobs"
fixture_tags=nerocd_proxy_fixture
if [[ "$remote" == true ]]; then fixture_tags=nerocd_proxy_fixture,nerocd_storage_remote_contract; fi
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -tags "$fixture_tags" -c -o "$evidence/api.test" ./internal/api
if [[ "$remote" == true ]]; then
 GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c -o "$evidence/store.test" ./internal/store
fi
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags nerocd_proxy_fixture -trimpath -o "$evidence/nerocd-proxy-fixture" ./cmd/nerocd
mkdir "$evidence/a" "$evidence/b"
for version in a b; do
 if [[ "$version" == a ]]; then display=A; else display=B; fi
 GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$display" -o "$evidence/$version/app" ./acceptance/image-hosting-storage-fixture
 cp acceptance/image-hosting-storage-fixture/Dockerfile "$evidence/$version/Dockerfile"
done
if [[ "$archive" == true ]]; then
 mkdir "$evidence/archive-helper"
 GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o "$evidence/archive-helper/nerocd-storage-archive" ./cmd/nerocd-storage-archive
 helper_sha=$(shasum -a 256 "$evidence/archive-helper/nerocd-storage-archive" | awk '{print $1}')
 sed -e "s/@SOURCE_COMMIT@/$head_before/g" -e "s/@EXECUTABLE_SHA256@/$helper_sha/g" Dockerfile.storage-archive > "$evidence/archive-helper/Dockerfile"
 printf 'source=%s\nexecutable_sha256=%s\n' "$head_before" "$helper_sha" > "$evidence/archive-helper-provenance"
fi
shasum -a 256 "$evidence/api.test" "$evidence/nerocd-proxy-fixture" "$evidence/a/app" "$evidence/b/app" | tee "$evidence/binary-sha256"
postgres=postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94
registry=registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373
host_docker image inspect "$dind" "$postgres" "$registry" > "$evidence/pinned-host-images"
traefik=docker.io/library/traefik@sha256:0f6150202bc1fed72a7bd2107460eaa510e5b8a905f3fdb5b84f2c8619c33368
pebble=ghcr.io/letsencrypt/pebble@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199
silo=pgsty/silo@sha256:87585bb783b7822318b3e1bcc4497c8427c98e2b021826d20b5f888713299f95
if [[ "$remote" == true ]]; then
 # Host cache supplies the exact content/config baseline, not guest digest lookup.
 host_docker image inspect "$traefik" "$pebble" "$silo" > "$evidence/pinned-remote-images"
fi
volume_attempted=true
host_docker volume create --driver local --label "$label=$owner" "$volume"
verify_volume || { echo 'new volume not proven owned and unused; preserving it' >&2; exit 1; }
outer_attempted=true
outer_run() {
 if [[ "$browser_publication" == true ]]; then host_docker run --pull=never --publish 127.0.0.1::18190 "$@"
 else host_docker run --pull=never "$@"; fi
}
returned_outer=$(outer_run --detach --privileged --cpus 4 --memory 6g --pids-limit 2048 --name "$name" --label "$label=$owner" --mount "type=volume,src=$volume,dst=/var/lib/docker" --env DOCKER_TLS_CERTDIR= "$dind" dockerd --host=unix:///var/run/docker.sock --feature=containerd-snapshotter=false --storage-driver=overlay2)
outer=$(outer_inventory)
[[ -n "$outer" && "$returned_outer" == "$outer" ]] && verify_volume "$outer" && verify_outer "$outer" || { echo 'created outer identity/profile uncertain' >&2; exit 1; }
cp "$evidence/outer-current-inspect" "$evidence/outer-initial-inspect"
ready=false
for _ in $(seq 1 60); do if inner_docker info >/dev/null 2>&1; then ready=true; break; fi; sleep 1; done
[[ "$ready" == true ]]
inner_docker info --format '{{.ID}} {{.OSType}} {{.Architecture}}' | tee "$evidence/inner-daemon"
inner_docker info --format '{{.ID}} {{.OSType}} {{.Architecture}} {{.Driver}} {{json .DriverStatus}}' | tee "$evidence/inner-storage-profile"
[[ $(inner_docker info --format '{{.Driver}}') == overlay2 ]]
printf 'qualification_scope=classic-image-store-overlay2-only\ncontainerd_image_store=not_qualified\n' > "$evidence/image-store-scope"
host_docker exec "$outer" mkdir -p /fixture /workspace/db/seeds /workspace/internal/api /workspace/internal/store /workspace/acceptance/runtime-proxy-control
for binary in api.test nerocd-proxy-fixture; do host_docker cp "$evidence/$binary" "$outer:/fixture/$binary"; done
if [[ "$remote" == true ]]; then host_docker cp "$evidence/store.test" "$outer:/fixture/store.test"; fi
host_docker cp db/seeds/dev.sql "$outer:/workspace/db/seeds/dev.sql"
host_docker cp acceptance/runtime-proxy-control/pebble.json "$outer:/workspace/acceptance/runtime-proxy-control/pebble.json"
# BASE_IMAGE_PRELOAD_BEGIN
# Qualify digest lookup in the guest itself. Host OCI descriptor IDs are not
# executable config IDs after transfer into the classic overlay2 image store.
preload_image "$postgres" "$evidence/pinned-host-images" postgres
preload_image "$registry" "$evidence/pinned-host-images" registry
# BASE_IMAGE_PRELOAD_END
if [[ "$remote" == true ]]; then
 preload_image "$traefik" "$evidence/pinned-remote-images" traefik
 preload_image "$pebble" "$evidence/pinned-remote-images" pebble
 preload_image "$silo" "$evidence/pinned-remote-images" silo
 host_docker exec "$outer" mkdir -m 700 /fixture/evidence
else
 for image in "$traefik" "$pebble"; do inner_docker pull "$image"; done
fi
# BASE_SERVICE_START_BEGIN
pg_image=$postgres; registry_image=$registry
pg=$(inner_docker run --pull=never --detach --name "$owner-pg" --label "$label=$owner" --network host --memory 1536m --cpus 1 --pids-limit 128 --tmpfs /var/lib/postgresql/data:rw,size=1g --env POSTGRES_USER=nerocd --env POSTGRES_PASSWORD=isolated_fixture_only --env POSTGRES_DB=nerocd_storage_runtime "$pg_image" postgres -c max_wal_size=128MB -c min_wal_size=32MB)
reg=$(inner_docker run --pull=never --detach --name "$owner-registry" --label "$label=$owner" --network host --memory 128m --cpus 1 --pids-limit 128 --tmpfs /var/lib/registry:rw,size=64m --env REGISTRY_HTTP_ADDR=127.0.0.1:25000 "$registry_image")
# BASE_SERVICE_START_END
printf 'inner_pg=%s\ninner_registry=%s\n' "$pg" "$reg" | tee "$evidence/inner-services"
for version in a b; do
 host_docker cp "$evidence/$version" "$outer:/fixture/$version"
 inner_docker build --build-arg "FIXTURE_OWNER=$app_owner" -t "127.0.0.1:25000/$app_owner:$version" "/fixture/$version"
 inner_docker push "127.0.0.1:25000/$app_owner:$version"
 inner_docker image inspect "127.0.0.1:25000/$app_owner:$version" > "$evidence/app-$version-image"
done
if [[ "$archive" == true ]]; then
 host_docker cp "$evidence/archive-helper" "$outer:/fixture/archive-helper"
 inner_docker build --platform linux/arm64 --provenance=false --sbom=false --network none --iidfile /fixture/archive-helper-image-id /fixture/archive-helper
 helper_image=$(host_docker exec "$outer" cat /fixture/archive-helper-image-id)
 inner_docker image inspect "$helper_image" > "$evidence/archive-helper-image-inspect"
 python3 -c "
import hashlib,json,sys
from pathlib import Path
root=Path(sys.argv[1]);image=json.loads((root/'archive-helper-image-inspect').read_text())[0]
daemon,os_name,arch=(root/'inner-daemon').read_text().split()
assert os_name=='linux' and arch=='aarch64' and image['Os']=='linux' and image['Architecture']=='arm64'
h={'mode':'local_image','recipe_version':'owned-upload.local-archive.v1','image_id':image['Id'],'executable_sha256':'sha256:'+sys.argv[3],'source_commit':sys.argv[2],'os':'linux','architecture':'arm64','daemon_id':daemon,'hash':''}
h['hash']='sha256:'+hashlib.sha256(json.dumps(h,separators=(',',':')).encode()).hexdigest()
(root/'archive-helper.json').write_text(json.dumps(h,separators=(',',':')))
" "$evidence" "$head_before" "$helper_sha"
 host_docker cp "$evidence/archive-helper.json" "$outer:/fixture/archive-helper.json"
fi
if [[ "${1:-}" == --catalog ]]; then
 linkding=docker.io/sissbruecker/linkding@sha256:e35cb50e0581178f245125ffaa909c565416c94c9f22ace305a7d234a4345522
 inner_docker pull "$linkding"
 inner_docker image inspect "$linkding" > "$evidence/linkding-image.json"
fi
inner_docker image ls --no-trunc --digests --format '{{json .}}' > "$evidence/inner-images-before-test"
host_docker exec "$outer" df -Pk / /var/lib/docker /tmp > "$evidence/guest-filesystem-before-test"
host_docker exec "$outer" df -Pi / /var/lib/docker /tmp > "$evidence/guest-inodes-before-test"
awk 'NR>1 && $4<2097152 {exit 1}' "$evidence/guest-filesystem-before-test"
ready=false
for _ in $(seq 1 60); do
 if [[ $(inner_docker exec --env PGPASSWORD=isolated_fixture_only "$pg" psql --host=127.0.0.1 --username=nerocd --dbname=nerocd_storage_runtime --no-psqlrc --tuples-only --no-align --command='SELECT current_database()' 2>/dev/null || true) == nerocd_storage_runtime ]]; then ready=true; break; fi
 sleep 1
done
[[ "$ready" == true ]]
postgres_capacity before-test
deletion_mode=''
if [[ "$deletion" == true ]]; then deletion_mode=disposable-dind; fi
retention_mode=''
if [[ "$retention" == true ]]; then retention_mode=disposable-dind; fi
remote_mode=''
if [[ "$remote" == true ]]; then remote_mode=disposable-dind; fi
archive_mode=''
if [[ "$archive" == true ]]; then archive_mode=disposable-dind; fi
if [[ "$remote" == true ]]; then
 store_tests='^(TestPostgresStorageRemoteLocalCompletionAtomicEnqueueAndReplay|TestPostgresStorageRemoteRetryPreservesArchiveAndRevocation|TestPostgresStorageRemoteDownloadFailureAndHeldRetry|TestPostgresStorageBackupScheduleAtomicConcurrencyAndDisable|TestPostgresStorageBackupScheduleAuthorityOverlapAndRollback|TestPostgresStorageBackupScheduleSafeProjection|TestPostgresStorageBackupNotificationAtomicReceiptAndUncertainty|TestPostgresStorageUploadNotificationPreservesLocalAndExcludesDownload|TestPostgresStorageBackupScheduleGenerationAndFrozenSource|TestPostgresStorageBackupScheduleRecoveryKeepsOccurrenceHeld)$'
 host_docker exec --workdir /workspace/internal/store "$outer" /fixture/store.test -test.list "$store_tests" > "$evidence/store-test-list"
 python3 - "$evidence/store-test-list" <<'CHECK_STORE_LIST'
import sys
expected={"TestPostgresStorageRemoteLocalCompletionAtomicEnqueueAndReplay","TestPostgresStorageRemoteRetryPreservesArchiveAndRevocation","TestPostgresStorageRemoteDownloadFailureAndHeldRetry","TestPostgresStorageBackupScheduleAtomicConcurrencyAndDisable","TestPostgresStorageBackupScheduleAuthorityOverlapAndRollback","TestPostgresStorageBackupScheduleSafeProjection","TestPostgresStorageBackupNotificationAtomicReceiptAndUncertainty","TestPostgresStorageUploadNotificationPreservesLocalAndExcludesDownload","TestPostgresStorageBackupScheduleGenerationAndFrozenSource","TestPostgresStorageBackupScheduleRecoveryKeepsOccurrenceHeld"}
actual=set(open(sys.argv[1]).read().splitlines())
if actual != expected:raise SystemExit("required storage PG test names missing")
CHECK_STORE_LIST
 host_docker exec --workdir /workspace/internal/store --env 'NEROCD_TEST_DATABASE_URL=postgres://nerocd:isolated_fixture_only@127.0.0.1:5432/nerocd_storage_runtime?sslmode=disable' "$outer" /fixture/store.test -test.v -test.run "$store_tests" -test.timeout=3m | tee "$evidence/store-tests.log"
 python3 - "$evidence/store-tests.log" <<'CHECK_STORE_PASS'
import sys
text=open(sys.argv[1]).read()
for name in ["TestPostgresStorageRemoteLocalCompletionAtomicEnqueueAndReplay","TestPostgresStorageRemoteRetryPreservesArchiveAndRevocation","TestPostgresStorageRemoteDownloadFailureAndHeldRetry","TestPostgresStorageBackupScheduleAtomicConcurrencyAndDisable","TestPostgresStorageBackupScheduleAuthorityOverlapAndRollback","TestPostgresStorageBackupScheduleSafeProjection","TestPostgresStorageBackupNotificationAtomicReceiptAndUncertainty","TestPostgresStorageUploadNotificationPreservesLocalAndExcludesDownload","TestPostgresStorageBackupScheduleGenerationAndFrozenSource","TestPostgresStorageBackupScheduleRecoveryKeepsOccurrenceHeld"]:
 if "--- PASS: "+name+" " not in text:raise SystemExit("required PG test PASS missing")
if "SKIP" in text:raise SystemExit("required PG test skipped")
CHECK_STORE_PASS
 if [[ "$deletion" == true ]]; then
  host_docker exec --workdir /workspace/internal/store --env 'NEROCD_TEST_DATABASE_URL=postgres://nerocd:isolated_fixture_only@127.0.0.1:5432/nerocd_storage_runtime?sslmode=disable' "$outer" /fixture/store.test -test.v -test.run '^TestPostgresStorageBackupDeletion' -test.timeout=3m | tee "$evidence/store-deletion-tests.log"
  python3 - "$evidence/store-deletion-tests.log" <<'CHECK_DELETION_PG'
import sys
text=open(sys.argv[1]).read()
for suffix in ['ManualDeleteSelectedBackupLocalKeepsRemoteAndHistory','CommittedResponseLossRetriesExactSelection','RestartAfterIntent','RejectsStaleAuthorityAndProtectedCopies','RemoteUsesRecordedTargetBothCopiesReportsPartialOutcome','RechecksFinalCopyAtExecution','RevocationAfterAdmission','DisabledPolicyDoesNotSelectCopies']:
 if '--- PASS: TestPostgresStorageBackupDeletion'+suffix+' ' not in text:raise SystemExit('manual deletion PG proof missing: '+suffix)
if 'SKIP' in text:raise SystemExit('manual deletion PG proof skipped')
CHECK_DELETION_PG
 fi
 host_docker exec --workdir /workspace/internal/api "$outer" /fixture/api.test -test.list "$test_pattern" > "$evidence/api-test-list"
 python3 - "$evidence/api-test-list" "$retention" "$deletion" <<'CHECK_API_LIST'
import sys
expected={"TestStorageRuntimeRollback","TestStorageRuntimeDockerOutputSeparatesDiagnostics","TestStorageRuntimeDockerOutputProcess","TestStorageRuntimeDockerDirectStartsNeverPull","TestHostingStorageArchiveCompiledRunnerHTTPS","TestHostingStorageRemoteCompiledRunnerHTTPS","TestHostingStorageRemoteReceiptFaultContract","TestHostingStorageRemoteObservationFaultContract","TestHostingStorageRemoteProductionContract"}
if sys.argv[2]=="true":expected.remove("TestHostingStorageRemoteCompiledRunnerHTTPS");expected.add("TestHostingStorageBackupRetentionCompiledRunnerHTTPS")
if sys.argv[3]=="true":expected.remove("TestHostingStorageBackupRetentionCompiledRunnerHTTPS");expected.add("TestHostingStorageBackupDeletionCompiledRunnerHTTPS")
actual=set(open(sys.argv[1]).read().splitlines())
if actual!=expected:raise SystemExit("required API test names missing or unexpected")
CHECK_API_LIST
 browser_origin=$(cat "$evidence/browser-origin")
 browser_key=$(python3 -c 'import secrets;print(secrets.token_hex(32))')
 docker --host "$docker_host" exec --workdir /workspace/internal/api --env NEROCD_STORAGE_RUNTIME=disposable-dind --env "NEROCD_STORAGE_REMOTE_RUNTIME=$remote_mode" --env "NEROCD_STORAGE_RETENTION_RUNTIME=$retention_mode" --env "NEROCD_STORAGE_DELETION_RUNTIME=$deletion_mode" --env "NEROCD_STORAGE_REMOTE_BROWSER_ORIGIN=$browser_origin" --env "NEROCD_STORAGE_REMOTE_BROWSER_KEY=$browser_key" --env NEROCD_STORAGE_REMOTE_EVIDENCE=/fixture/evidence --env "NEROCD_STORAGE_ARCHIVE_RUNTIME=$archive_mode" --env 'NEROCD_TEST_DATABASE_URL=postgres://nerocd:isolated_fixture_only@127.0.0.1:5432/nerocd_storage_runtime?sslmode=disable' "$outer" /fixture/api.test -test.v -test.run "$test_pattern" -test.timeout=24m > "$evidence/api-tests.log" 2>&1 &
 test_pid=$!
 NEROCD_STORAGE_REMOTE_BROWSER_ORIGIN="$browser_origin" NEROCD_STORAGE_REMOTE_BROWSER_KEY="$browser_key" NEROCD_STORAGE_REMOTE_HOST_EVIDENCE="$evidence" node acceptance/runtime-hosting-storage/remote_browser.mjs > "$evidence/browser.log" 2>&1
 wait "$test_pid"
 test_pid=''
 python3 - "$evidence/api-tests.log" "$retention" "$deletion" <<'CHECK_API_PASS'
import sys
text=open(sys.argv[1]).read()
required=["TestStorageRuntimeRollback","TestStorageRuntimeDockerOutputSeparatesDiagnostics","TestStorageRuntimeDockerOutputProcess","TestStorageRuntimeDockerDirectStartsNeverPull","TestHostingStorageArchiveCompiledRunnerHTTPS","TestHostingStorageRemoteCompiledRunnerHTTPS","TestHostingStorageRemoteReceiptFaultContract","TestHostingStorageRemoteObservationFaultContract","TestHostingStorageRemoteProductionContract"]
if sys.argv[2]=="true":required.remove("TestHostingStorageRemoteCompiledRunnerHTTPS");required.append("TestHostingStorageBackupRetentionCompiledRunnerHTTPS")
if sys.argv[3]=="true":required.remove("TestHostingStorageBackupRetentionCompiledRunnerHTTPS");required.append("TestHostingStorageBackupDeletionCompiledRunnerHTTPS")
for name in required:
 if "--- PASS: "+name+" " not in text:raise SystemExit("required API runtime PASS missing: "+name)
if "SKIP" in text:raise SystemExit("required API runtime test skipped")
CHECK_API_PASS
 if [[ "$deletion" == true ]]; then
  collect_runtime_evidence
  python3 - "$evidence/runtime/deletion-markers.json" "$evidence/browser-safe.json" <<'CHECK_DELETION_MARKERS'
import json,sys
v=json.load(open(sys.argv[1]))
for k in ['local_absent','remote_retained','history_retained','remaining_remote_restore','new_volume_application_reads_and_writes','source_preserved','manual_remote_conditional_delete','final_copy_confirmation','final_history_retained']:
 if v.get(k) is not True:raise SystemExit('manual deletion proof missing: '+k)
if v.get('status')!='passed':raise SystemExit('manual deletion failed')
b=json.load(open(sys.argv[2]))
if b.get('status')!='passed':raise SystemExit('connected browser manual deletion failed')
for k in ['manual_deletion_selection_and_exact_retry','manual_deletion_history_and_final_copy_warning','manual_deletion_viewer_history_and_denied','manual_deletion_remote_and_final_confirmation','manual_deletion_final_history']:
 if b.get(k) is not True:raise SystemExit('connected browser manual deletion proof missing: '+k)
CHECK_DELETION_MARKERS
 fi
elif [[ "$catalog" == true ]]; then
 browser_origin=$(cat "$evidence/browser-origin")
 browser_key=$(python3 -c 'import secrets;print(secrets.token_hex(32))')
 host_docker exec --workdir /workspace/internal/api --env NEROCD_CATALOG_RUNTIME=disposable-dind --env "NEROCD_CATALOG_BROWSER_ORIGIN=$browser_origin" --env "NEROCD_CATALOG_BROWSER_KEY=$browser_key" --env NEROCD_STORAGE_RUNTIME=disposable-dind --env 'NEROCD_TEST_DATABASE_URL=postgres://nerocd:isolated_fixture_only@127.0.0.1:5432/nerocd_storage_runtime?sslmode=disable' "$outer" /fixture/api.test -test.v -test.run "$test_pattern" -test.timeout=20m > "$evidence/api-tests.log" 2>&1 &
 test_pid=$!
 NEROCD_CATALOG_BROWSER_ORIGIN="$browser_origin" NEROCD_CATALOG_BROWSER_KEY="$browser_key" NEROCD_CATALOG_HOST_EVIDENCE="$evidence" node acceptance/runtime-hosting-catalog/browser.mjs > "$evidence/browser.log" 2>&1
 wait "$test_pid"
 test_pid=''
 python3 - "$evidence/api-tests.log" "$evidence/catalog-browser-safe.json" <<'CATALOG_BROWSER_PASS'
import json,pathlib,sys
text=pathlib.Path(sys.argv[1]).read_text();browser=json.loads(pathlib.Path(sys.argv[2]).read_text())
if "--- PASS: TestHostingCatalogLinkdingCompiledRunnerHTTPS " not in text or "SKIP" in text:raise SystemExit("required catalog runtime test did not pass without skips")
if browser.get("status")!="passed" or browser.get("intercepted_requests") is not False:raise SystemExit("connected catalog browser did not pass")
for key in ["served_assets_match_manifest","catalog_install_identical_retry","explicit_reveal_and_hide","browser_deployment","healthy_resource_result","current_viewer_denied_replay","browser_route_selection"]:
 if browser.get(key) is not True:raise SystemExit("connected catalog browser proof missing: "+key)
if browser.get("responsive_widths")!=[1280,320,390,430]:raise SystemExit("catalog responsive proof missing")
CATALOG_BROWSER_PASS
else
 catalog_mode=""; if [[ "${1:-}" == --catalog ]]; then catalog_mode=disposable-dind; fi
 host_docker exec --workdir /workspace/internal/api --env "NEROCD_CATALOG_RUNTIME=$catalog_mode" --env NEROCD_STORAGE_RUNTIME=disposable-dind --env "NEROCD_STORAGE_ARCHIVE_RUNTIME=$archive_mode" --env 'NEROCD_TEST_DATABASE_URL=postgres://nerocd:isolated_fixture_only@127.0.0.1:5432/nerocd_storage_runtime?sslmode=disable' "$outer" /fixture/api.test -test.v -test.run "$test_pattern" -test.timeout=12m | tee "$evidence/api-tests.log"
 if [[ "$catalog" == true ]]; then
  python3 - "$evidence/api-tests.log" <<'CATALOG_PASS'
import pathlib,sys
text=pathlib.Path(sys.argv[1]).read_text()
if "--- PASS: TestHostingCatalogLinkdingCompiledRunnerHTTPS " not in text or "SKIP" in text:
 raise SystemExit("required catalog runtime test did not pass without skips")
CATALOG_PASS
 fi
fi
echo 'compiled storage runner / authenticated public API / trusted fixture-CA HTTPS gate passed'
