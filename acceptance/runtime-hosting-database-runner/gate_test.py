"""Exercise the real fixed-target launcher with harmless stateful CLI stubs."""

import ast
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest

OWNER = "nerocd-database-runner-20261024-dbr7319"
NAME, VOLUME = OWNER + "-dind", OWNER + "-docker-data"
LABEL = "io.nerocd.database-runner.owner"
IDENTITY = "f" * 64

STUB = r'''
import json, os, pathlib, signal, subprocess, sys, time
root = pathlib.Path(os.environ["DATABASE_GATE_TEST_ROOT"])
case = os.environ["DATABASE_GATE_TEST_CASE"]
tool, args = pathlib.Path(sys.argv[0]).name, sys.argv[1:]
owner = "nerocd-database-runner-20261024-dbr7319"
name, volume = owner + "-dind", owner + "-docker-data"
label = "io.nerocd.database-runner.owner"
identity = "f" * 64
state_path = root / "state"
state = json.loads(state_path.read_text())
def save():
    state_path.write_text(json.dumps(state))
def block():
    child = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(120)"])
    def stop(number, _frame):
        child.wait(timeout=2)
        sys.exit(128 + number)
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    (root / "blocked").write_text(json.dumps([os.getpid(), child.pid]))
    while True:
        time.sleep(1)
with (root / "commands").open("a") as log:
    log.write(json.dumps([tool, *args]) + "\n")
if tool == "git":
    if args[:1] == ["rev-parse"]:
        print("1" * 40 if args[1] == "HEAD" else "2" * 40)
elif tool == "go":
    if args[:2] == ["test", "-c"] or args[:1] == ["build"]:
        if case in ("sigint", "sigterm"):
            block()
        if case == "build_failure":
            sys.exit(17)
        pathlib.Path(args[args.index("-o") + 1]).write_bytes(b"harmless compiled fixture placeholder")
    elif args[:2] == ["version", "-m"]:
        print("GOOS=linux GOARCH=arm64")
    else:
        raise RuntimeError("unexpected Go command")
elif tool == "docker":
    assert args[:2] == ["--host", "unix:///var/run/docker.sock"]
    args = args[2:]
    if args[:1] == ["info"]:
        print(json.dumps(dict(ID="foreign" if case == "wrong_daemon" else "e37dd219-b13f-47dd-99ed-21d79442ed0c", OSType="linux", Architecture="aarch64")))
    elif args[:2] == ["volume", "ls"]:
        if case == "inventory_error":
            sys.exit(19)
        if state.get("volume"):
            print(json.dumps(dict(Name=volume)))
    elif args[:2] == ["container", "ls"]:
        if state.get("outer"):
            print(json.dumps(dict(Names=name, ID=identity)))
        if state.get("foreign_user") and "--filter" in args:
            print(json.dumps(dict(Names="foreign-stopped", ID="e" * 64)))
    elif args[:2] == ["image", "inspect"]:
        print(json.dumps([dict(Id="sha256:" + "a" * 64, Config=dict(Labels={}))]))
    elif args[:2] == ["volume", "inspect"]:
        assert args[2] == volume and state.get("volume")
        print(json.dumps([state["volume"]]))
    elif args[:2] == ["volume", "create"]:
        assert args[-1] == volume
        state["volume"] = dict(Name=volume, Driver="local", Scope="local", Labels={label: owner},
                               Options=None, CreatedAt="2026-10-24T00:00:00Z")
        if case == "foreign_race":
            state["volume"]["Labels"] = {label: "somebody-else"}
        if case == "volume_options":
            state["volume"]["Options"] = {"device": "/foreign"}
        if case == "foreign_user":
            state["foreign_user"] = True
        save()
        if case == "volume_response_loss":
            sys.exit(23)
        print(volume)
    elif args[:2] == ["container", "create"]:
        assert "--privileged" in args and args[args.index("--name") + 1] == name
        assert args[args.index("--mount") + 1] == "type=volume,src=" + volume + ",dst=/var/lib/docker"
        if case == "outer_response_loss_absent":
            sys.exit(24)
        state["outer"] = dict(Id=identity, Name="/" + name, Image="sha256:" + "a" * 64,
            Config=dict(Labels={label: owner}, Cmd=["dockerd", "--host=unix:///var/run/docker.sock"]),
            HostConfig=dict(Privileged=True, PidMode="", UsernsMode="", Binds=None,
                            PortBindings={}, PublishAllPorts=False, NetworkMode="default"),
            Mounts=[dict(Type="volume", Name=volume, Driver="local", Destination="/var/lib/docker", RW=True)],
            State=dict(Running=False))
        if case == "outer_mismatch":
            state["outer"]["Config"]["Labels"] = {label: "somebody-else"}
        save()
        if case == "outer_signal":
            block()
        if case in ("outer_response_loss", "outer_mismatch"):
            sys.exit(24)
        print("malformed" if case == "outer_malformed_response" else identity)
    elif args[:2] == ["container", "inspect"]:
        assert args[2] == identity and state.get("outer")
        print(json.dumps([state["outer"]]))
    elif args[:2] == ["container", "start"]:
        assert args[2] == identity
        state["outer"]["State"]["Running"] = True
        save()
        if case == "start_response_loss":
            sys.exit(25)
        print(identity)
    elif args[:2] == ["container", "stop"]:
        assert args[-1] == identity
        state["outer"]["State"]["Running"] = False
        save()
        print(identity)
    elif args[:2] == ["container", "rm"]:
        assert args == ["container", "rm", "--force", identity]
        state.pop("outer")
        save()
        print(identity)
    elif args[:2] == ["volume", "rm"]:
        assert args == ["volume", "rm", volume] and not state.get("outer")
        state.pop("volume")
        save()
        print(volume)
    elif args[:1] == ["exec"]:
        if args[1:4] == [identity, "docker", "--host"] and "info" in args:
            print(json.dumps(dict(ID="guest-daemon", OSType="linux", Architecture="aarch64")))
        elif args[1:4] == [identity, "sh", "-ec"]:
            if case == "guest_join_failure":
                sys.exit(26)
        elif "/fixture/api.test" in args:
            if case == "runtime_sigterm":
                block()
            if case == "runtime_failure":
                sys.exit(31)
            print("harmless runtime model passed")
            print("--- PASS: TestHostingDatabaseCompiledRunner (0.01s)")
            if case == "capture_skipped":
                print("--- SKIP: TestHostingDatabaseCaptureCompiledRunner (0.00s)")
            elif case != "capture_missing":
                print("--- PASS: TestHostingDatabaseCaptureCompiledRunner (0.01s)")
        elif args[1:3] == [identity, "docker"]:
            if "--detach" in args:
                print("e" * 64)
            elif "--command=SELECT current_database()" in args:
                print("nerocd_database_runner")
        elif args[1:3] == [identity, "mkdir"]:
            pass
        else:
            raise RuntimeError("unexpected exec")
    elif args[:1] == ["cp"]:
        pass
    else:
        raise RuntimeError("unexpected Docker command: " + repr(args))
else:
    raise RuntimeError("unexpected executable")
'''


class GateTest(unittest.TestCase):
    def run_gate(self, case, expected, delivered_signal=None):
        gate = Path(os.environ.get("DATABASE_GATE_UNDER_TEST", Path(__file__).with_name("gate.sh"))).resolve()
        with tempfile.TemporaryDirectory(prefix="nerocd-database-gate-test-") as temporary:
            root = Path(temporary)
            fixture = root / "internal/api/hosting_database_runtime_integration_test.go"
            fixture.parent.mkdir(parents=True)
            fixture.touch()
            client = root / "acceptance/runtime-hosting-database-client"
            client.mkdir(parents=True)
            (client / "Dockerfile").write_text("FROM scratch\nCOPY client /client\n")
            initial = {}
            if case == "preexisting_foreign":
                initial["volume"] = {"Name": VOLUME, "Labels": {LABEL: "foreign"}}
            if case == "preexisting_outer":
                initial["outer"] = {"Id": IDENTITY, "Name": "/" + NAME}
            (root / "state").write_text(json.dumps(initial))
            binary = root / "bin"
            binary.mkdir()
            for name in ("git", "go", "docker"):
                executable = binary / name
                executable.write_text("#!" + sys.executable + "\n" + STUB)
                executable.chmod(0o700)
            environment = dict(os.environ, PATH=str(binary) + os.pathsep + os.environ["PATH"], TMPDIR=str(root),
                               DATABASE_GATE_TEST_ROOT=str(root), DATABASE_GATE_TEST_CASE=case)
            process = subprocess.Popen(["bash", str(gate)], cwd=root, env=environment,
                                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                       text=True, start_new_session=True)
            evidence = None
            blocked = []
            try:
                if delivered_signal:
                    deadline = time.monotonic() + 8
                    while not (root / "blocked").exists():
                        if process.poll() is not None or time.monotonic() >= deadline:
                            self.fail("gate did not reach genuinely blocked child")
                        time.sleep(0.01)
                    blocked = json.loads((root / "blocked").read_text())
                    for pid in blocked:
                        os.kill(pid, 0)
                    process.send_signal(delivered_signal)
                output, _ = process.communicate(timeout=15)
                self.assertEqual(process.returncode, expected, output)
                evidence = Path(next(line.removeprefix("evidence=") for line in output.splitlines()
                                     if line.startswith("evidence=")))
                self.assertIn(f"gate_exit={expected}\n", (evidence / "result").read_text())
                self.assertEqual(output.count("gate exit="), 1, output)
                for pid in blocked:
                    with self.assertRaises(ProcessLookupError, msg=output):
                        os.kill(pid, 0)
                if blocked:
                    self.assertIn("owned process group joined: " + str(blocked[0]), output)
                commands = [json.loads(line)[1:] for line in (root / "commands").read_text().splitlines()]
                docker_commands = [args[2:] for args in commands if args[:2] == ["--host", "unix:///var/run/docker.sock"]]
                state = json.loads((root / "state").read_text())
                return state, docker_commands, output
            finally:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.communicate(timeout=5)
                # These are exact harmless test evidence directories created by this invocation.
                if evidence is not None and not os.environ.get("DATABASE_GATE_KEEP_EVIDENCE"):
                    self.assertTrue(str(evidence).startswith("/tmp/nerocd-database-runner-20261024."))
                    shutil.rmtree(evidence)
                elif evidence is not None:
                    print(json.dumps(dict(case=case, evidence=str(evidence), exit=process.returncode,
                                          joined_pids=blocked)), flush=True)

    def assert_no_creation(self, commands):
        self.assertFalse(any(args[:2] in (["volume", "create"], ["container", "create"]) for args in commands))

    def test_inventory_error_never_means_absence(self):
        state, commands, _ = self.run_gate("inventory_error", 19)
        self.assert_no_creation(commands)
        self.assertEqual(state, {})

    def test_wrong_daemon_has_no_mutations(self):
        state, commands, _ = self.run_gate("wrong_daemon", 1)
        self.assert_no_creation(commands)
        self.assertEqual(state, {})

    def test_preexisting_outer_is_never_owned_or_removed(self):
        state, commands, _ = self.run_gate("preexisting_outer", 1)
        self.assert_no_creation(commands)
        self.assertEqual(state["outer"], {"Id": IDENTITY, "Name": "/" + NAME})
        self.assertFalse(any(args[:2] in (["container", "rm"], ["container", "stop"]) for args in commands))

    def test_preexisting_foreign_volume_is_preserved(self):
        state, commands, _ = self.run_gate("preexisting_foreign", 1)
        self.assert_no_creation(commands)
        self.assertEqual(state["volume"]["Labels"], {LABEL: "foreign"})

    def test_raced_foreign_volume_is_never_mounted_or_deleted(self):
        for case in ("foreign_race", "volume_options", "foreign_user"):
            with self.subTest(case=case):
                state, commands, _ = self.run_gate(case, 1)
                self.assertIn("volume", state)
                self.assertFalse(any(args[:2] in (["container", "create"], ["volume", "rm"]) for args in commands))

    def test_volume_response_loss_reconciles_owned_volume(self):
        state, commands, _ = self.run_gate("volume_response_loss", 23)
        self.assertEqual(state, {})
        self.assertIn(["volume", "rm", VOLUME], commands)

    def test_outer_response_loss_reconciles_without_start(self):
        state, commands, _ = self.run_gate("outer_response_loss", 24)
        self.assertEqual(state, {})
        self.assertIn(["container", "rm", "--force", IDENTITY], commands)
        self.assertFalse(any(args[:2] == ["container", "start"] for args in commands))

    def test_mismatched_ambiguous_outer_is_preserved(self):
        state, commands, _ = self.run_gate("outer_mismatch", 24)
        self.assertIn("outer", state)
        self.assertIn("volume", state)
        self.assertFalse(any(args[:2] in (["container", "rm"], ["volume", "rm"]) for args in commands))

    def test_ambiguous_absent_outer_retains_reserved_storage(self):
        state, commands, output = self.run_gate("outer_response_loss_absent", 24)
        self.assertIn("volume", state)
        self.assertNotIn("outer", state)
        self.assertIn("outer creation outcome unknown", output)
        self.assertFalse(any(args[:2] == ["volume", "rm"] for args in commands))

    def test_malformed_create_response_reconciles_exact_owned_name(self):
        state, commands, _ = self.run_gate("outer_malformed_response", 1)
        self.assertEqual(state, {})
        self.assertIn(["container", "rm", "--force", IDENTITY], commands)
        self.assertFalse(any(args[:2] == ["container", "start"] for args in commands))

    def test_start_response_loss_reconciles_running_outer(self):
        state, commands, output = self.run_gate("start_response_loss", 25)
        self.assertEqual(state, {})
        self.assertTrue(any(args[:4] == ["exec", IDENTITY, "sh", "-ec"] for args in commands))
        self.assertIn("owned outer absent:", output)

    def test_guest_join_failure_stops_and_preserves_outer(self):
        state, commands, _ = self.run_gate("guest_join_failure", 1)
        self.assertFalse(state["outer"]["State"]["Running"])
        self.assertIn("volume", state)
        self.assertIn(["container", "stop", "--time", "5", IDENTITY], commands)
        self.assertFalse(any(args[:2] == ["container", "rm"] for args in commands))

    def test_sigint_joins_blocked_compiler_and_child(self):
        state, commands, _ = self.run_gate("sigint", 130, signal.SIGINT)
        self.assertEqual(state, {})
        self.assert_no_creation(commands)

    def test_sigterm_joins_blocked_compiler_and_child(self):
        state, commands, _ = self.run_gate("sigterm", 143, signal.SIGTERM)
        self.assertEqual(state, {})
        self.assert_no_creation(commands)

    def test_sigterm_joins_ambiguous_create_and_reconciles(self):
        state, commands, _ = self.run_gate("outer_signal", 143, signal.SIGTERM)
        self.assertEqual(state, {})
        self.assertIn(["container", "rm", "--force", IDENTITY], commands)

    def test_build_failure_preserves_status(self):
        state, commands, _ = self.run_gate("build_failure", 17)
        self.assertEqual(state, {})
        self.assert_no_creation(commands)

    def test_command_deadline_joins_blocked_process_group(self):
        gate = Path(__file__).with_name("gate.sh").read_text().split("# PYTHON SOURCE\n", 1)[1].rsplit("\nPY\n", 1)[0]
        names = {"Failure", "Interrupted", "group_exists", "join_owned", "run"}
        definitions = [node for node in ast.parse(gate).body
                       if isinstance(node, (ast.FunctionDef, ast.ClassDef)) and node.name in names]
        with tempfile.TemporaryDirectory(prefix="nerocd-database-gate-deadline-") as temporary:
            root = Path(temporary)
            script = root / "blocked.py"
            script.write_text("import os, pathlib, signal, subprocess, sys, time\n"
                              "child = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(120)'])\n"
                              "def stop(number, frame):\n child.wait(timeout=2)\n sys.exit(128 + number)\n"
                              "signal.signal(signal.SIGTERM, stop)\n"
                              "pathlib.Path(sys.argv[1]).write_text(str(os.getpid()) + ' ' + str(child.pid))\n"
                              "while True: time.sleep(1)\n")
            messages = []
            with (root / "stderr").open("wb") as log:
                namespace = dict(os=os, signal=signal, subprocess=subprocess, time=time, tempfile=tempfile,
                                 log=log, evidence=root, say=messages.append)
                exec(compile(ast.Module(body=definitions, type_ignores=[]), str(script), "exec"), namespace)
                with self.assertRaisesRegex(namespace["Failure"], "command deadline exceeded"):
                    namespace["run"]([sys.executable, str(script), str(root / "pids")], timeout=0.5)
            pids = [int(value) for value in (root / "pids").read_text().split()]
            for pid in pids:
                with self.assertRaises(ProcessLookupError):
                    os.kill(pid, 0)
            self.assertIn("owned process group joined: " + str(pids[0]), messages)

    def test_missing_or_skipped_capture_cannot_qualify_baseline_only(self):
        for case in ("capture_missing", "capture_skipped"):
            with self.subTest(case=case):
                state, commands, output = self.run_gate(case, 1)
                self.assertEqual(state, {})
                self.assertIn("required physical test did not pass: TestHostingDatabaseCaptureCompiledRunner", output)
                self.assertIn(["container", "rm", "--force", IDENTITY], commands)

    def test_runtime_failure_retains_exit_and_cleans_owned_fixture(self):
        state, commands, _ = self.run_gate("runtime_failure", 31)
        self.assertEqual(state, {})
        self.assertIn(["container", "rm", "--force", IDENTITY], commands)

    def test_sigterm_during_runtime_joins_child_and_cleans_owned_fixture(self):
        state, commands, _ = self.run_gate("runtime_sigterm", 143, signal.SIGTERM)
        self.assertEqual(state, {})
        self.assertIn(["container", "rm", "--force", IDENTITY], commands)

    def test_harmless_complete_model(self):
        state, commands, output = self.run_gate("success", 0)
        self.assertEqual(state, {})
        self.assertIn("harmless runtime model passed", output)
        runtime_command = next(args for args in commands if "/fixture/api.test" in args)
        self.assertEqual(runtime_command[runtime_command.index("-test.run") + 1],
                         "^TestHostingDatabase(CompiledRunner|CaptureCompiledRunner)$")
        self.assertIn("-test.timeout=25m", runtime_command)
        self.assertEqual(sum("/fixture/api.test" in args for args in commands), 1)
        create = next(index for index, args in enumerate(commands) if args[:2] == ["container", "create"])
        start = next(index for index, args in enumerate(commands) if args[:2] == ["container", "start"])
        self.assertTrue(any(args[:2] == ["volume", "inspect"] for args in commands[create + 1:start]))
        self.assertTrue(any(args[:2] == ["container", "inspect"] for args in commands[create + 1:start]))


if __name__ == "__main__":
    unittest.main()
