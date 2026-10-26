"""Exercise the actual gate's signal traps using harmless executable stubs."""

import json
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
    def run_preload(self, change=None, pull_exit=0):
        source = GATE.read_text().split("# IMAGE_PRELOAD_BEGIN\n", 1)[1].split("# IMAGE_PRELOAD_END", 1)[0]
        digest = "sha256:" + "a" * 64
        reference = "postgres:17.6-alpine@" + digest
        host = {"Id": "sha256:" + "b" * 64, "Os": "linux", "Architecture": "arm64",
                "RepoDigests": ["postgres@" + digest], "Config": {"Entrypoint": ["entrypoint"], "User": ""}}
        guest = json.loads(json.dumps(host))
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
            return result, calls

    def test_exact_pin_preloaded_before_inspection(self):
        result, calls = self.run_preload()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(calls, ["pull --platform linux/arm64 postgres:17.6-alpine@sha256:" + "a" * 64,
                                 "image inspect postgres:17.6-alpine@sha256:" + "a" * 64])

    def test_bad_digest_platform_content_or_config_blocks(self):
        changes = {
            "lost digest lookup": lambda value: value.update(RepoDigests=[]),
            "wrong digest": lambda value: value.update(RepoDigests=["postgres@sha256:" + "c" * 64]),
            "wrong repository": lambda value: value.update(RepoDigests=["foreign/image@sha256:" + "a" * 64]),
            "wrong platform": lambda value: value.update(Architecture="amd64"),
            "wrong content": lambda value: value.update(Id="sha256:" + "c" * 64),
            "wrong config": lambda value: value.update(Config={"User": "unexpected"}),
        }
        for name, change in changes.items():
            with self.subTest(name=name):
                result, calls = self.run_preload(change)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(len(calls), 2)

    def test_pull_failure_never_inspects_or_starts(self):
        result, calls = self.run_preload(pull_exit=19)
        self.assertEqual(result.returncode, 19)
        self.assertEqual(len(calls), 1)


class BrowserCancellationTest(unittest.TestCase):
    """Run the real driver with inert Playwright resources, without a browser."""
    def run_driver(self, delivered_signal=None):
        driver = GATE.with_name("remote_browser.mjs")
        with tempfile.TemporaryDirectory(prefix="nerocd-storage-browser-stop-test-") as temporary:
            root = Path(temporary)
            module = root / "web/app/node_modules/@playwright/test"
            module.mkdir(parents=True)
            (root / "web/app/package.json").write_text("{}")
            (module / "index.js").write_text(r"""
const fs = require("node:fs");
const root = process.env.NEROCD_STORAGE_REMOTE_HOST_EVIDENCE;
const mark = value => fs.appendFileSync(root + "/closed", value + "\n");
let rejectPending, pendingHandle;
const api = {
 get: async () => {
  fs.writeFileSync(root + "/started", "polling");
  if (process.env.STORAGE_BROWSER_TEST_EXPIRE === "1") {
   const expired = Date.now() + 25 * 60 * 1000;
   Date.now = () => expired;
   return {status: () => 403};
  }
  return new Promise((resolve, reject) => {rejectPending = reject; pendingHandle = setInterval(() => {}, 1000);});
 },
 dispose: async () => {mark("api"); clearInterval(pendingHandle); rejectPending?.(Error("owned API disposed"));}
};
const context = {newPage: async () => ({setDefaultTimeout: () => {}}), close: async () => mark("context")};
module.exports = {
 request: {newContext: async () => api},
 chromium: {launch: async () => ({newContext: async () => context, close: async () => mark("browser")})},
 expect: () => {throw Error("UI must not execute in cancellation test");}
};
""")
            environment = os.environ.copy()
            environment.update(NEROCD_STORAGE_REMOTE_BROWSER_ORIGIN="https://127.0.0.1:49155",
                               NEROCD_STORAGE_REMOTE_BROWSER_KEY="a" * 64,
                               NEROCD_STORAGE_REMOTE_HOST_EVIDENCE=str(root),
                               STORAGE_BROWSER_TEST_EXPIRE="0" if delivered_signal else "1")
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
                self.assertEqual(process.returncode, 128 + delivered_signal if delivered_signal else 1, output)
                self.assertEqual(set((root / "closed").read_text().splitlines()), {"api", "context", "browser"})
                safe = json.loads((root / "browser-safe.json").read_text())
                self.assertEqual(safe["status"], "failed")
                self.assertEqual(safe["failure"], "browser_workflow_cancelled" if delivered_signal else "browser_workflow_failed")
            finally:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.communicate(timeout=3)

    def test_sigterm_stops_pending_poll_and_joins_owned_resources(self):
        self.run_driver(signal.SIGTERM)

    def test_sigint_stops_pending_poll_and_joins_owned_resources(self):
        self.run_driver(signal.SIGINT)

    def test_phase_deadline_closes_owned_resources(self):
        self.run_driver()


if __name__ == "__main__":
    unittest.main()
