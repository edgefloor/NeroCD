"""Exercise the real fixed-target launcher with harmless stateful CLI stubs."""

import ast
import hashlib
import io
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
from unittest import mock

OWNER = "nerocd-database-runner-20261024-dbr7319"
NAME, VOLUME = OWNER + "-dind", OWNER + "-docker-data"
LABEL = "io.nerocd.database-runner.owner"
IDENTITY = "f" * 64

EXPECTED_PROCPS_PACKAGES = (
    ("libintl", "0.24.1-r1", "291a89bbb0e511e9875ff7aa9d6d07dc9a2fec8bf94c2a686d1a9153b8c58903"),
    ("libncursesw", "6.5_p20251123-r0", "7fe332c8af8b97579e89df56466f83f4abda5040a18e1842c07d684320bbef4b"),
    ("libproc2", "4.0.5-r0", "9f3b7b5f3f27cd15b471ac8e14915d7f686968f53b9249168f0adf318edb1687"),
    ("musl", "1.2.5-r24", "d7043c549029c98d438e9bc94213b006e5c88540eb58d1f5b0f0d1d56dbf9c98"),
    ("ncurses-terminfo-base", "6.5_p20251123-r0", "6952a6b39abaf7bbc498cb085f0f59bf23619b53b3f7328b08fe50c0198a2bd4"),
    ("procps-ng", "4.0.5-r0", "be8701f46de45aa1876f0b50748c54d47d216e8d3f54793c350818c669e64bc8"),
    ("skalibs-libs", "2.14.4.0-r0", "f1c33c73d842c98d99033833eaf681819b0f5b51310a6e286c2eb88b9a88ed97"),
    ("utmps-libs", "0.1.3.1-r0", "dbaa260e4328738d51b2ec77fdda9037cdbd67047465de5b2a30dce9a66f8fff"),
)

def download_source():
    gate = Path(__file__).with_name("gate.sh").read_text().split("# PYTHON SOURCE\n", 1)[1].rsplit("\nPY\n", 1)[0]
    return next(ast.literal_eval(node.value) for node in ast.parse(gate).body
                if isinstance(node, ast.Assign) and any(isinstance(target, ast.Name) and target.id == "PROCPS_DOWNLOAD"
                                                       for target in node.targets))


class DownloadTest(unittest.TestCase):
    def test_bounded_verified_download_and_transport_policy(self):
        namespace = {"__name__": "test_archive_download"}
        exec(download_source(), namespace)
        url = "https://dl-cdn.alpinelinux.org/alpine/v3.23/main/aarch64/procps-ng-4.0.5-r0.apk"
        request = namespace["urllib"].request
        payload = b"harmless signed-archive model"
        for case in ("success", "size", "hash", "status", "redirect", "network"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temporary:
                path = Path(temporary) / "procps-ng-4.0.5-r0.apk"
                response = mock.MagicMock()
                response.status = 201 if case == "status" else 200
                response.geturl.return_value = url + "/redirect" if case == "redirect" else url
                response.read.return_value = b"x" * ((1 << 20) + 1) if case == "size" else payload
                response.__enter__.return_value = response
                opener = mock.Mock()
                opener.open.return_value = response
                if case == "network":
                    opener.open.side_effect = OSError("SENSITIVE-DOWNLOAD-ERROR")
                digest = "0" * 64 if case == "hash" else hashlib.sha256(payload).hexdigest()
                with mock.patch.object(request, "build_opener", return_value=opener) as build:
                    if case == "success":
                        namespace["download_archive"](url, str(path), digest)
                        self.assertEqual(path.read_bytes(), payload)
                        with self.assertRaises(FileExistsError):
                            namespace["download_archive"](url, str(path), digest)
                    else:
                        with self.assertRaises((ValueError, OSError)):
                            namespace["download_archive"](url, str(path), digest)
                        self.assertFalse(path.exists())
                    proxy, tls, redirect = build.call_args.args
                    self.assertEqual(proxy.proxies, {})
                    self.assertTrue(tls._context.check_hostname)
                    self.assertEqual(tls._context.verify_mode, namespace["ssl"].CERT_REQUIRED)
                    with self.assertRaisesRegex(ValueError, "redirect refused"):
                        redirect.redirect_request(None, None, 302, None, None, url)
                    opener.open.assert_called_with(url, timeout=10)
                    if case not in ("status", "redirect", "network"):
                        response.read.assert_called_with((1 << 20) + 1)

    def test_foreign_url_is_refused_before_network(self):
        namespace = {"__name__": "test_archive_download"}
        exec(download_source(), namespace)
        with mock.patch.object(namespace["urllib"].request, "build_opener") as build:
            with self.assertRaisesRegex(ValueError, "archive identity refused"):
                namespace["download_archive"]("https://foreign.invalid/procps-ng.apk", "/tmp/procps-ng.apk", "0" * 64)
            build.assert_not_called()

    def test_cli_errors_do_not_echo_transport_diagnostics(self):
        source = download_source()
        url = "https://dl-cdn.alpinelinux.org/alpine/v3.23/main/aarch64/procps-ng-4.0.5-r0.apk"
        with mock.patch("urllib.request.build_opener", side_effect=OSError("SENSITIVE-DOWNLOAD-ERROR")), \
                mock.patch.object(sys, "argv", ["download", url, "/tmp/procps-ng-4.0.5-r0.apk", "0" * 64]), \
                mock.patch.object(sys, "stderr", new_callable=io.StringIO) as stderr:
            with self.assertRaises(SystemExit) as stopped:
                exec(source, {"__name__": "__main__"})
            self.assertEqual(stopped.exception.code, 1)
            self.assertNotIn("SENSITIVE-DOWNLOAD-ERROR", stderr.getvalue())
            self.assertIn("network or filesystem error", stderr.getvalue())


STUB = r'''
import json, os, pathlib, signal, subprocess, sys, time
root = pathlib.Path(os.environ.get("DATABASE_GATE_TEST_ROOT", pathlib.Path(__file__).resolve().parent.parent))
case = os.environ.get("DATABASE_GATE_TEST_CASE") or (root / "case").read_text()
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
if tool == "python3":
    if args[:1] == ["-c"]:
        os.execv(sys.executable, [sys.executable, *args])
    assert args[:2] == ["-I", "-c"] and len(args) == 6
    assert not any(key in os.environ for key in ("HOME", "HTTPS_PROXY", "HTTP_PROXY", "PYTHONPATH", "SSL_CERT_FILE", "NETRC"))
    url, destination, digest = args[3:]
    assert url.startswith("https://dl-cdn.alpinelinux.org/alpine/v3.23/main/aarch64/")
    if case == "procps_fetch_signal":
        block()
    if case == "procps_fetch_failure":
        sys.exit(29)
    if case == "procps_hash_failure":
        sys.exit(30)
    if case == "procps_size_failure":
        sys.exit(34)
    pathlib.Path(destination).write_bytes(b"harmless archive model; real helper integrity is tested separately")
elif tool == "git":
    if args[:1] == ["rev-parse"]:
        print("1" * 40 if args[1] == "HEAD" else "2" * 40)
    elif args[:2] == ["ls-files", "-s"]:
        for path in args[2:]:
            print("100644 " + "a" * 40 + "\t" + path)
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
        elif args[1:4] == [identity, "env", "-i"]:
            assert "APK_CONFIG=/dev/null" in args
            if "apk" in args:
                assert args[args.index("--repositories-file") + 1] == "/dev/null"
                assert args[args.index("--keys-dir") + 1] == "/etc/apk/keys"
                assert "--allow-untrusted" not in args
                assert "fetch" not in args and "--repository" not in args
                assert "--no-network" in args
                if "verify" in args and case == "procps_signature_failure":
                    sys.exit(32)
                if "add" in args:
                    if case == "procps_install_failure":
                        sys.exit(31)
                    state["outer"]["procps_installed"] = True
                    save()
                if "info" in args and case == "procps_version_failure":
                    sys.exit(33)
            elif args[-3:] == ["ps", "-eo", "pid,uid"]:
                assert state["outer"].get("procps_installed")
                if case == "procps_unsupported":
                    print("ps: unsupported uid column", file=sys.stderr)
                    sys.exit(28)
                print({"procps_bad_header": "USER PID\n0 1", "procps_bad_uid": "PID UID\n1 root",
                       "procps_duplicate_pid": "PID UID\n1 0\n1 0", "procps_bad_pid": "PID UID\n01 0",
                       "procps_missing_rows": "PID UID", "procps_oversized": "x" * 8193}.get(case, "PID UID\n1 0\n2 999"))
            else:
                raise RuntimeError("unexpected provisioning exec")
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
            if case == "upgrade_failed":
                print("--- FAIL: TestHostingDatabaseUpgradeCompiledRunner (0.01s)")
                sys.exit(37)
            if case == "upgrade_skipped":
                print("--- SKIP: TestHostingDatabaseUpgradeCompiledRunner (0.00s)")
            elif case != "upgrade_missing":
                print("--- PASS: TestHostingDatabaseUpgradeCompiledRunner (0.01s)")
            if case == "upgrade_skipped_after_pass":
                print("--- SKIP: TestHostingDatabaseUpgradeCompiledRunner (0.00s)")
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
        self.installed_closure = None
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
            (root / "case").write_text(case)
            binary = root / "bin"
            binary.mkdir()
            for name in ("git", "go", "docker", "python3"):
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
                self.source_manifests = [args[2:] for args in commands if args[:2] == ["ls-files", "-s"]]
                if expected == 0:
                    self.assertIn("head=" + "1" * 40 + "\ntree=" + "2" * 40, output)
                    recorded = [line.split("\t", 1)[1] for line in (evidence / "source-blobs").read_text().splitlines()]
                    self.assertEqual(recorded, self.source_manifests[0])
                    for name, binary in (("api.test", evidence / "api.test"), ("nerocd", evidence / "nerocd"),
                                         ("client", evidence / "client/client")):
                        self.assertEqual((evidence / (name + "-sha256")).read_text(),
                                         hashlib.sha256(binary.read_bytes()).hexdigest() + "\n")
                        self.assertIn(["version", "-m", str(binary)], commands)
                        self.assertEqual((evidence / (name + "-build-info")).read_text(), "GOOS=linux GOARCH=arm64\n")
                docker_commands = [args[2:] for args in commands if args[:2] == ["--host", "unix:///var/run/docker.sock"]]
                self.download_commands = [args for args in commands if args[:2] == ["-I", "-c"]]
                if (evidence / "procps-installed-closure").exists():
                    closure = json.loads((evidence / "procps-installed-closure").read_text())
                    self.assertTrue(closure["exact_installed_versions_verified"])
                    self.assertEqual(closure["architecture"], "aarch64")
                    self.assertEqual(closure["repository"], "https://dl-cdn.alpinelinux.org/alpine/v3.23/main")
                    self.assertEqual(closure["keys"], "/etc/apk/keys")
                    self.installed_closure = closure["packages"]
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

    def test_missing_or_skipped_upgrade_cannot_qualify_deployment_and_capture_only(self):
        for case in ("upgrade_missing", "upgrade_skipped", "upgrade_skipped_after_pass"):
            with self.subTest(case=case):
                state, commands, output = self.run_gate(case, 1)
                self.assertEqual(state, {})
                message = "required physical test skipped: " if case == "upgrade_skipped_after_pass" else "required physical test did not pass: "
                self.assertIn(message + "TestHostingDatabaseUpgradeCompiledRunner", output)
                self.assertNotIn("private PostgreSQL and Redis gate passed", output)
                self.assertIn(["container", "rm", "--force", IDENTITY], commands)
                self.assertIn(["volume", "rm", VOLUME], commands)

    def test_upgrade_failure_retains_exit_and_cleans_owned_fixture(self):
        state, commands, output = self.run_gate("upgrade_failed", 37)
        self.assertEqual(state, {})
        self.assertNotIn("private PostgreSQL and Redis gate passed", output)
        self.assertIn(["container", "rm", "--force", IDENTITY], commands)
        self.assertIn(["volume", "rm", VOLUME], commands)

    def test_sigterm_during_runtime_joins_child_and_cleans_owned_fixture(self):
        state, commands, _ = self.run_gate("runtime_sigterm", 143, signal.SIGTERM)
        self.assertEqual(state, {})
        self.assertIn(["container", "rm", "--force", IDENTITY], commands)

    def test_source_manifest_covers_upgrade_contract_and_production_inputs(self):
        state, _, _ = self.run_gate("success", 0)
        self.assertEqual(state, {})
        self.assertEqual(len(self.source_manifests), 1)
        manifest = self.source_manifests[0]
        self.assertEqual(len(manifest), len(set(manifest)), "source evidence contains duplicate paths")
        required = {
            'cmd/nerocd/runner.go',
            'cmd/nerocd/runner_database_config.go',
            'db/migrations/0071_hosting_database_network_upgrades.sql',
            'internal/api/hosting_database_capture_runtime_integration_test.go',
            'internal/api/hosting_database_runtime_integration_test.go',
            'internal/api/hosting_database_upgrade_runtime_integration_test.go',
            'internal/app/hosting_database_upgrade_service.go',
            'internal/domain/hosting_database_network_plan.go',
            'internal/domain/hosting_database_upgrade_execution.go',
            'internal/domain/hosting_execution.go',
            'internal/runner/journal.go',
            'internal/runner/journal_database.go',
            'internal/runner/journal_database_network_capture.go',
            'internal/runner/journal_hosting.go',
            'internal/runner/journal_storage.go',
            'internal/runnerclient/hosting_database_delivery.go',
            'internal/runnerclient/hosting_execution.go',
            'internal/runnerclient/hosting_proxy.go',
            'internal/runnerruntime/hosting_database_execution.go',
            'internal/runnerruntime/hosting_database_execution_config.go',
            'internal/runnerruntime/hosting_database_network_upgrade.go',
            'internal/runnerruntime/hosting_database_replay.go',
            'internal/runnerruntime/hosting_database_upgrade_execution.go',
            'internal/runnerruntime/hosting_runtime_environment.go',
            'internal/store/hosting_database_upgrade_repository.go',
            'internal/store/postgres_hosting_database_delivery.go',
            'internal/store/postgres_hosting_database_execution.go',
            'internal/store/postgres_hosting_database_upgrade.go',
            'internal/store/postgres_hosting_execution.go',
            'internal/store/postgres_hosting_storage_materialization.go',
            'internal/store/sql/hosting_database_network_upgrades.sql',
            'internal/store/sqlcgen/hosting_database_network_upgrades.sql.go',
            'internal/store/sqlcgen/models.go',
        }
        self.assertFalse(required - set(manifest), "upgrade source evidence omitted: " + str(sorted(required - set(manifest))))


    def test_harmless_complete_model(self):
        state, commands, output = self.run_gate("success", 0)
        self.assertEqual(state, {})
        self.assertIn("harmless runtime model passed", output)
        runtime_command = next(args for args in commands if "/fixture/api.test" in args)
        self.assertEqual(runtime_command[runtime_command.index("-test.run") + 1],
                         "^TestHostingDatabase(CompiledRunner|CaptureCompiledRunner|UpgradeCompiledRunner)$")
        self.assertIn("-test.timeout=25m", runtime_command)
        self.assertEqual(sum("/fixture/api.test" in args for args in commands), 1)
        create = next(index for index, args in enumerate(commands) if args[:2] == ["container", "create"])
        start = next(index for index, args in enumerate(commands) if args[:2] == ["container", "start"])
        self.assertTrue(any(args[:2] == ["volume", "inspect"] for args in commands[create + 1:start]))
        self.assertTrue(any(args[:2] == ["container", "inspect"] for args in commands[create + 1:start]))


    def test_procps_preflight_precedes_database_execution(self):
        state, commands, output = self.run_gate("success", 0)
        self.assertEqual(state, {})
        provision = [args for args in commands if args[:4] == ["exec", IDENTITY, "env", "-i"]]
        archives = {"libintl-0.24.1-r1.apk", "libncursesw-6.5_p20251123-r0.apk", "libproc2-4.0.5-r0.apk",
                    "musl-1.2.5-r24.apk", "ncurses-terminfo-base-6.5_p20251123-r0.apk", "procps-ng-4.0.5-r0.apk",
                    "skalibs-libs-2.14.4.0-r0.apk", "utmps-libs-0.1.3.1-r0.apk"}
        self.assertEqual(len(self.download_commands), 8)
        self.assertEqual({args[-3].rsplit("/", 1)[1] for args in self.download_commands}, archives)
        self.assertTrue(all(args[-3] == "https://dl-cdn.alpinelinux.org/alpine/v3.23/main/aarch64/" + Path(args[-2]).name
                            for args in self.download_commands))
        self.assertEqual([(Path(args[-2]).stem, args[-1]) for args in self.download_commands],
                         [(name + "-" + version, digest) for name, version, digest in EXPECTED_PROCPS_PACKAGES])
        self.assertEqual(self.installed_closure,
                         [dict(name=name, version=version, sha256=digest) for name, version, digest in EXPECTED_PROCPS_PACKAGES])
        stages = [next(stage for stage in ("verify", "add", "info", "ps") if stage in args)
                  for args in provision]
        self.assertEqual(stages, ["verify", "add", "info", "ps"])
        environment = ["exec", IDENTITY, "env", "-i",
                       "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "APK_CONFIG=/dev/null"]
        apk = [*environment, "apk", "--arch", "aarch64", "--no-cache", "--repositories-file", "/dev/null",
               "--keys-dir", "/etc/apk/keys", "--no-network"]
        files = ["/fixture/apk/" + name + "-" + version + ".apk" for name, version, _ in EXPECTED_PROCPS_PACKAGES]
        specs = [name + "=" + version for name, version, _ in EXPECTED_PROCPS_PACKAGES]
        self.assertEqual(provision, [[*apk, "verify", *files], [*apk, "add", *files],
                                     [*apk, "info", "--exists", *specs], [*environment, "ps", "-eo", "pid,uid"]])
        copied = next(args for args in commands if args[:1] == ["cp"] and args[-1] == IDENTITY + ":/fixture/apk")
        self.assertLess(commands.index(copied), commands.index(provision[0]))
        probe = next(args for args in provision if args[-3:] == ["ps", "-eo", "pid,uid"])
        runtime = next(args for args in commands if "/fixture/api.test" in args)
        self.assertLess(commands.index(probe), commands.index(runtime))
        self.assertIn("numeric PID/UID process preflight passed", output)

    def test_procps_provisioning_failure_cleans_owned_resources(self):
        for case, expected in (("fetch", 29), ("hash", 30), ("size", 34), ("signature", 32), ("install", 31), ("version", 33)):
            with self.subTest(stage=case):
                state, commands, output = self.run_gate("procps_" + case + "_failure", expected)
                self.assertEqual(state, {})
                self.assertIn("procps preflight failed:", output)
                self.assertIn(["container", "rm", "--force", IDENTITY], commands)
                self.assertIn(["volume", "rm", VOLUME], commands)
                self.assertFalse(any("/fixture/api.test" in args for args in commands))

    def test_procps_rejects_unsupported_or_malformed_numeric_profile(self):
        for case in ("unsupported", "bad_header", "bad_uid", "duplicate_pid", "bad_pid", "missing_rows", "oversized"):
            with self.subTest(case=case):
                state, commands, output = self.run_gate("procps_" + case, 28 if case == "unsupported" else 1)
                self.assertEqual(state, {})
                self.assertIn("procps preflight failed:", output)
                self.assertIn(["container", "rm", "--force", IDENTITY], commands)
                self.assertIn(["volume", "rm", VOLUME], commands)
                self.assertFalse(any("/fixture/api.test" in args for args in commands))

    def test_sigterm_during_package_fetch_joins_and_cleans(self):
        state, commands, _ = self.run_gate("procps_fetch_signal", 143, signal.SIGTERM)
        self.assertEqual(state, {})
        self.assertIn(["container", "rm", "--force", IDENTITY], commands)
        self.assertIn(["volume", "rm", VOLUME], commands)


if __name__ == "__main__":
    unittest.main()
