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
                deadline = time.monotonic() + 5
                while not (root / "started").exists():
                    if process.poll() is not None or time.monotonic() >= deadline:
                        self.fail("gate did not reach fake compiler handshake")
                    time.sleep(0.01)
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


class BrowserCancellationTest(unittest.TestCase):
    """Run the real driver with inert Playwright resources, without a browser."""
    def run_driver(self, delivered_signal=None, cleanup_failure="", complete=False):
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
const phases = ["target", "validation", "backup", "denied", "retry", "restore", "archive_alert", "complete"];
let rejectPending, pendingHandle;
const api = {
 get: async path => {
  fs.writeFileSync(root + "/started", "polling");
  if (complete) return path.includes("/__storage_remote_fixture/")
   ? {status: () => 200, json: async () => {const phase=phases.shift();const archive=phase==="archive_alert";return {phase,resource_id:"resource",environment_id:"environment",target_id:"target",backup_id:archive?"failed_backup":"backup",source_id:archive?"failed_archive":"failed_upload",recovery_url:process.env.NEROCD_STORAGE_REMOTE_BROWSER_ORIGIN+"/projects?project_id=proj_platform&environment_id=environment&resource_id=resource&backup_id="+(archive?"failed_backup&source_kind=storage_backup":"backup&source_kind=storage_transfer&transfer_id=failed_upload"),access_key:"fixture",secret_key:"fixture"}}}
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
 if (name === "goto") return async value=>{currentURL=value};
 if (name === "url") return ()=>currentURL;
 if (["getByRole","getByLabel","locator"].includes(name)) return () => page;
 if (name === "evaluateAll") return async () => "target:1";
 if (name === "textContent") return async () => "View scheduled archive backup";
 if (name === "evaluate") return async () => false;
 if (name === "waitForResponse") return async () => {
  const index=responseIndex++;
  const values=[{id:"target"},{id:"validation"},{},{id:"schedule",revision:1,remote_target:{target_id:"target"}},{id:"schedule",revision:2},{id:"schedule",revision:3,enabled:false},{id:"upload"},{operation:{id:"restore"}}];
  const statuses=[200,202,503,200,200,200,202,202];
  return {status: () => statuses[index],json: async () => values[index],
   request: () => ({postDataJSON: () => ({remote_target:{target_id:"target"},remote_source:{transfer_id:"upload"}})})};
 };
 return async () => {};
}});
const makeContext = () => {const name=contextIndex++ === 0 ? "context" : "viewer";return {newPage: async () => page,close: async () => {
 await new Promise(resolve => setTimeout(resolve, 10));
 if (browserClosed) throw Error("parent closed before child settled");
 await close(name);
}};};
module.exports = {
 request: {newContext: async () => api},
 chromium: {launch: async () => ({newContext: async () => makeContext(), close: async () => {browserClosed = true; await close("browser");}})},
 expect: () => new Proxy({}, {get: () => async () => {if (!complete) throw Error("UI must not execute in cancellation test");}})
};
""")
            environment = os.environ.copy()
            environment.update(NEROCD_STORAGE_REMOTE_BROWSER_ORIGIN="https://127.0.0.1:49155",
                               NEROCD_STORAGE_REMOTE_BROWSER_KEY="a" * 64,
                               NEROCD_STORAGE_REMOTE_HOST_EVIDENCE=str(root),
                               STORAGE_BROWSER_TEST_EXPIRE="0" if delivered_signal or complete else "1",
                               STORAGE_BROWSER_TEST_CLOSE_FAILURE=cleanup_failure,
                               STORAGE_BROWSER_TEST_COMPLETE="1" if complete else "0")
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
                expected_code = 128 + delivered_signal if delivered_signal else (1 if cleanup_failure or not complete else 0)
                self.assertEqual(process.returncode, expected_code, output)
                expected_closed = {"api", "context", "browser", "viewer"} if complete else {"api", "context", "browser"}
                self.assertEqual(set((root / "closed").read_text().splitlines()), expected_closed)
                self.assertFalse((root / "receipt-before-cleanup").exists(), "receipt was published before cleanup")
                safe = json.loads((root / "browser-safe.json").read_text())
                self.assertEqual(safe["status"], "passed" if expected_code == 0 else "failed")
                if expected_code:
                    expected_failure = "browser_workflow_cancelled" if delivered_signal else ("browser_cleanup_failed" if complete else "browser_workflow_failed")
                    self.assertEqual(safe["failure"], expected_failure)
                if cleanup_failure:
                    self.assertEqual(safe["cleanup_failures"], [cleanup_failure])
                    self.assertNotIn("private close failure", json.dumps(safe))
            finally:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGKILL)
                process.communicate(timeout=3)

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

if __name__ == "__main__":
    unittest.main()
