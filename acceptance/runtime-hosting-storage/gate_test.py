"""Exercise the actual gate's signal traps using harmless executable stubs."""

import json
import hashlib
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import textwrap
import time
import unittest


STUB = r'''
import json, os, pathlib, sys, time
root = pathlib.Path(os.environ["STORAGE_GATE_TEST_ROOT"])
tool, args = pathlib.Path(sys.argv[0]).name, sys.argv[1:]
with (root / "commands").open("a") as log:
    log.write(json.dumps([tool, *args]) + "\n")
if tool == "git":
    if args[:1] == ["rev-parse"]:
        print("1" * 40 if args[1] == "HEAD" else "2" * 40)
elif tool == "mktemp":
    evidence = root / "evidence"
    evidence.mkdir()
    print(evidence)
elif tool == "docker":
    assert args[:2] == ["--host", "unix:///var/run/docker.sock"]
    args = args[2:]
    if args[:1] == ["info"]:
        print("e37dd219-b13f-47dd-99ed-21d79442ed0c linux aarch64")
    elif args[:2] == ["volume", "inspect"]:
        sys.exit(1)
    elif args[:1] != ["ps"] and args[:2] != ["volume", "ls"]:
        raise RuntimeError("unexpected Docker command: " + repr(args))
elif tool == "go":
    (root / "started").touch()
    deadline = time.monotonic() + 10
    while not (root / "release").exists():
        if time.monotonic() >= deadline:
            raise RuntimeError("test did not release fake compiler")
        time.sleep(0.01)
    sys.exit(int(os.environ["STORAGE_GATE_TEST_BUILD_EXIT"]))
else:
    raise RuntimeError("unexpected executable")
'''


class GateExitTest(unittest.TestCase):
    def run_gate(self, delivered_signal, expected_exit):
        gate = Path(os.environ.get("STORAGE_GATE_UNDER_TEST", Path(__file__).with_name("gate.sh"))).resolve()
        with tempfile.TemporaryDirectory(prefix="nerocd-storage-gate-test-") as temporary:
            root = Path(temporary)
            (root / "internal/api").mkdir(parents=True)
            (root / "internal/api/hosting_storage_runtime_integration_test.go").touch()
            binary = root / "bin"
            binary.mkdir()
            for name in ("git", "go", "docker", "mktemp"):
                executable = binary / name
                executable.write_text("#!" + sys.executable + "\n" + STUB)
                executable.chmod(0o700)
            environment = os.environ.copy()
            environment.update(
                PATH=str(binary) + os.pathsep + environment["PATH"],
                STORAGE_GATE_TEST_ROOT=str(root),
                STORAGE_GATE_TEST_BUILD_EXIT="0" if delivered_signal else str(expected_exit),
            )
            process = subprocess.Popen(
                ["bash", str(gate)], cwd=root, env=environment,
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                text=True, start_new_session=True,
            )
            try:
                # This setup runs a fresh Python interpreter for each Git/Docker
                # stub before the compiler handshake. SDK/interpreter startup
                # on macOS can exceed five seconds without any gate work hung.
                # Keep a bounded setup budget separate from the unchanged
                # compiler release, exact signal/exit and cleanup deadlines.
                setup_started = time.monotonic()
                deadline = setup_started + 15
                while not (root / "started").exists():
                    if process.poll() is not None or time.monotonic() >= deadline:
                        self.fail("gate did not reach fake compiler handshake")
                    time.sleep(0.01)
                print(f"gate fake compiler ready after {time.monotonic()-setup_started:.3f}s", flush=True)
                if delivered_signal:
                    process.send_signal(delivered_signal)
                (root / "release").touch()
                output, _ = process.communicate(timeout=5)
                self.assertEqual(process.returncode, expected_exit, output)
                result = (root / "evidence/result").read_text()
                self.assertIn(f"gate_exit={expected_exit}\n", result)
                self.assertEqual(output.count("gate exit="), 1, output)
                commands = [json.loads(line) for line in (root / "commands").read_text().splitlines()]
                self.assertEqual(sum(command[0] == "go" for command in commands), 1)
                self.assertTrue(any(command[0] == "docker" for command in commands))
            finally:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.communicate(timeout=5)

    def test_sigint_exits_130(self):
        self.run_gate(signal.SIGINT, 130)

    def test_sigterm_exits_143(self):
        self.run_gate(signal.SIGTERM, 143)

    def test_build_failure_preserves_exit(self):
        self.run_gate(None, 17)


# Ownership/admission regressions use the same real launcher with a stateful CLI.
GATE = Path(os.environ.get("STORAGE_GATE_UNDER_TEST", Path(__file__).with_name("gate.sh"))).resolve()
OWNER = "nerocd-storage-archive-runtime-20261024-f0a618"
VOLUME = OWNER + "-docker-data"
OUTER = "a" * 64

FAKE_DOCKER = r'''
#!/usr/bin/env python3
import json,os,sys
from pathlib import Path
root=Path(os.environ['FAKE_ROOT']);state=json.loads((root/'state').read_text());args=sys.argv[1:]
if args[:2]!=['--host','unix:///var/run/docker.sock']:sys.exit(99)
a=args[2:]
prior=[json.loads(line) for line in (root/'calls').read_text().splitlines()]
with (root/'calls').open('a') as f:f.write(json.dumps(a)+'\n')
owner='nerocd-storage-archive-runtime-20261024-f0a618';label='io.nerocd.storage-runtime.owner';volume=owner+'-docker-data';name=owner+'-dind';identity='a'*64
image='sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489'
reference='docker:29.4.0-dind@'+image
scenario=os.environ['FAKE_SCENARIO']
def save(): (root/'state').write_text(json.dumps(state))
def volume_body():return {'Name':volume,'Driver':'local','Scope':'local','Options':{},'Labels':{label:state['volume_owner']},'Mountpoint':'/var/lib/docker/volumes/'+volume+'/_data'}
def outer_body():
 body={'Id':identity,'Name':'/'+name,'Image':image,'Config':{'Image':reference,'Labels':{label:owner},'Env':['DOCKER_TLS_CERTDIR='],'Entrypoint':['dockerd-entrypoint.sh'],'Cmd':['dockerd','--host=unix:///var/run/docker.sock','--feature=containerd-snapshotter=false','--storage-driver=overlay2'],'User':'','WorkingDir':'/'},'HostConfig':{'Privileged':True,'Memory':6<<30,'MemorySwap':12<<30,'NanoCpus':4000000000,'PidsLimit':2048,'Binds':None,'VolumesFrom':None,'Mounts':[{'Type':'volume','Source':volume,'Target':'/var/lib/docker'}],'PortBindings':{},'PublishAllPorts':False,'NetworkMode':'bridge','PidMode':'','IpcMode':'private','UsernsMode':'','UTSMode':'','CgroupnsMode':'private','Devices':[],'DeviceRequests':[],'RestartPolicy':{'Name':'no','MaximumRetryCount':0},'AutoRemove':False,'ReadonlyRootfs':False},'Mounts':[{'Type':'volume','Name':volume,'Driver':'local','Destination':'/var/lib/docker','Source':'/var/lib/docker/volumes/'+volume+'/_data','RW':True}],'State':{'Running':True,'Status':'running','Paused':False,'Restarting':False}}
 if scenario=='lost_created_only':body['State']['Running']=False;body['State']['Status']='created'
 if scenario=='lost_mismatch':body['HostConfig']['Mounts'][0]['Target']='/foreign';body['Mounts'][0]['Destination']='/foreign'
 if scenario=='lost_wrong_image':body['Image']='sha256:'+'f'*64
 if scenario=='lost_wrong_owner':body['Config']['Labels'][label]='foreign'
 if scenario=='lost_extra_mount':body['Mounts'].append({'Type':'bind','Source':'/foreign','Destination':'/foreign','RW':True})
 if scenario=='lost_wrong_resources':body['HostConfig']['Memory']=1
 if scenario=='lost_wrong_profile':body['Config']['Cmd']=['dockerd','--host=tcp://0.0.0.0:2375']
 return body
if a[0]=='info':print('e37dd219-b13f-47dd-99ed-21d79442ed0c linux aarch64')
elif a[:2]==['volume','ls']:
 if scenario=='failed_inventory' or scenario=='failed_final_inventory' and any(c[:2]==['volume','rm'] for c in prior):sys.exit(23)
 if state['volume_owner']:
  print(json.dumps({'Name':volume}) if '{{json .}}' in a else volume)
elif a[0]=='ps':
 if scenario=='failed_container_inventory' and not any(c[0]=='ps' for c in prior):sys.exit(24)
 if scenario=='failed_users' and any(v.startswith('volume=') for v in a):sys.exit(25)
 if scenario=='foreign_users' and state['volume_owner'] and any(v.startswith('volume=') for v in a):print('c'*64);sys.exit(0)
 if scenario=='lost_failed_inventory' and state['outer']:sys.exit(26)
 if state['outer']:
  if '{{json .}}' in a:print(json.dumps({'ID':identity,'Names':name}))
  elif '{{.Names}}' in ' '.join(a):print(identity+' '+name)
  else:print(identity)
elif a[:2]==['image','inspect']:
 body={'Id':image,'Config':{'Env':['DOCKER_TLS_CERTDIR=/certs'],'Entrypoint':['dockerd-entrypoint.sh'],'WorkingDir':'/','User':'','Labels':{}},'Os':'linux','Architecture':'arm64'}
 print(json.dumps([body,body,body]))
elif a[:2]==['volume','inspect']:
 if not state['volume_owner'] or scenario=='inspect_error_existing':sys.exit(1)
 body=volume_body()
 if scenario=='wrong_driver':body['Driver']='foreign'
 if scenario=='wrong_scope':body['Scope']='global'
 if scenario=='nonempty_options':body['Options']={'device':'/foreign'}
 if '--format' in a:
  if '{{.Driver}}' in a[-2]:print(volume+' local '+state['volume_owner'])
  else:print(json.dumps(body))
 else:print(json.dumps([body]))
elif a[:2]==['volume','create']:
 if scenario=='foreign_race':state['volume_owner']='foreign-owner'
 elif not state['volume_owner']:state['volume_owner']=owner
 save();print(volume)
elif a[0]=='run':
 state['mounted_owner']=state['volume_owner'];state['outer']=True;save()
 # Simulate Docker committing the run, then losing its successful response.
 if scenario=='lost_empty_success':sys.exit(0)
 if scenario.startswith('lost_') or scenario in ['foreign_race','failed_final_inventory']:sys.exit(42)
 sys.exit(43)
elif a[0]=='inspect':
 if not state['outer']:sys.exit(1)
 body=outer_body()
 if '--format' in a:
  fmt=a[a.index('--format')+1]
  if '.Mounts' in fmt:print('volume '+volume+' /var/lib/docker')
  else:print(identity+' '+owner)
 else:print(json.dumps([body]))
elif a[0]=='exec':
 # Only owned cleanup is reachable after the intentionally lost run response.
 if a[1]!=identity:sys.exit(97)
 if 'df' in a:print('Filesystem 1024-blocks Used Available Capacity Mounted on\noverlay 10000000 1 9999999 1% /')
elif a[0]=='rm':state['outer']=False;save();print(identity)
elif a[:2]==['volume','rm']:
 if state['outer']:sys.exit(96)
 state['volume_owner']=None;save();print(volume)
else:sys.exit(98)
'''


class LauncherTests(unittest.TestCase):
    def run_gate(self, scenario):
        with tempfile.TemporaryDirectory(prefix="nerocd-archive-launcher-test-") as directory:
            root = Path(directory)
            bins = root / "bin"
            bins.mkdir()
            (root / "internal/api").mkdir(parents=True)
            (root / "internal/api/hosting_storage_runtime_integration_test.go").touch()
            (root / "acceptance/image-hosting-storage-fixture").mkdir(parents=True)
            (root / "acceptance/image-hosting-storage-fixture/Dockerfile").write_text("FROM scratch\n")
            (root / "state").write_text(json.dumps({"outer": False, "volume_owner": "foreign-owner" if scenario=="inspect_error_existing" else None, "mounted_owner": None}))
            (root / "calls").touch()
            commands = {
                "docker": textwrap.dedent(FAKE_DOCKER).lstrip(),
                "mktemp": "#!/usr/bin/env python3\nimport os,pathlib\np=pathlib.Path(os.environ['FAKE_ROOT'])/'evidence';p.mkdir();print(p)\n",
                "git": "#!/usr/bin/env python3\nimport sys\nif sys.argv[1:3]==['rev-parse','HEAD']:print('b'*40)\nelif sys.argv[1:2]==['rev-parse']:print('c'*40)\n",
                "go": "#!/usr/bin/env python3\nimport pathlib,sys\np=pathlib.Path(sys.argv[sys.argv.index('-o')+1]);p.write_bytes(b'fake compiled fixture')\n",
            }
            for name, source in commands.items():
                executable = bins / name
                executable.write_text(source)
                executable.chmod(0o700)
            env = os.environ.copy()
            env.update(PATH=str(bins) + os.pathsep + env["PATH"], FAKE_ROOT=str(root), FAKE_SCENARIO=scenario)
            process = subprocess.Popen(
                ["bash", str(GATE)], cwd=root, env=env,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                start_new_session=True,
            )
            try:
                output, diagnostic = process.communicate(timeout=20)
                result = subprocess.CompletedProcess(process.args, process.returncode, output, diagnostic)
            finally:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.communicate(timeout=5)
            calls = [json.loads(line) for line in (root / "calls").read_text().splitlines()]
            state = json.loads((root / "state").read_text())
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            return calls, state, result.stdout + result.stderr

    def test_failed_inventory_never_authorizes_create(self):
        calls, _, output = self.run_gate("failed_inventory")
        self.assertFalse(any(c[:2] == ["volume", "create"] or c[0] == "run" for c in calls), output)

    def test_same_name_foreign_volume_is_checked_before_mount(self):
        calls, state, output = self.run_gate("foreign_race")
        self.assertFalse(any(c[0] == "run" for c in calls), output)
        self.assertEqual(state["volume_owner"], "foreign-owner")
        self.assertIsNone(state["mounted_owner"])
        self.assertFalse(any(c[:2] == ["volume", "rm"] for c in calls), output)

    def test_lost_successful_run_response_reconciles_and_cleans_exact_id(self):
        calls, state, output = self.run_gate("lost_exact")
        self.assertFalse(state["outer"], output)
        self.assertIsNone(state["volume_owner"], output)
        self.assertIn(["rm", "--force", OUTER], calls)
        self.assertIn(["volume", "rm", VOLUME], calls)
        create = next(i for i,c in enumerate(calls) if c[:2] == ["volume", "create"])
        inspect = next(i for i,c in enumerate(calls) if i>create and c[:2] == ["volume", "inspect"])
        users = next(i for i,c in enumerate(calls) if i>inspect and c[0]=="ps" and "volume="+VOLUME in c)
        mount = next(i for i,c in enumerate(calls) if c[0]=="run")
        self.assertLess(create, inspect)
        self.assertLess(inspect, users)
        self.assertLess(users, mount)

    def test_lost_run_profile_mismatch_preserves_everything(self):
        for scenario in ["lost_mismatch", "lost_wrong_image", "lost_wrong_owner", "lost_extra_mount", "lost_wrong_resources", "lost_wrong_profile"]:
            with self.subTest(scenario=scenario):
                calls, state, output = self.run_gate(scenario)
                self.assertTrue(state["outer"], output)
                self.assertEqual(state["volume_owner"], OWNER)
                self.assertFalse(any(c[0] in ["rm", "exec"] or c[:2] == ["volume", "rm"] for c in calls), output)

    def test_empty_successful_run_response_is_reconciled_before_cleanup(self):
        calls,state,output=self.run_gate("lost_empty_success")
        self.assertFalse(state["outer"],output)
        self.assertIsNone(state["volume_owner"],output)
        self.assertIn(["rm","--force",OUTER],calls)

    def test_lost_created_but_not_started_guest_is_removed_without_exec(self):
        calls,state,output=self.run_gate("lost_created_only")
        self.assertFalse(state["outer"],output)
        self.assertIsNone(state["volume_owner"],output)
        self.assertFalse(any(c[0]=="exec" for c in calls),output)
        self.assertIn(["rm","--force",OUTER],calls)

    def test_failed_reconciliation_inventory_preserves_uncertain_resources(self):
        calls,state,output=self.run_gate("lost_failed_inventory")
        self.assertTrue(state["outer"],output)
        self.assertEqual(state["volume_owner"],OWNER)
        self.assertFalse(any(c[0] in ["rm","exec"] or c[:2]==["volume","rm"] for c in calls),output)

    def test_failed_container_inventory_does_not_authorize_creation(self):
        calls, _, output = self.run_gate("failed_container_inventory")
        self.assertFalse(any(c[:2]==["volume","create"] or c[0]=="run" for c in calls),output)

    def test_inspect_error_cannot_hide_preexisting_volume(self):
        calls, state, output = self.run_gate("inspect_error_existing")
        self.assertEqual(state["volume_owner"],"foreign-owner")
        self.assertFalse(any(c[:2]==["volume","create"] or c[0]=="run" for c in calls),output)

    def test_bad_volume_profile_or_failed_user_inventory_prevents_mount(self):
        for scenario in ["wrong_driver","wrong_scope","nonempty_options","failed_users","foreign_users"]:
            with self.subTest(scenario=scenario):
                calls,state,output=self.run_gate(scenario)
                self.assertIsNone(state["mounted_owner"])
                self.assertEqual(state["volume_owner"],OWNER)
                self.assertFalse(any(c[0]=="run" or c[:2]==["volume","rm"] for c in calls),output)

    def test_failed_final_inventory_never_claims_absence(self):
        _,state,output=self.run_gate("failed_final_inventory")
        self.assertFalse(state["outer"])
        self.assertIsNone(state["volume_owner"])
        self.assertIn("volume inventory unavailable",output)
        self.assertNotIn("owned outer volume absent",output)


class ImagePreloadTest(unittest.TestCase):
    def run_preload(self, change=None, pull_exit=0, images=None):
        source = GATE.read_text().split("# IMAGE_PRELOAD_BEGIN\n", 1)[1].split("# IMAGE_PRELOAD_END", 1)[0]
        digest = "sha256:" + "a" * 64
        reference = "postgres:17.6-alpine@" + digest
        host = {"Id": "sha256:" + "b" * 64, "Os": "linux", "Architecture": "arm64",
                "RepoDigests": ["postgres@" + digest], "Config": {"Entrypoint": ["entrypoint"], "User": ""},
                "RootFS": {"Type": "layers", "Layers": ["sha256:" + "c" * 64, "sha256:" + "d" * 64]}}
        guest = json.loads(json.dumps(host))
        if images is not None:
            host, guest = json.loads(json.dumps(images))
            reference = "postgres:17.6-alpine@" + host["RepoDigests"][0].split("@", 1)[1]
        if change:
            change(guest)
        with tempfile.TemporaryDirectory(prefix="nerocd-storage-preload-test-") as temporary:
            root = Path(temporary)
            (root / "host.json").write_text(json.dumps([host]))
            (root / "guest.json").write_text(json.dumps([guest]))
            script = root / "preload.sh"
            script.write_text(source + r"""
evidence=$1
inner_docker() {
 printf '%s\n' "$*" >> "$evidence/calls"
 if [[ "$1" == pull ]]; then return "$3"; fi
 cat "$evidence/guest.json"
}
""".replace('return "$3"', 'return ' + str(pull_exit)) + '\npreload_image "' + reference + '" "$evidence/host.json" fixture\n')
            result = subprocess.run(["bash", "-e", str(script), str(root)], capture_output=True, text=True, timeout=5)
            calls = (root / "calls").read_text().splitlines()
            proof_path = root / "preload-fixture-proof.json"
            proof = json.loads(proof_path.read_text()) if proof_path.exists() else None
            return result, calls, proof

    def test_exact_pin_preloaded_before_inspection(self):
        result, calls, proof = self.run_preload()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(proof["id_relation"], "same_reported_id")
        self.assertEqual(proof["host_reported_id"], proof["guest_reported_id"])
        self.assertEqual(calls, ["pull --platform linux/arm64 postgres:17.6-alpine@sha256:" + "a" * 64,
                                 "image inspect postgres:17.6-alpine@sha256:" + "a" * 64])

    def test_bad_digest_platform_content_or_config_blocks(self):
        changes = {
            "lost digest lookup": lambda value: value.update(RepoDigests=[]),
            "wrong digest": lambda value: value.update(RepoDigests=["postgres@sha256:" + "c" * 64]),
            "wrong repository": lambda value: value.update(RepoDigests=["foreign/image@sha256:" + "a" * 64]),
            "wrong platform": lambda value: value.update(Architecture="amd64"),
            "wrong variant": lambda value: value.update(Variant="v8"),
            "non-string variant": lambda value: value.update(Variant=False),
            "missing layers": lambda value: value.pop("RootFS"),
            "empty layers": lambda value: value["RootFS"].update(Layers=[]),
            "reordered layers": lambda value: value["RootFS"]["Layers"].reverse(),
            "wrong layers": lambda value: value["RootFS"].update(Layers=["sha256:" + "e" * 64]),
            "malformed layer": lambda value: value["RootFS"].update(Layers=["not-a-digest"]),
            "wrong content": lambda value: value.update(Id="sha256:" + "c" * 64),
            "wrong config": lambda value: value.update(Config={"User": "unexpected"}),
        }
        for name, change in changes.items():
            with self.subTest(name=name):
                result, calls, proof = self.run_preload(change)
                self.assertNotEqual(result.returncode, 0)
                self.assertIsNone(proof)
                self.assertEqual(len(calls), 2)

    def test_pull_failure_never_inspects_or_starts(self):
        result, calls, proof = self.run_preload(pull_exit=19)
        self.assertEqual(result.returncode, 19)
        self.assertEqual(len(calls), 1)

    def observed_postgres_images(self):
        # Public fields from c0c4089...1ab232 pinned-host-images and
        # preload-postgres.json. Host OCI descriptor and classic guest IDs differ.
        return json.loads(r"""
[
  {
    "Id": "sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94",
    "RepoDigests": [
      "postgres@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94"
    ],
    "Os": "linux",
    "Architecture": "arm64",
    "Variant": "v8",
    "Config": {
      "ExposedPorts": {
        "5432/tcp": {}
      },
      "Env": [
        "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
        "GOSU_VERSION=1.19",
        "LANG=en_US.utf8",
        "PG_MAJOR=17",
        "PG_VERSION=17.6",
        "PG_SHA256=e0630a3600aea27511715563259ec2111cd5f4353a4b040e0be827f94cd7a8b0",
        "DOCKER_PG_LLVM_DEPS=llvm19-dev \t\tclang19",
        "PGDATA=/var/lib/postgresql/data"
      ],
      "Entrypoint": [
        "docker-entrypoint.sh"
      ],
      "Cmd": [
        "postgres"
      ],
      "Volumes": {
        "/var/lib/postgresql/data": {}
      },
      "WorkingDir": "/",
      "StopSignal": "SIGINT"
    },
    "RootFS": {
      "Type": "layers",
      "Layers": [
        "sha256:0e64f2360a448b389c83fcbc3705e1364ccf43f004bb55233c689e7ce59a1ae9",
        "sha256:965e96d0897a96b80fda5e72155305fd47de0e9043fe5cc71cef7d3c6d0f0a90",
        "sha256:de1383344182e02236dc45d98c1f0db145f05d342a9f6c59cf46c32e91fbf043",
        "sha256:34c57e72ae2a35ef4791cf9334140c711c8763af9db0aa41fa0767e63e0c455b",
        "sha256:7da700cd465db8100913d74ca0548e33278e810bd593c65190eb765044e74ad2",
        "sha256:eea405ba15068b301a24b421e0da949d2cd9ea957d163ce6db2faed3f2e00fc5",
        "sha256:5ce0dcb85bd093a27df5c52d0f6d76fa914d9dc0d3b9e52b4743d66cd7f9316a",
        "sha256:b4298a1c153ea6e97a06dfc480ba9702aceef58742c79cd956cebe0e132378a6",
        "sha256:ad7cdd7f4d2aa089558e8a95739619699515aed99d34719a37894cfe196d8da7",
        "sha256:9eccf8b57d180ec87635195f28a28d80b16396f6d478b86d58407ef057522b6d"
      ]
    },
    "Descriptor": {
      "mediaType": "application/vnd.oci.image.index.v1+json",
      "digest": "sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94",
      "size": 10293
    }
  },
  {
    "Id": "sha256:29e645e20571728f22df398b9954bb6c1cefec390d99ed4e26308fc2f52d6a37",
    "RepoDigests": [
      "postgres@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94"
    ],
    "Os": "linux",
    "Architecture": "arm64",
    "Variant": "v8",
    "Config": {
      "ExposedPorts": {
        "5432/tcp": {}
      },
      "Env": [
        "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
        "GOSU_VERSION=1.19",
        "LANG=en_US.utf8",
        "PG_MAJOR=17",
        "PG_VERSION=17.6",
        "PG_SHA256=e0630a3600aea27511715563259ec2111cd5f4353a4b040e0be827f94cd7a8b0",
        "DOCKER_PG_LLVM_DEPS=llvm19-dev \t\tclang19",
        "PGDATA=/var/lib/postgresql/data"
      ],
      "Entrypoint": [
        "docker-entrypoint.sh"
      ],
      "Cmd": [
        "postgres"
      ],
      "Volumes": {
        "/var/lib/postgresql/data": {}
      },
      "WorkingDir": "/",
      "StopSignal": "SIGINT"
    },
    "RootFS": {
      "Type": "layers",
      "Layers": [
        "sha256:0e64f2360a448b389c83fcbc3705e1364ccf43f004bb55233c689e7ce59a1ae9",
        "sha256:965e96d0897a96b80fda5e72155305fd47de0e9043fe5cc71cef7d3c6d0f0a90",
        "sha256:de1383344182e02236dc45d98c1f0db145f05d342a9f6c59cf46c32e91fbf043",
        "sha256:34c57e72ae2a35ef4791cf9334140c711c8763af9db0aa41fa0767e63e0c455b",
        "sha256:7da700cd465db8100913d74ca0548e33278e810bd593c65190eb765044e74ad2",
        "sha256:eea405ba15068b301a24b421e0da949d2cd9ea957d163ce6db2faed3f2e00fc5",
        "sha256:5ce0dcb85bd093a27df5c52d0f6d76fa914d9dc0d3b9e52b4743d66cd7f9316a",
        "sha256:b4298a1c153ea6e97a06dfc480ba9702aceef58742c79cd956cebe0e132378a6",
        "sha256:ad7cdd7f4d2aa089558e8a95739619699515aed99d34719a37894cfe196d8da7",
        "sha256:9eccf8b57d180ec87635195f28a28d80b16396f6d478b86d58407ef057522b6d"
      ]
    },
    "GraphDriver": {
      "Name": "overlay2"
    }
  }
]
""")

    def test_observed_cross_store_content_and_reported_ids(self):
        host, guest = self.observed_postgres_images()
        self.assertNotEqual(host["Id"], guest["Id"])
        result, calls, proof = self.run_preload(images=[host, guest])
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(proof["id_relation"], "host_oci_descriptor_to_classic_overlay2")
        self.assertEqual(proof["host_reported_id"], host["Id"])
        self.assertEqual(proof["guest_reported_id"], guest["Id"])
        self.assertEqual(proof["platform"], ["linux", "arm64", "v8"])
        self.assertEqual(proof["rootfs_layers"], 10)
        self.assertEqual(len(calls), 2)

    def test_cross_store_pin_platform_config_layers_and_representation_stay_closed(self):
        changes = {
            "pin": lambda host, guest: guest.update(RepoDigests=["postgres@sha256:" + "f" * 64]),
            "repository": lambda host, guest: guest.update(RepoDigests=["foreign/image@" + host["Id"]]),
            "platform": lambda host, guest: guest.update(Architecture="amd64"),
            "variant": lambda host, guest: guest.update(Variant=""),
            "config": lambda host, guest: guest["Config"].update(Cmd=["foreign-command"]),
            "layer": lambda host, guest: guest["RootFS"]["Layers"].__setitem__(0, "sha256:" + "f" * 64),
            "layer order": lambda host, guest: guest["RootFS"]["Layers"].reverse(),
            "unexplained host ID": lambda host, guest: host.pop("Descriptor"),
            "wrong host descriptor digest": lambda host, guest: host["Descriptor"].update(digest="sha256:" + "f" * 64),
            "unsupported descriptor": lambda host, guest: host["Descriptor"].update(mediaType="unsupported"),
            "invalid descriptor size": lambda host, guest: host["Descriptor"].update(size=0),
            "unqualified guest store": lambda host, guest: guest["GraphDriver"].update(Name="unknown"),
            "missing guest store": lambda host, guest: guest.pop("GraphDriver"),
            "malformed guest ID": lambda host, guest: guest.update(Id="unexplained"),
        }
        for name, change in changes.items():
            with self.subTest(name=name):
                host, guest = self.observed_postgres_images()
                change(host, guest)
                result, calls, proof = self.run_preload(images=[host, guest])
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIsNone(proof)
                self.assertEqual(len(calls), 2)

    def run_base_services(self, invalid_registry=False, pull_exit=0):
        source = GATE.read_text()
        preload = source.split("# IMAGE_PRELOAD_BEGIN\n", 1)[1].split("# IMAGE_PRELOAD_END", 1)[0]
        transfer = source.split("# BASE_IMAGE_PRELOAD_BEGIN\n", 1)[1].split("# BASE_IMAGE_PRELOAD_END", 1)[0]
        start = source.split("# BASE_SERVICE_START_BEGIN\n", 1)[1].split("# BASE_SERVICE_START_END", 1)[0]
        postgres = source.split("\npostgres=", 1)[1].splitlines()[0]
        registry = source.split("\nregistry=", 1)[1].splitlines()[0]
        host, guest = self.observed_postgres_images()
        # PostgreSQL uses the observed host descriptor / guest config pair.
        # The registry pair is synthetic and exercises selection of the second
        # independently qualified service without any Docker process.
        registry_host, registry_guest = json.loads(json.dumps([host, guest]))
        registry_digest = registry.split("@", 1)[1]
        registry_host.update(Id=registry_digest, RepoDigests=["registry@" + registry_digest])
        registry_host["Descriptor"]["digest"] = registry_digest
        registry_guest.update(Id="sha256:" + "9" * 64, RepoDigests=["registry@" + registry_digest])
        if invalid_registry:
            registry_guest["Config"]["Cmd"] = ["unqualified-command"]
        with tempfile.TemporaryDirectory(prefix="nerocd-storage-service-image-test-") as temporary:
            root = Path(temporary)
            (root / "pinned-host-images").write_text(json.dumps([host, registry_host]))
            (root / "postgres.json").write_text(json.dumps([guest]))
            (root / "registry.json").write_text(json.dumps([registry_guest]))
            script = root / "services.sh"
            script.write_text(preload + r"""
evidence=$1
owner=offline-fixture
postgres='@POSTGRES@'
registry='@REGISTRY@'
inner_docker() {
 printf '%s\n' "$*" >> "$evidence/calls"
 if [[ "$1" == pull ]]; then return @PULL_EXIT@; fi
 if [[ "$1 $2" == 'image inspect' ]]; then
  if [[ "$3" == "$postgres" ]]; then cat "$evidence/postgres.json"
  elif [[ "$3" == "$registry" ]]; then cat "$evidence/registry.json"
  else return 88; fi
 elif [[ "$1" == run ]]; then printf '%064d\n' 1
 else return 89; fi
}
""".replace("@POSTGRES@", postgres).replace("@REGISTRY@", registry).replace("@PULL_EXIT@", str(pull_exit)) + transfer + start)
            result = subprocess.run(["bash", "-e", str(script), str(root)], capture_output=True, text=True, timeout=5)
            calls = (root / "calls").read_text().splitlines()
            proof_path = root / "preload-postgres-proof.json"
            proof = json.loads(proof_path.read_text()) if proof_path.exists() else None
            return result, calls, proof, postgres, registry, host, guest

    def test_base_services_start_qualified_digest_with_different_host_and_guest_ids(self):
        result, calls, proof, postgres, registry, host, guest = self.run_base_services()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotEqual(host["Id"], guest["Id"])
        self.assertEqual(proof["id_relation"], "host_oci_descriptor_to_classic_overlay2")
        self.assertEqual(proof["guest_reported_id"], guest["Id"])
        self.assertEqual(calls[:4], ["pull --platform linux/arm64 " + postgres,
                                    "image inspect " + postgres,
                                    "pull --platform linux/arm64 " + registry,
                                    "image inspect " + registry])
        self.assertEqual(len(calls), 6)
        self.assertTrue(calls[4].startswith("run --pull=never "), calls)
        self.assertIn(" " + postgres + " postgres -c max_wal_size=128MB -c min_wal_size=32MB", calls[4])
        self.assertTrue(calls[5].startswith("run --pull=never "), calls)
        self.assertTrue(calls[5].endswith(" " + registry), calls)
        for call in calls[4:]:
            self.assertNotIn(" " + host["Id"] + " ", call)
            self.assertNotIn(" " + guest["Id"] + " ", call)

    def test_base_services_never_start_after_failed_pull_or_content_qualification(self):
        for options in [{"pull_exit": 19}, {"invalid_registry": True}]:
            with self.subTest(options=options):
                result, calls, *_ = self.run_base_services(**options)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(any(call.startswith("run ") for call in calls), calls)


class BrowserCancellationTest(unittest.TestCase):
    """Run the real driver with inert Playwright resources, without a browser."""
    def run_driver(self, delivered_signal=None, cleanup_failure="", complete=False, retention=False, diagnostic_failure=""):
        driver = GATE.with_name("remote_browser.mjs")
        with tempfile.TemporaryDirectory(prefix="nerocd-storage-browser-stop-test-") as temporary:
            root = Path(temporary)
            module = root / "web/app/node_modules/@playwright/test"
            module.mkdir(parents=True)
            (root / "web/app/package.json").write_text("{}")
            (root / "browser-ui-manifest.json").write_text(json.dumps({"schema": "nerocd.ui-assets/v1", "files": {"index.html": hashlib.sha256(b"fixture").hexdigest()}}))
            (module / "index.js").write_text(r"""
const fs = require("node:fs");
const root = process.env.NEROCD_STORAGE_REMOTE_HOST_EVIDENCE;
const mark = value => {
 fs.appendFileSync(root + "/closed", value + "\n");
 if (fs.existsSync(root + "/browser-safe.json")) fs.writeFileSync(root + "/receipt-before-cleanup", "invalid");
};
const close = async value => {
 mark(value);
 if (process.env.STORAGE_BROWSER_TEST_CLOSE_FAILURE === value) throw Error("private close failure must not appear in receipt");
};
const complete = process.env.STORAGE_BROWSER_TEST_COMPLETE === "1";
const retention=process.env.STORAGE_BROWSER_TEST_RETENTION==="1";
const diagnosticFailure=process.env.STORAGE_BROWSER_TEST_DIAGNOSTIC_FAILURE;
let activePhase="";
const phases = ["target", "validation", "backup", "denied", "retry", "restore", "archive_alert", ...(retention?["retention","retention_history"]:[]), ...(diagnosticFailure?["deletion"]:[]), "complete"];
let rejectPending, pendingHandle;
const api = {
 get: async path => {
  fs.writeFileSync(root + "/started", "polling");
  if (complete) return path.includes("/__storage_remote_fixture/")
   ? {status: () => 200, json: async () => {const phase=phases.shift();activePhase=phase;const archive=phase==="archive_alert";return {phase,deletion_mode:diagnosticFailure?"true":"",retention_mode:retention?"true":"",resource_name:"retention resource",retention_backup_ids:"retention_old,retention_middle,retention_new",retention_execution_id:"retention_execution",resource_id:"resource",environment_id:"environment",target_id:"target",backup_id:archive?"failed_backup":"backup",source_id:archive?"failed_archive":"failed_upload",recovery_url:process.env.NEROCD_STORAGE_REMOTE_BROWSER_ORIGIN+"/projects?project_id=proj_platform&environment_id=environment&resource_id=resource&backup_id="+(archive?"failed_backup&source_kind=storage_backup":"backup&source_kind=storage_transfer&transfer_id=failed_upload"),access_key:"fixture",secret_key:"fixture"}}}
   : {status: () => 200, body: async () => Buffer.from("fixture")};
  if (process.env.STORAGE_BROWSER_TEST_EXPIRE === "1") {
   const expired = Date.now() + 25 * 60 * 1000;
   Date.now = () => expired;
   return {status: () => 403};
  }
  return new Promise((resolve, reject) => {rejectPending = reject; pendingHandle = setInterval(() => {}, 1000);});
 },
 post: async () => ({status: () => 204}),
 dispose: async () => {clearInterval(pendingHandle); rejectPending?.(Error("owned API disposed")); await close("api");}
};
let responseIndex = 0, contextIndex = 0, browserClosed = false, currentURL="";
const page = new Proxy({}, {get: (_, name) => {
 if (name === "then") return undefined;
 if (name === "goto") return async value=>{currentURL=value;if(activePhase==="deletion"&&diagnosticFailure==="navigation"){const error=Error("private diagnostic secret URL body DOM");error.name="TimeoutError";throw error;}};
 if (name === "check" && activePhase==="deletion" && diagnosticFailure==="checkbox") return async()=>{const error=Error("private diagnostic secret URL body DOM");error.name="private diagnostic secret class";throw error;};
 if (name === "url") return ()=>currentURL;
 if (name === "page") return ()=>page;
 if (["getByRole","getByLabel","locator","filter"].includes(name)) return () => page;
 if (name === "evaluateAll") return async () => "target:1";
 if (name === "textContent") return async () => "View scheduled archive backup";
 if (name === "evaluate") return async () => false;
 if (name === "waitForResponse") return async predicate => {
  // Backup GETs are independent of the mutation response sequence below.
  for(const id of ["backup","failed_backup","retention_old","retention_middle","retention_new"]) {
   const read={status:()=>200,url:()=>process.env.NEROCD_STORAGE_REMOTE_BROWSER_ORIGIN+"/api/v1/storage-backups/"+id,
    request:()=>({method:()=>"GET",timing:()=>({startTime:Date.now()+1})}),json:async()=>({id,copies:[]})};
   if(predicate(read))return read;
  }
  const index=responseIndex++;
  const values=[{id:"target"},{id:"validation"},{},{id:"schedule",revision:1,remote_target:{target_id:"target"}},{id:"schedule",revision:2},{id:"schedule",revision:3,enabled:false},{id:"upload"},{operation:{id:"restore"}}];
  values.push({enabled:true,automatic:false,revision:1,local:{keep_count:1},remote:{keep_count:2}},
   {id:"retention_preview",policy_revision:1,plan_hash:"sha256:"+"a".repeat(64),evaluated_at:"2026-10-26T12:00:00Z",items:[{backup_id:"retention_old",location:"local",selected:true},{backup_id:"retention_middle",location:"local",selected:true},{backup_id:"retention_old",location:"remote",selected:true}]},
   {},{id:"retention_execution",preview_id:"retention_preview",plan_hash:"sha256:"+"a".repeat(64)});
  const statuses=[200,202,503,200,200,200,202,202,200,200,503,202];
  return {status: () => index===12&&diagnosticFailure==="preview_status"?409:statuses[index],json: async () => values[index],
   request: () => ({postDataJSON: () => index>=10?{request_id:"same_request",preview_id:"retention_preview",expected_policy_revision:1,expected_plan_hash:"sha256:"+"a".repeat(64)}:({remote_target:{target_id:"target"},remote_source:{transfer_id:"upload"}})})};
 };
 return async () => {};
}});
const makeContext = () => {const name=contextIndex++ === 0 ? "context" : "viewer";return {newPage: async () => page,request:{post:async()=>({status:()=>403})},close: async () => {
 await new Promise(resolve => setTimeout(resolve, 10));
 if (browserClosed) throw Error("parent closed before child settled");
 await close(name);
}};};
module.exports = {
 request: {newContext: async () => api},
 chromium: {launch: async () => ({newContext: async () => makeContext(), close: async () => {browserClosed = true; await close("browser");}})},
 expect: () => {let assertion;assertion=new Proxy({}, {get: (_,name) => name==="not"?assertion:async () => {if (!complete) throw Error("UI must not execute in cancellation test");}});return assertion}
};
""")
            environment = os.environ.copy()
            environment.update(NEROCD_STORAGE_REMOTE_BROWSER_ORIGIN="https://127.0.0.1:49155",
                               NEROCD_STORAGE_REMOTE_BROWSER_KEY="a" * 64,
                               NEROCD_STORAGE_REMOTE_HOST_EVIDENCE=str(root),
                               STORAGE_BROWSER_TEST_EXPIRE="0" if delivered_signal or complete else "1",
                               STORAGE_BROWSER_TEST_CLOSE_FAILURE=cleanup_failure,
                               STORAGE_BROWSER_TEST_COMPLETE="1" if complete else "0",
                               STORAGE_BROWSER_TEST_RETENTION="1" if retention else "0",
                               STORAGE_BROWSER_TEST_DIAGNOSTIC_FAILURE=diagnostic_failure)
            process = subprocess.Popen(["node", str(driver)], cwd=root, env=environment,
                                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                       text=True, start_new_session=True)
            try:
                deadline = time.monotonic() + 5
                while not (root / "started").exists():
                    if process.poll() is not None or time.monotonic() >= deadline:
                        self.fail("driver did not reach pending phase request")
                    time.sleep(0.01)
                if delivered_signal:
                    process.send_signal(delivered_signal)
                output, _ = process.communicate(timeout=3)
                expected_code = 128 + delivered_signal if delivered_signal else (1 if cleanup_failure or diagnostic_failure or not complete else 0)
                self.assertEqual(process.returncode, expected_code, output)
                expected_closed = {"api", "context", "browser", "viewer"} if complete and not diagnostic_failure else {"api", "context", "browser"}
                self.assertEqual(set((root / "closed").read_text().splitlines()), expected_closed)
                self.assertFalse((root / "receipt-before-cleanup").exists(), "receipt was published before cleanup")
                safe = json.loads((root / "browser-safe.json").read_text())
                self.assertEqual(safe["status"], "passed" if expected_code == 0 else "failed")
                if expected_code:
                    expected_failure = "browser_workflow_cancelled" if delivered_signal else ("browser_cleanup_failed" if complete and not diagnostic_failure else "browser_workflow_failed")
                    self.assertEqual(safe["failure"], expected_failure)
                if retention:
                    for marker in ["retention_policy_and_exact_preview", "retention_identical_execution_retry", "retention_history_and_copy_availability"] + ([] if diagnostic_failure else ["retention_viewer_read_only_and_denied"]):
                        self.assertTrue(safe.get(marker), marker)
                if diagnostic_failure:
                    expected_action = {"navigation":"navigate_projects","checkbox":"select_local_copy","preview_status":"preview_http"}[diagnostic_failure]
                    expected_class = "TimeoutError" if diagnostic_failure == "navigation" else "Error"
                    self.assertEqual(safe["diagnostics"][-1], {"phase":"deletion","action":expected_action,"error_class":expected_class})
                    if diagnostic_failure == "preview_status":
                        self.assertIn({"phase":"deletion","action":"preview_http","http_status":409}, safe["diagnostics"])
                    self.assertIn('"browser_diagnostic"', output)
                    self.assertNotIn("private diagnostic secret", output + json.dumps(safe))
                    self.assertNotIn("manual_deletion_selection_and_exact_retry", safe)
                if cleanup_failure:
                    self.assertEqual(safe["cleanup_failures"], [cleanup_failure])
                    self.assertNotIn("private close failure", json.dumps(safe))
            finally:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGKILL)
                process.communicate(timeout=3)

    def test_exact_backup_selection_waits_for_delayed_same_id_copy_refresh(self):
        # The old button and same-ID metadata stay actionable throughout both gates.
        script = r"""
const fs=require("node:fs"),assert=require("node:assert/strict");
const source=fs.readFileSync(process.argv[1],"utf8");
const helper=source.slice(source.indexOf("async function selectBackup(p, id)"),source.indexOf("function deliveredRecoveryURL"));
const run=new Function("recordDiagnostic","expect",helper+"return selectBackup;");
const tick=()=>new Promise(setImmediate);
(async()=>{
 for(const scenario of ["republished_generation","refresh_denied","http_denied"]) {
  const events=[];let finishHTTP,finishDOM,settled=false;
  const network=new Promise(resolve=>{finishHTTP=resolve;});
  const rendered=new Promise(resolve=>{finishDOM=resolve;});
  const response={status:()=>scenario==="http_denied"?403:200,
   json:async()=>({id:"exact_backup",copies:[
    {location:"local",availability:"deleted",protected_reasons:["superseded"]},
    {location:"remote",availability:"available",protected_reasons:[]},
    {location:"local",availability:"available",protected_reasons:[]}]})};
  const metadata={locator:selector=>{assert.equal(selector,"ul > li");return "copies";}};
  const panel={page:()=>({waitForResponse:predicate=>{
   events.push("armed");
   const sample=(method,id,startTime)=>({url:()=>"https://fixture/api/v1/storage-backups/"+id,request:()=>({method:()=>method,timing:()=>({startTime})})});
   assert.equal(predicate(sample("GET","exact_backup",0)),false);
   assert.equal(predicate(sample("POST","exact_backup",Date.now()+1)),false);
   assert.equal(predicate(sample("GET","foreign",Date.now()+1)),false);
   assert.equal(predicate(sample("GET","exact_backup",Date.now()+1)),true);
   return network;
  }}),getByRole:(role,options)=>{
   assert.equal(role,"button");assert.equal(options.exact,true);
   return {click:async()=>{
    if(options.name==="Refresh archive status") {events.push("refresh_clicked");if(scenario==="refresh_denied")throw Error("denied");}
    else {assert.equal(options.name,"Backup exact_backup");events.push("old_button_clicked");}
   }};
  },locator:selector=>{assert.equal(selector,'dl[aria-label="Selected backup metadata"]');return metadata;}};
  const select=run(action=>events.push(action),value=>({toContainText:async id=>{
   assert.equal(value,metadata);assert.equal(id,"exact_backup");events.push("same_id_metadata_present");
  },toHaveText:async expected=>{
   assert.equal(value,"copies");assert.deepEqual(expected,["local: deleted · superseded","remote: available","local: available"]);
   events.push("waiting_for_render");await rendered;events.push("fresh_copies_rendered");
  }}));
  const outcome=select(panel,"exact_backup").then(()=>{settled=true;return "passed";},()=>{settled=true;return "failed";});
  await tick();
  if(scenario==="refresh_denied") {
   assert.equal(await outcome,"failed");assert.equal(events.includes("old_button_clicked"),false);finishHTTP(response);continue;
  }
  assert.equal(events.includes("old_button_clicked"),true);
  assert.equal(settled,false,"old same-ID button must not complete selection before refreshed GET");
  finishHTTP(response);await tick();
  if(scenario==="http_denied") {assert.equal(await outcome,"failed");assert.equal(events.includes("same_id_metadata_present"),false);continue;}
  assert.equal(events.includes("same_id_metadata_present"),true);
  assert.equal(events.includes("waiting_for_render"),true);
  assert.equal(settled,false,"same-ID metadata must not complete selection before fresh copy inventory renders");
  finishDOM();assert.equal(await outcome,"passed");
  assert.equal(events.at(-1),"fresh_copies_rendered");
 }
})().catch(error=>{console.error(error);process.exitCode=1;});
"""
        result = subprocess.run(["node", "-e", script, str(GATE.with_name("remote_browser.mjs"))], capture_output=True, text=True, timeout=5)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_manual_phase_failures_report_only_safe_action_status_and_class(self):
        # Inert driver failure injection proves diagnostics, not live UI behavior.
        for failure in ["navigation", "checkbox", "preview_status"]:
            with self.subTest(failure=failure):
                self.run_driver(complete=True, retention=True, diagnostic_failure=failure)

    def test_diagnostic_labels_status_and_storage_are_bounded(self):
        script = r"""
const fs=require("node:fs"),assert=require("node:assert/strict");
const source=fs.readFileSync(process.argv[1],"utf8");
const diagnostics=source.slice(source.indexOf("const diagnosticPhases"),source.indexOf("let api, browser, context, page;"));
new Function("assert", "let stopped=false;"+diagnostics+`
 setDiagnosticPhase("private diagnostic secret");
 recordDiagnostic("private diagnostic secret", "private diagnostic secret", {name:"private diagnostic secret"});
 assert.deepEqual(diagnosticTrace.at(-1),{phase:"unknown_phase",action:"unknown_action",error_class:"Error"});
 for(let i=0;i<100;i++) recordDiagnostic("preview_http",200);
 assert.equal(diagnosticTrace.length,64);
 assert.equal(JSON.stringify(diagnosticTrace).includes("private diagnostic secret"),false);
 for(const status of [NaN,Infinity,-1,99,600,200.5,"409"]) {
  recordDiagnostic("preview_http",status);
  assert.equal(Object.hasOwn(diagnosticTrace.at(-1),"http_status"),false);
 }
`)(assert);
"""
        result = subprocess.run(["node", "-e", script, str(GATE.with_name("remote_browser.mjs"))], capture_output=True, text=True, timeout=5)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_retention_phase_driver_wiring_with_inert_browser_resources(self):
        # This tests sequencing and cleanup only; it is not browser qualification.
        self.run_driver(complete=True, retention=True)

    def test_overlapping_cleanup_joins_children_and_late_resources(self):
        # Evaluate the actual cleanup function without starting the workflow.
        script = r"""
const fs = require("node:fs"), assert = require("node:assert/strict");
const source = fs.readFileSync(process.argv[1], "utf8");
const cleanup = source.slice(source.indexOf("let api, browser, context, page;"), source.indexOf("function stop(code)"));
const run = new Function("exercise", cleanup + `
 return exercise({closeOwned, cleanupFailures,
  set: resources => {({api, browser, context} = resources);}});`);
(async () => {
 for (const failure of ["", "context", "browser", "api"]) {
  await run(async ({closeOwned, cleanupFailures, set}) => {
   const events = [], calls = new Map();
   let release;
   const pending = new Promise(resolve => {release = resolve;});
   const resource = name => ({[name === "api" ? "dispose" : "close"]: async () => {
    calls.set(name, (calls.get(name) ?? 0) + 1);
    events.push(name + ":start");
    if (name === "context") await pending;
    events.push(name + ":settled");
    if (failure === name) throw Error("private failure");
   }});
   set({context:resource("context"), browser:resource("browser"), api:resource("api")});
   const first = closeOwned(), overlapping = closeOwned();
   await new Promise(resolve => setImmediate(resolve));
   assert.deepEqual(events, ["context:start"]);
   release();
   assert.deepEqual(await Promise.all([first, overlapping]), [!failure, !failure]);
   assert(events.indexOf("context:settled") < events.indexOf("browser:start"));
   assert.deepEqual([...calls.values()], [1, 1, 1]);
   assert.deepEqual([...cleanupFailures], failure ? [failure] : []);
   assert.equal(await closeOwned(), !failure);
   assert.deepEqual([...calls.values()], [1, 1, 1]);
  });
 }
 await run(async ({closeOwned, set}) => {
  assert.equal(await closeOwned(), true);
  const closed = [];
  // A resource creation completing after cancellation is joined by finally.
  set({api:{dispose:async () => closed.push("api")}});
  assert.equal(await closeOwned(), true);
  set({api:undefined, context:{close:async () => closed.push("context")}, browser:{close:async () => closed.push("browser")}});
  assert.equal(await closeOwned(), true);
  assert.deepEqual(closed, ["api", "context", "browser"]);
 });
})().catch(error => {console.error(error); process.exitCode = 1;});
"""
        result = subprocess.run(["node", "-e", script, str(GATE.with_name("remote_browser.mjs"))],
                                capture_output=True, text=True, timeout=5)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_sigterm_stops_pending_poll_and_joins_owned_resources(self):
        self.run_driver(signal.SIGTERM)

    def test_sigint_stops_pending_poll_and_joins_owned_resources(self):
        self.run_driver(signal.SIGINT)

    def test_phase_deadline_closes_owned_resources(self):
        self.run_driver()

    def test_success_receipt_is_written_after_clean_shutdown(self):
        self.run_driver(complete=True)

    def test_each_owned_close_failure_prevents_success(self):
        for resource in ("context", "browser", "api"):
            with self.subTest(resource=resource):
                self.run_driver(cleanup_failure=resource, complete=True)

    def test_cleanup_failure_preserves_primary_failure(self):
        self.run_driver(cleanup_failure="context")

    def test_cleanup_failure_preserves_interrupt_status(self):
        for delivered_signal in (signal.SIGINT, signal.SIGTERM):
            for resource in ("context", "browser", "api"):
                with self.subTest(signal=delivered_signal, resource=resource):
                    self.run_driver(delivered_signal, cleanup_failure=resource)



class DeliveredRecoveryURLTest(unittest.TestCase):
    def test_delivered_panel_rejects_automatic_operator_mutation(self):
        script = r"""
const fs=require("node:fs"),assert=require("node:assert/strict");
const source=fs.readFileSync(process.argv[1],"utf8");
const start=source.indexOf("function deliveredRecoveryURL(");
const end=source.indexOf("const safe =",start);
const value={resource_id:"resource",environment_id:"environment",backup_id:"backup",source_id:"transfer",recovery_url:"https://127.0.0.1:49155/projects?project_id=proj_platform&environment_id=environment&resource_id=resource&backup_id=backup&source_kind=storage_transfer&transfer_id=transfer"};
(async()=>{
 for(const mutate of [false,true]){
  let observer,detached=false,opened="";const scopes=[];
  const locator={getByRole:(role,options)=>{scopes.push(options.name);return locator},locator:()=>locator};
  const page={...locator,on:(event,callback)=>{assert.equal(event,"request");observer=callback},off:(event,callback)=>{assert.equal(callback,observer);detached=true},url:()=>opened,goto:async url=>{opened=url;if(mutate)observer({method:()=>"POST",url:()=>"https://127.0.0.1:49155/api/v1/storage-backups/backup/transfers/retry"})}};
  const expect=()=>({toBeVisible:async()=>{},toContainText:async()=>{}});
  const run=new Function("origin","page","expect",source.slice(start,end)+"return deliveredPanel;")("https://127.0.0.1:49155",page,expect);
  if(mutate)await assert.rejects(run(value,"storage_transfer"),/caused a mutation/);else await run(value,"storage_transfer");
  assert.equal(opened,value.recovery_url);assert(detached);assert(scopes.includes("Backup notification recovery"));assert(scopes.includes("Remote archive transfers"));
 }
})().catch(error=>{console.error(error);process.exitCode=1});
"""
        result = subprocess.run(["node", "-e", script, str(GATE.with_name("remote_browser.mjs"))],capture_output=True,text=True,timeout=5)
        self.assertEqual(result.returncode,0,result.stdout+result.stderr)

    def test_exact_delivered_selection_and_rejections(self):
        script = r"""
const fs=require("node:fs"),assert=require("node:assert/strict");
const source=fs.readFileSync(process.argv[1],"utf8");
const start=source.indexOf("function deliveredRecoveryURL(");
const end=source.indexOf("async function deliveredPanel(",start);
const validate=new Function("origin",source.slice(start,end)+"return deliveredRecoveryURL;")("https://127.0.0.1:49155");
const upload={resource_id:"resource",environment_id:"environment",backup_id:"backup",source_id:"transfer",recovery_url:"https://127.0.0.1:49155/projects?project_id=proj_platform&environment_id=environment&resource_id=resource&backup_id=backup&source_kind=storage_transfer&transfer_id=transfer"};
assert.equal(validate(upload,"storage_transfer"),upload.recovery_url);
const archive={...upload,source_id:"operation",recovery_url:upload.recovery_url.replace("storage_transfer&transfer_id=transfer","storage_backup")};
assert.equal(validate(archive,"storage_backup"),archive.recovery_url);
assert.throws(()=>validate({...archive,source_id:undefined},"storage_backup"));
for(const [field,value] of Object.entries({project_id:"foreign",environment_id:"foreign",resource_id:"foreign",backup_id:"foreign",transfer_id:"foreign",source_kind:"storage_backup"})){
 const url=new URL(upload.recovery_url);url.searchParams.set(field,value);assert.throws(()=>validate({...upload,recovery_url:url.href},"storage_transfer"));
}
for(const changed of [upload.recovery_url.replace(":49155",":49156"),upload.recovery_url.replace("https:","http:"),upload.recovery_url+"&backup_id=backup",upload.recovery_url+"&token=secret",upload.recovery_url+"#fragment",upload.recovery_url.replace("/projects?","/other?"),upload.recovery_url.replace("127.0.0.1","user@127.0.0.1")])assert.throws(()=>validate({...upload,recovery_url:changed},"storage_transfer"));
assert.throws(()=>validate({...archive,recovery_url:archive.recovery_url+"&transfer_id=transfer"},"storage_backup"));
"""
        result = subprocess.run(["node", "-e", script, str(GATE.with_name("remote_browser.mjs"))],capture_output=True,text=True,timeout=5)
        self.assertEqual(result.returncode,0,result.stdout+result.stderr)


class PostgresCapacityTest(unittest.TestCase):
    def test_service_budget_and_wal_targets_keep_outer_profile(self):
        source = GATE.read_text()
        launch = next(line for line in source.splitlines() if line.startswith("pg=$(inner_docker run "))
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            script = r"""
set -euo pipefail
owner=owned; label=io.nerocd.storage-runtime.owner; pg_image=exact-pinned-image
inner_docker() { printf '%s\n' "$@" > "$1.args"; printf '%064d\n' 1; }
""" + launch
            result = subprocess.run(["bash", "-c", script], cwd=root, capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            args = (root / "run.args").read_text().splitlines()
        for flag, value in {"--memory": "1536m", "--cpus": "1", "--pids-limit": "128", "--tmpfs": "/var/lib/postgresql/data:rw,size=1g", "--label": "io.nerocd.storage-runtime.owner=owned", "--network": "host", "--pull=never": "--detach"}.items():
            self.assertEqual(args[args.index(flag) + 1], value)
        self.assertEqual(args[-6:], ["exact-pinned-image", "postgres", "-c", "max_wal_size=128MB", "-c", "min_wal_size=32MB"])
        self.assertFalse(set(args) & {"--privileged", "--mount", "--volume", "-v"})
        outer = next(line for line in source.splitlines() if line.startswith("returned_outer=$(outer_run "))
        self.assertIn("--cpus 4 --memory 6g --pids-limit 2048", outer)
        self.assertIn('postgres_capacity before-test', source)
        self.assertIn('postgres_capacity final || result=1', source)

    def diagnostic(self, identity="correct", fail="", pg="a" * 64):
        source = GATE.read_text().split("# POSTGRES_CAPACITY_BEGIN", 1)[1].split("# POSTGRES_CAPACITY_END", 1)[0]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            script = r"""
set -euo pipefail
outer=exact-owned-guest; owner=owned; label=io.nerocd.storage-runtime.owner
host_docker() {
 printf '%s\n' "$@" >> "$evidence/calls"
 printf '%s\n' END_CALL >> "$evidence/calls"
 [[ "$1 $2 $3 $4 $5 $6 $7" == 'exec exact-owned-guest timeout 5 docker --host unix:///var/run/docker.sock' ]] || return 90
 if [[ "$8" == inspect ]]; then
  [[ "${10}" == '{{.Id}} {{index .Config.Labels "io.nerocd.storage-runtime.owner"}}' ]] || return 91
  if [[ "$identity" == missing ]]; then return 1; fi
  if [[ "$identity" == wrong ]]; then printf '%s foreign\n' "$pg"; else printf '%s owned\n' "$pg"; fi
 else
  [[ "$8 $9" == "exec $pg" ]] || return 92
  if [[ "${10}" == "$fail" ]]; then echo unavailable; return 1; fi
  echo readonly-capacity-evidence
 fi
}
""" + source + '\npostgres_capacity final\n'
            result = subprocess.run(["bash", "-c", script], env={**os.environ, "evidence": str(root), "pg": pg, "identity": identity, "fail": fail}, capture_output=True, text=True, timeout=5)
            calls = (root / "calls").read_text() if (root / "calls").exists() else ""
            evidence = (root / "postgres-capacity-final").read_text() if (root / "postgres-capacity-final").exists() else ""
            return result, calls, evidence

    def test_readonly_owned_diagnostics_capture_space_wal_and_settings(self):
        result, calls, evidence = self.diagnostic()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(calls.count("END_CALL"), 4)
        self.assertIn("df\n-Pk\n/var/lib/postgresql/data", calls)
        self.assertIn("df\n-Pi\n/var/lib/postgresql/data", calls)
        self.assertIn("pg_ls_waldir()", calls)
        self.assertIn("n_dead_tup", calls)
        self.assertIn("'fsync','full_page_writes','synchronous_commit'", calls)
        self.assertNotIn("PGPASSWORD", calls)
        self.assertNotIn("POSTGRES_PASSWORD", calls)
        self.assertNotIn("--host=127.0.0.1", calls)
        self.assertIn("stage=final", evidence)
        self.assertIn("diagnostic_exit=0", evidence)
        self.assertNotIn("password", evidence.lower())

    def test_uncertain_identity_never_executes_in_database(self):
        for identity in ["wrong", "missing"]:
            with self.subTest(identity=identity):
                result, calls, evidence = self.diagnostic(identity=identity)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(calls.count("END_CALL"), 1)
                self.assertEqual(evidence, "")
        for pg in ["", "unvalidated-id"]:
            result, calls, evidence = self.diagnostic(pg=pg)
            self.assertEqual(result.returncode, 0 if not pg else 1)
            self.assertEqual(calls, "")
            self.assertEqual(evidence, "")

    def test_failed_measurement_retains_other_evidence_and_fails(self):
        result, calls, evidence = self.diagnostic(fail="df")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls.count("END_CALL"), 4)
        self.assertIn("readonly-capacity-evidence", evidence)
        self.assertIn("unavailable", evidence)
        self.assertIn("diagnostic_exit=1", evidence)



class RetentionModeContractTests(unittest.TestCase):
    def test_retention_is_distinct_and_keeps_exact_remote_profile(self):
        source = (Path(__file__).parent / "gate.sh").read_text()
        self.assertIn("--remote|--retention|--deletion)", source)
        self.assertIn("owner=nerocd-storage-retention-runtime-20261026-e4db0929", source)
        self.assertIn('NEROCD_STORAGE_RETENTION_RUNTIME=$retention_mode', source)
        self.assertIn("TestHostingStorageBackupRetentionCompiledRunnerHTTPS", source)
        self.assertIn('if sys.argv[2]=="true":required.remove("TestHostingStorageRemoteCompiledRunnerHTTPS")', source)
        fixture = (Path(__file__).parents[2] / "internal/api/hosting_storage_backup_retention_runtime_integration_test.go").read_text()
        for requirement in ["storageRetentionRuntimeAwait", "conditional S3 deletion", "remote_restore_republished_local", "source_preserved"]:
            self.assertIn(requirement, fixture)
        self.assertNotIn("t.Skip", fixture[fixture.index("func hostingStorageRetentionRuntimeWorkflow"):])

class DeletionModeContractTests(unittest.TestCase):
    def test_collects_joined_guest_markers_before_validating_and_rejects_bad_evidence(self):
        source = (Path(__file__).parent / "gate.sh").read_text()
        start = source.index("collect_runtime_evidence() {")
        collect = source[start:source.index("\n}\n", start) + 3]
        start = source.index(' if [[ "$deletion" == true ]]; then\n  collect_runtime_evidence')
        validation = source[start:source.index("\n fi\nelif", start) + len("\n fi")]
        required = ['local_absent','remote_retained','history_retained','remaining_remote_restore','new_volume_application_reads_and_writes','source_preserved','manual_remote_conditional_delete','final_copy_confirmation','final_history_retained']
        browser_required = ['manual_deletion_selection_and_exact_retry','manual_deletion_history_and_final_copy_warning','manual_deletion_viewer_history_and_denied','manual_deletion_remote_and_final_confirmation','manual_deletion_final_history']
        for kind in ["valid", "missing", "malformed", "missing_flag", "false_flag", "numeric_flag", "failed_status", "browser_missing_flag", "browser_failed", "browser_missing", "browser_malformed"]:
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                guest = root / "guest"
                guest.mkdir()
                output = root / "evidence"
                output.mkdir()
                marker = {"status": "passed", **dict.fromkeys(required, True)}
                browser = {"status": "passed", **dict.fromkeys(browser_required, True)}
                if kind == "missing_flag": del marker[required[0]]
                if kind == "false_flag": marker[required[0]] = False
                if kind == "numeric_flag": marker[required[0]] = 1
                if kind == "failed_status": marker["status"] = "failed"
                if kind == "browser_missing_flag": del browser[browser_required[0]]
                if kind == "browser_failed": browser["status"] = "failed"
                if kind != "missing":
                    (guest / "deletion-markers.json").write_text("invalid JSON" if kind == "malformed" else json.dumps(marker))
                if kind != "browser_missing":
                    (output / "browser-safe.json").write_text("invalid JSON" if kind == "browser_malformed" else json.dumps(browser))
                # Execute the actual collection function and validation block with
                # an inert copy tool. No Docker or runtime fixture is launched.
                script = """set -euo pipefail
evidence=$GATE_MARKER_EVIDENCE
outer=owned-id
deletion=true
host_docker() {
 [[ "$1" == cp && "$2" == 'owned-id:/fixture/evidence/.' && "$3" == "$evidence/runtime" ]]
 cp -R "$GATE_MARKER_GUEST/." "$3"
}
""" + collect + "\n" + validation
                env = os.environ.copy()
                env.update(GATE_MARKER_EVIDENCE=str(output), GATE_MARKER_GUEST=str(guest))
                result = subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode == 0, kind == "valid", result.stdout + result.stderr)
                self.assertFalse((output / "deletion-markers.json").exists())
                if kind != "missing": self.assertTrue((output / "runtime/deletion-markers.json").is_file())


    def test_deletion_requires_its_own_profile_and_physical_recovery_evidence(self):
        source = (Path(__file__).parent / "gate.sh").read_text()
        for requirement in ["owner=nerocd-storage-deletion-runtime-20261026", "NEROCD_STORAGE_DELETION_RUNTIME=$deletion_mode", "TestHostingStorageBackupDeletionCompiledRunnerHTTPS", "CHECK_DELETION_PG", "CHECK_DELETION_MARKERS", "deletion-markers.json"]:
            self.assertIn(requirement, source)
        fixture = (Path(__file__).parents[2] / "internal/api/hosting_storage_backup_deletion_runtime_integration_test.go").read_text()
        for requirement in ["ManualDeleteSelectedBackupLocalKeepsRemoteAndHistory", "remaining_remote_restore", "new_volume_application_reads_and_writes", "os.ErrNotExist"]:
            self.assertIn(requirement, fixture)
        self.assertNotIn("t.Skip", fixture[fixture.index("func hostingStorageDeletionRuntimeWorkflow"):])

class CatalogModeContractTests(unittest.TestCase):
    def test_catalog_requires_connected_browser_and_exact_loopback_profile(self):
        source = Path(__file__).with_name("gate.sh").read_text()
        self.assertIn('if [[ "$remote" == true || "$catalog" == true ]]; then browser_publication=true; fi', source)
        self.assertIn('if [[ "$browser_publication" == true ]]; then host_docker run --pull=never --publish 127.0.0.1::18190', source)
        self.assertIn('"$owner" "$volume" "$browser_publication" "$git_compose"', source)
        self.assertIn('node acceptance/runtime-hosting-catalog/browser.mjs', source)
        self.assertIn('wait "$test_pid"', source[source.index('elif [[ "$catalog" == true ]]; then'):])

    def test_catalog_rejects_missing_false_skipped_or_intercepted_browser_evidence(self):
        source = Path(__file__).with_name("gate.sh").read_text()
        validator = source.split("<<'CATALOG_BROWSER_PASS'\n", 1)[1].split("\nCATALOG_BROWSER_PASS", 1)[0]
        required = ["served_assets_match_manifest", "catalog_install_identical_retry", "explicit_reveal_and_hide", "browser_deployment", "healthy_resource_result", "current_viewer_denied_replay", "browser_route_selection"]
        for kind in ["valid", "missing", "false", "numeric", "intercepted", "skipped", "missing_runtime_pass", "wrong_widths", "malformed"]:
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                marker = {"status": "passed", "intercepted_requests": False, "responsive_widths": [1280, 320, 390, 430], **dict.fromkeys(required, True)}
                if kind == "missing": del marker[required[0]]
                if kind == "false": marker[required[0]] = False
                if kind == "numeric": marker[required[0]] = 1
                if kind == "intercepted": marker["intercepted_requests"] = True
                if kind == "wrong_widths": marker["responsive_widths"] = [1280]
                browser = root / "browser.json"
                browser.write_text("invalid JSON" if kind == "malformed" else json.dumps(marker))
                runtime = root / "runtime.log"
                runtime.write_text("PASS" if kind == "missing_runtime_pass" else "--- PASS: TestHostingCatalogLinkdingCompiledRunnerHTTPS (1s)\n" + ("SKIP" if kind == "skipped" else ""))
                result = subprocess.run([sys.executable, "-c", validator, str(runtime), str(browser), "linkding"], capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode == 0, kind == "valid", result.stdout + result.stderr)


class PrivateRegistryModeContractTests(unittest.TestCase):
    def test_private_registry_requires_complete_connected_runtime_receipt(self):
        source = Path(__file__).with_name("gate.sh").read_text()
        validator = source.split("<<'CATALOG_BROWSER_PASS'\n", 1)[1].split("\nCATALOG_BROWSER_PASS", 1)[0]
        required = ["served_assets_match_manifest", "browser_deployment", "registry_credential_selected", "registry_snapshot_pinned", "browser_stateless_registry_deployment", "browser_storage_registry_deployment", "browser_route_selection"]
        for kind in ["valid", *required, "false", "numeric", "widths", "intercepted", "skip", "runtime"]:
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                marker = {"status": "passed", "intercepted_requests": False, "responsive_widths": [1280,320,390,430], **dict.fromkeys(required, True)}
                if kind in required: del marker[kind]
                if kind == "false": marker[required[2]] = False
                if kind == "numeric": marker[required[2]] = 1
                if kind == "widths": marker["responsive_widths"] = [1280]
                if kind == "intercepted": marker["intercepted_requests"] = True
                browser = root / "browser.json"; browser.write_text(json.dumps(marker))
                runtime = root / "runtime.log"; runtime.write_text("PASS" if kind == "runtime" else "--- PASS: TestHostingPrivateRegistryCompiledRunnerHTTPS (1s)\n" + ("SKIP" if kind == "skip" else ""))
                result = subprocess.run([sys.executable,"-c",validator,str(runtime),str(browser),"private-registry"],capture_output=True,text=True,timeout=5)
                self.assertEqual(result.returncode == 0, kind == "valid", result.stdout + result.stderr)

    def test_compose_requires_catalog_install_and_private_replacement(self):
        import ast
        import re
        source = Path(__file__).with_name("gate.sh").read_text()
        validator = source.split("<<'COMPOSE_BROWSER_PASS'\n", 1)[1].split("\nCOMPOSE_BROWSER_PASS", 1)[0]
        required = [key for group in re.findall(r"for key in (\[.*?\]):", validator) for key in ast.literal_eval(group)]
        for kind in ["valid", "browser_compose_archive_download", "browser_catalog_compose_install_identical_retry", "catalog_compose_original_source", "catalog_compose_generated_runtime_refs", "catalog_compose_readiness_prefill", "browser_initial_public_compose", "browser_compose_registry_selection", "false", "numeric", "summary", "archive_summary", "runtime", "skip"]:
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                marker = {"status":"passed", "intercepted_requests":False, **dict.fromkeys(required,True)}
                if kind in required: del marker[kind]
                if kind == "false": marker["browser_compose_registry_selection"] = False
                if kind == "numeric": marker["browser_compose_registry_selection"] = 1
                browser = root / "browser.json"; browser.write_text(json.dumps(marker))
                text = "--- PASS: TestHostingComposeVikunjaCompiledRunnerHTTPS (1s)\ncatalog Compose: original public source and generated runtime refs deployed; private Compose registry: protected replacement and uncached Start\nCompose archive native download: exact selected blob; no new archive or writer lifecycle events"
                if kind == "summary": text = text.split("\n")[0]
                if kind == "archive_summary": text = "\n".join(text.split("\n")[:-1])
                if kind == "runtime": text = "catalog Compose: original public source and generated runtime refs deployed; private Compose registry: protected replacement and uncached Start\nCompose archive native download: exact selected blob; no new archive or writer lifecycle events"
                if kind == "skip": text += "\nSKIP"
                runtime = root / "runtime.log"; runtime.write_text(text)
                result = subprocess.run([sys.executable,"-c",validator,str(runtime),str(browser)],capture_output=True,text=True,timeout=5)
                self.assertEqual(result.returncode == 0, kind == "valid", result.stdout + result.stderr)



class GitComposeContractTests(unittest.TestCase):
    def test_git_capacity_covers_engine_admission_without_changing_other_profiles(self):
        source = GATE.read_text()
        block = "minimum_kib=2097152\n" + source.split("minimum_kib=2097152\n", 1)[1].split("# GIT_INODE_PREREQUISITE_BEGIN", 1)[0]
        # Engine admission reserves UsageBytes + UnpackedBytes + ReserveBytes.
        required = ((3 << 30) + (1 << 30) + (256 << 20)) // 1024
        for git, minimum in [(False, 2097152), (True, required)]:
            for available in [minimum - 1, minimum]:
                with self.subTest(git=git, available=available), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    (root / "guest-filesystem-before-test").write_text("Filesystem 1024-blocks Used Available Capacity Mounted\nfixture 9999999 0 " + str(available) + " 0% /\n")
                    result = subprocess.run(["bash", "-c", "set -e; " + block], env={**os.environ, "evidence": str(root), "git_compose": str(git).lower()}, capture_output=True, text=True, timeout=5)
                    self.assertEqual(result.returncode == 0, available == minimum, result.stderr)

    def test_git_requests_are_checked_before_build_and_guest_setup(self):
        source = GATE.read_text()
        command = "go test -count=1 ./internal/api -run '^TestGitCompose(FixtureSourceAndRequestContracts|BuildRegistryCredentialRotation|BuildSecretDependency)$'"
        self.assertIn('if [[ "$git_compose" == true ]]; then\n ' + command, source)
        position = source.index(command)
        for later in ['bun run --cwd web/app build', 'GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test', 'curl --fail', 'returned_outer=$(outer_run']:
            self.assertLess(position, source.index(later))
        self.assertIn('> "$evidence/git-compose-request-contracts.log" 2>&1', source)

    def test_git_compose_requires_actual_build_failure_and_browser_proofs(self):
        import ast
        import re
        source = GATE.read_text()
        validator = source.split("<<'COMPOSE_BROWSER_PASS'\n", 1)[1].split("\nCOMPOSE_BROWSER_PASS", 1)[0]
        branch = validator.split('raise SystemExit(0)', 1)[0]
        required = ast.literal_eval(re.search(r"for key in (\[.*?\]):", branch).group(1))
        self.assertIn("git_failures_preserved_records_uploads", required)
        self.assertIn("browser_git_compose_route_continuity", required)
        self.assertIn("browser_git_compose_route_recovery", required)
        self.assertIn("browser_git_compose_registry_selection", required)
        self.assertIn("browser_git_compose_build_variables", required)
        self.assertIn("browser_git_compose_build_registry_selection", required)
        self.assertIn("browser_git_compose_build_secret_selection", required)
        for missing in [None, *required, "skip", "runtime"]:
            with self.subTest(missing=missing), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                marker = {"status": "passed", "intercepted_requests": False, **dict.fromkeys(required, True)}
                if missing in required: del marker[missing]
                (root / "browser").write_text(json.dumps(marker))
                runtime = "Git Compose: private HTTPS source and exact receipt built;\n--- PASS: TestHostingGitComposeVikunjaCompiledRunnerHTTPS (1s)\n"
                if missing == "skip": runtime += "SKIP"
                if missing == "runtime": runtime = "PASS"
                (root / "runtime").write_text(runtime)
                result = subprocess.run([sys.executable, "-c", validator, str(root / "runtime"), str(root / "browser"), "true"], capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode == 0, missing is None, result.stderr)

    def test_failed_image_user_inventory_preserves_new_reference(self):
        source = GATE.read_text().split('if [[ "$git_image_added" == true ]]; then', 1)[1].split('\n fi\n host_docker ps', 1)[0]
        for inventory_status in [0, 23]:
            with self.subTest(status=inventory_status), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                prelude = 'result=0; git_image=fixture; git_image_id=sha256:abc\nhost_docker() { case "$1" in image) if [[ "$2" == inspect ]]; then echo sha256:abc; else touch "' + str(root / 'removed') + '"; fi;; ps) return ' + str(inventory_status) + ';; esac; }\n'
                run = subprocess.run(['bash', '-ec', prelude + source + '\nexit "$result"'], capture_output=True, text=True, timeout=5)
                self.assertEqual((root / 'removed').exists(), inventory_status == 0, run.stderr)
                self.assertEqual(run.returncode, 0 if inventory_status == 0 else 1, run.stderr)

    def test_exact_outer_validator_rejects_namespace_drift_for_each_profile(self):
        source = GATE.read_text().split('verify_outer() {', 1)[1]
        validator = source.split("python3 -c '\n", 1)[1].split("\n' \"$evidence/", 1)[0]
        namespace = dict(identity=OUTER, name=OWNER + "-dind", image="sha256:" + "d" * 64, reference="docker:fixture", label="io.nerocd.storage-runtime.owner", owner=OWNER, volume=VOLUME, scenario="valid")
        body = FAKE_DOCKER.split('def outer_body():\n', 1)[1].split("\nif a[0]", 1)[0]
        exec('def outer_body():\n' + body, namespace)
        for git_profile in [False, True]:
            for pid, cgroup in [("", "private"), ("host", "private"), ("host", "host")]:
                with self.subTest(git=git_profile, pid=pid, cgroup=cgroup), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    container = namespace["outer_body"]()
                    owner = "nerocd-git-compose-vikunja-runtime-20261027" if git_profile else OWNER
                    container["Name"] = "/" + owner + "-dind"
                    container["Config"]["Labels"] = {namespace["label"]: owner}
                    container["HostConfig"].update(PidMode=pid, CgroupnsMode=cgroup)
                    image = {"Id": namespace["image"], "Os": "linux", "Architecture": "arm64", "Config": {"Env": ["DOCKER_TLS_CERTDIR=/certs"], "Entrypoint": ["dockerd-entrypoint.sh"], "WorkingDir": "/", "User": "", "Labels": {}}}
                    for name, value in [("outer", [container]), ("images", [image] * 3), ("volume", [{"Mountpoint": "/var/lib/docker/volumes/" + VOLUME + "/_data"}])]:
                        (root / name).write_text(json.dumps(value))
                    args = [str(root / name) for name in ["outer", "images", "volume"]] + [OUTER, owner + "-dind", namespace["reference"], namespace["label"], owner, VOLUME, "false", str(git_profile).lower()]
                    result = subprocess.run([sys.executable, "-c", validator, *args], capture_output=True, text=True, timeout=5)
                    expected = (pid, cgroup) == (("host", "host") if git_profile else ("", "private"))
                    self.assertEqual(result.returncode == 0, expected, result.stderr)

    def test_host_pid_cleanup_signals_and_joins_only_same_mount_namespace(self):
        source = GATE.read_text()
        cleanup = source.split('if [[ "$running" == true ]] && ! host_docker exec "$outer" sh -ec \'', 1)[1].split("'; then", 1)[0]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            proc = root / "proc"
            for identity, namespace in [("self", "owned"), ("123", "owned"), ("456", "foreign")]:
                (proc / identity / "ns").mkdir(parents=True)
                (proc / identity / "ns/mnt").symlink_to("mnt:[" + namespace + "]")
                if identity != "self": (proc / identity / "exe").symlink_to("/fixture/api.test")
            # The fake signal removes only the selected process, so the real
            # launcher join loop must disregard the live foreign executable.
            script = 'kill() { printf "%s\\n" "$2" >> "' + str(root / "signals") + '"; rm "' + str(proc) + '/$2/exe"; }\n'
            result = subprocess.run(["sh", "-ec", script + cleanup.replace("/proc/", str(proc) + "/")], capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual((root / "signals").read_text().splitlines(), ["123"])
            self.assertTrue((proc / "456/exe").is_symlink())
            self.assertFalse((proc / "123/exe").is_symlink())

    def test_git_namespaces_are_explicit_and_other_profiles_remain_private(self):
        source = GATE.read_text()
        self.assertIn('if [[ "$git_compose" == true ]]; then set -- --pid=host --cgroupns=host "$@"; fi', source)
        self.assertIn('expected.update(PidMode="host",CgroupnsMode="host")', source)
        self.assertIn('"PidMode":""', source)
        self.assertIn('"CgroupnsMode":"private"', source)
        prerequisite = source.split('# GIT_CGROUP_PREREQUISITE_BEGIN', 1)[1].split('# GIT_CGROUP_PREREQUISITE_END', 1)[0]
        for contract in ['cgroup.type', 'cgroup.procs', 'cgroup.subtree_control', 'cpu.max', 'memory.max', 'memory.swap.max', 'pids.max', '/proc/$pid/cgroup', 'wait "$pid"', 'rmdir "$child"']:
            self.assertIn(contract, prerequisite)


class GitInodePrerequisiteTests(unittest.TestCase):
    @staticmethod
    def block():
        return GATE.read_text().split('# GIT_INODE_PREREQUISITE_BEGIN', 1)[1].split('# GIT_INODE_PREREQUISITE_END', 1)[0]

    def test_counters_distinguish_unknown_exhausted_and_malformed(self):
        validator = self.block().split("<<'INODE_COUNTERS'\n", 1)[1].split('\nINODE_COUNTERS', 1)[0]
        cases = [
            ('0 0 0', 'allocation-required'),
            ('100000 34464 65536', 'reported-reserve'),
            ('100000 34465 65535', None),
            ('100000 100000 0', None),
            ('0 1 0', None), ('0 0 1', None),
            ('100000 99999 65536', None),
            ('unknown 0 0', None), ('-1 0 0', None),
            ('1 2', None),
        ]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'df'
            for counters, expected in cases:
                with self.subTest(counters=counters):
                    # The mount label is intentionally unrelated to the requested path.
                    path.write_text('Filesystem Inodes Used Available Capacity Mounted on\n/dev/vdb1 ' + counters + ' 0% /etc/resolv.conf\n')
                    result = subprocess.run([sys.executable, '-c', validator, str(path), '/var/lib/docker'], capture_output=True, text=True, timeout=5)
                    self.assertEqual(result.returncode == 0, expected is not None, result.stderr)
                    if expected: self.assertEqual(result.stdout.strip(), expected)
            for body in ['', 'Filesystem Inodes Used Available Capacity Mounted on\n', 'header\na 0 0 0 0% /\nb 0 0 0 0% /\n']:
                path.write_text(body)
                result = subprocess.run([sys.executable, '-c', validator, str(path), '/var/lib/docker'], capture_output=True, text=True, timeout=5)
                self.assertNotEqual(result.returncode, 0)
            path.write_text('header\na 0 0 0 0% /\n')
            result = subprocess.run([sys.executable, '-c', validator, str(path), '/foreign'], capture_output=True, text=True, timeout=5)
            self.assertNotEqual(result.returncode, 0)

    def test_unknown_requires_bounded_allocation_on_explicit_path(self):
        for counters, allocation_status in [('0 0 0', 0), ('0 0 0', 1), ('0 0 0', 124), ('100000 34464 65536', 0), ('100000 100000 0', 0)]:
            with self.subTest(counters=counters, status=allocation_status), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                prelude = r"""
set -euo pipefail
outer=owned; git_compose=false
host_docker() {
 printf '%s\n' "$@" >> "$evidence/calls"
 if [[ "$5" == df ]]; then
  [[ "$*" == 'exec owned timeout 5 df -Pi /var/lib/docker' ]] || return 90
  printf 'Filesystem Inodes Used Available Capacity Mounted on\n/dev/vdb1 %s 0%% /etc/resolv.conf\n' "$counters"
 else
  [[ "$1 $2 $3 $4 $5 $6 $7 $8" == 'exec owned timeout -k 5 30 sh -ec' ]] || return 91
  [[ "${10} ${11}" == 'sh /var/lib/docker' ]] || return 92
  touch "$evidence/allocated"
  return "$allocation_status"
 fi
}
"""
                run = subprocess.run(['bash', '-c', prelude + self.block() + '\ngit_inode_capacity /var/lib/docker docker\n'], env={**os.environ, 'evidence': str(root), 'counters': counters, 'allocation_status': str(allocation_status)}, capture_output=True, text=True, timeout=5)
                expected = allocation_status == 0 and counters != '100000 100000 0'
                self.assertEqual(run.returncode == 0, expected, run.stderr)
                self.assertEqual((root / 'allocated').exists(), counters == '0 0 0')

    def test_actual_allocation_cleanup_and_failure_preserves_foreign_files(self):
        script = self.block().split("timeout -k 5 30 sh -ec '\n", 1)[1].split("\n' sh \"$path\"", 1)[0]
        for fail in [False, True]:
            with self.subTest(fail=fail), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                foreign = root / 'foreign'; foreign.mkdir(); (foreign / 'keep').touch()
                prelude = 'mktemp() { command mktemp -d "$TEST_ROOT/probe.XXXXXX"; }\n'
                prelude += "stat() { python3 -c 'import os,sys; s=os.stat(sys.argv[1], follow_symlinks=False); print(str(s.st_dev)+\":\"+str(s.st_ino))' \"$3\"; }\n"
                if fail: prelude += 'mkdir() { return 1; }\n'
                result = subprocess.run(['sh', '-ec', prelude + script, 'sh', '/var/lib/docker'], env={**os.environ, 'TEST_ROOT': str(root)}, capture_output=True, text=True, timeout=30)
                self.assertEqual(result.returncode == 0, not fail, result.stderr)
                self.assertIn('created=', result.stdout)
                self.assertIn('removed=', result.stdout)
                self.assertEqual(list(root.iterdir()), [foreign])
                self.assertTrue((foreign / 'keep').exists())
                if not fail: self.assertIn('allocated=65536 path=/var/lib/docker', result.stdout)

    def test_explicit_paths_keep_allocation_inside_owned_guest(self):
        block = self.block()
        for path, key in [('/', 'root'), ('/var/lib/docker', 'docker'), ('/tmp', 'tmp')]:
            self.assertIn('git_inode_capacity ' + path + ' ' + key, block)
        self.assertIn('host_docker exec "$outer" timeout 5 df -Pi "$path"', block)
        self.assertIn('identity=$(stat -c "%d:%i" "$probe")', block)
        self.assertIn('test "$(stat -c "%d:%i" "$probe")" = "$identity"', block)
        self.assertIn('test ! -e "$probe" && test ! -L "$probe"', block)

if __name__ == "__main__":
    unittest.main()
