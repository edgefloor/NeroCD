"""Exercise the real fixed-target launcher with harmless stateful CLI stubs."""

import ast
import functools
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


@functools.lru_cache(maxsize=4)
def storage_selection_output(selection="selected", outcome="success"):
    """Run real Go selection with the route fixture's exact loop bookkeeping."""
    root = Path(__file__).resolve().parents[2]
    source = (root / "internal/api/hosting_application_database_route_runtime_integration_test.go").read_text()
    function = source.split("func testHostingApplicationDatabaseRouteFailuresCompiledRunner(", 1)[1]
    loop = function[function.index('\tfor _, engine := range applicationDatabaseFixtureEngines(t)') :]
    before, body = loop.split('\t\t\t\tapplicationDatabaseRouteCapacity(t)', 1)
    _, after = body.rsplit('\n\t\t\t}) {', 1)
    # Keep the real t.Run name, completion guard, capacity check and marker
    # emission. Replace only the privileged callback body with harmless work.
    code = 'package selection\nimport ("fmt"; "testing")\n'
    code += 'func applicationDatabaseRouteCapacity(t *testing.T) {}\n'
    code += 'func applicationDatabaseFixtureEngines(t *testing.T) []string { return []string{"postgresql", "redis"} }\n'
    code += 'func TestHostingApplicationDatabaseStorageFailuresCompiledRunner(t *testing.T) {\nstorage := true\n'
    code += before
    if outcome == "skip":
        code += '\n t.Skip("isolated selected skip")\n'
    elif outcome == "fail":
        code += '\n t.Fatal("isolated selected failure")\n'
    else:
        code += '\n fmt.Println("DATABASE_CONNECTION_STORAGE_RECOVERY_PASS " + engine + " " + failure)\n'
    code += '\n\t\t\t}) {' + after
    pattern = "^TestHostingApplicationDatabaseStorageFailuresCompiledRunner$"
    if selection == "selected":
        pattern += "/^postgresql$/^cutover$"
    with tempfile.TemporaryDirectory(prefix="nerocd-storage-selection-") as temporary:
        fixture = Path(temporary) / "selection_test.go"
        fixture.write_text(code)
        result = subprocess.run(["go", "test", "-v", "-count=1", "-run", pattern, str(fixture)],
                                cwd=root, capture_output=True, text=True, timeout=30)
    if result.returncode != (1 if outcome == "fail" else 0):
        raise AssertionError("isolated Go selection failed: " + result.stdout + result.stderr)
    return result.stdout


class SelectionOutputTest(unittest.TestCase):
    def test_only_selected_completed_case_emits_capacity(self):
        output = storage_selection_output()
        markers = [line for line in output.splitlines() if line.startswith("DATABASE_CONNECTION_")]
        self.assertEqual(markers, ["DATABASE_CONNECTION_STORAGE_RECOVERY_PASS postgresql cutover",
                                   "DATABASE_CONNECTION_STORAGE_CAPACITY_PASS postgresql cutover"], output)
        passed = [line.strip().split(" ")[2] for line in output.splitlines() if line.strip().startswith("--- PASS: ")]
        self.assertEqual(set(passed), {"TestHostingApplicationDatabaseStorageFailuresCompiledRunner",
                                      "TestHostingApplicationDatabaseStorageFailuresCompiledRunner/postgresql/cutover"}, output)
        self.assertEqual(len(passed), 2, output)

    def test_full_selection_keeps_every_capacity_marker(self):
        output = storage_selection_output("full")
        for engine in ("postgresql", "redis"):
            for failure in ("readiness", "cutover"):
                self.assertEqual(output.splitlines().count("DATABASE_CONNECTION_STORAGE_CAPACITY_PASS " + engine + " " + failure), 1, output)

    def test_skipped_or_failed_case_has_no_capacity_marker(self):
        for outcome in ("skip", "fail"):
            with self.subTest(outcome=outcome):
                output = storage_selection_output(outcome=outcome)
                self.assertNotIn("DATABASE_CONNECTION_STORAGE_CAPACITY_PASS", output)


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
backup = os.environ.get("NEROCD_DATABASE_BACKUP_RUNTIME") == "1"
owner = "nerocd-postgresql-backup-runtime-20261025-pbr7319" if backup else "nerocd-database-runner-20261024-dbr7319"
name, volume = owner + "-dind", owner + "-docker-data"
label = "io.nerocd.postgresql-backup-runtime.owner" if backup else "io.nerocd.database-runner.owner"
label_value = "20261025-pbr7319" if backup else owner
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
elif tool == "bun":
    assert args == ["run", "--cwd", "web/app", "build"]
    if case == "browser_build_failure":
        sys.exit(16)
    built = root / "web/dist/index.html"
    built.parent.mkdir(parents=True)
    built.write_text("harmless built UI artifact")
elif tool == "node":
    assert args[0] == "acceptance/runtime-hosting-database-runner/browser-driver.mjs"
    assert args[1] == "http://127.0.0.1:35002"
    assert "NEROCD_DATABASE_CONNECTION_BROWSER=1" in args
    assert "NEROCD_DATABASE_CONNECTION_BROWSER_ORIGIN=http://127.0.0.1:35002" in args
    if case == "full_receipt_from_diagnostic":
        print((root/"selection-output").read_text())
        sys.exit(0)
    for name in ("TestHostingDatabaseCompiledRunner", "TestHostingDatabaseCaptureCompiledRunner", "TestHostingDatabaseUpgradeCompiledRunner", "TestHostingApplicationDatabaseCompiledRunner"):
        print("--- PASS: " + name + " (0.01s)")
    print("DATABASE_CONNECTION_BROWSER_PASS postgresql")
    if case != "browser_missing_result":
        print("DATABASE_CONNECTION_BROWSER_PASS redis")
    if "NEROCD_DATABASE_CONNECTION_ROUTES=1" in args:
        print("DATABASE_CONNECTION_ROUTE_PASS postgresql")
        if case != "route_missing_result": print("DATABASE_CONNECTION_ROUTE_PASS redis")
        print("--- PASS: TestHostingApplicationDatabaseRouteFailuresCompiledRunner (0.01s)")
        for engine in ("postgresql", "redis"):
            for fault in ("readiness", "cutover"):
                if case != "route_missing_failure": print("DATABASE_CONNECTION_ROUTE_FAILURE_PASS " + engine + " " + fault)
            for phase in ("success", "readiness", "cutover"):
                if case != "route_missing_capacity": print("DATABASE_CONNECTION_ROUTE_CAPACITY_PASS " + engine + " " + phase)
    if case == "browser_driver_failure":
        sys.exit(39)
    if "NEROCD_DATABASE_CONNECTION_STORAGE=1" in args:
        if case != "storage_missing_test": print("--- PASS: TestHostingApplicationDatabaseStorageCompiledRunner (0.01s)")
        if case != "storage_missing_recovery_test": print("--- PASS: TestHostingApplicationDatabaseStorageFailuresCompiledRunner (0.01s)")
        for engine in ("postgresql", "redis"):
            if case != "storage_missing_result": print("DATABASE_CONNECTION_STORAGE_PASS " + engine)
            if case != "storage_missing_browser": print("DATABASE_CONNECTION_STORAGE_BROWSER_PASS " + engine)
            if case != "storage_missing_capacity": print("DATABASE_CONNECTION_STORAGE_CAPACITY_PASS " + engine + " success")
            for fault in ("readiness", "cutover"):
                if case != "storage_missing_recovery": print("DATABASE_CONNECTION_STORAGE_RECOVERY_PASS " + engine + " " + fault)
                if case != "storage_missing_recovery_capacity": print("DATABASE_CONNECTION_STORAGE_CAPACITY_PASS " + engine + " " + fault)
        for engine,action in (("postgresql","resume_predecessor"),("redis","complete_replacement")):
            if case != "storage_missing_recovery_browser": print("DATABASE_CONNECTION_STORAGE_RECOVERY_BROWSER_PASS " + engine + " " + action)
            if case != "storage_missing_recovery_ui": print("DATABASE_CONNECTION_STORAGE_RECOVERY_UI_PASS " + engine + " " + action)
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
        state["volume"] = dict(Name=volume, Driver="local", Scope="local", Labels={label: label_value},
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
            Config=dict(Labels={label: label_value}, Cmd=["dockerd", "--host=unix:///var/run/docker.sock"]),
            HostConfig=dict(Privileged=True, PidMode="", UsernsMode="", Binds=None,
                            PortBindings={}, PublishAllPorts=False, NetworkMode="default"),
            Mounts=[dict(Type="volume", Name=volume, Driver="local", Destination="/var/lib/docker", RW=True)],
            State=dict(Running=False))
        if backup:
            state["outer"]["HostConfig"].update(NanoCpus=4000000000,Memory=4294967296,PidsLimit=1024)
        if "--publish" in args:
            assert args[args.index("--publish") + 1] == "127.0.0.1::18080"
            state["outer"]["HostConfig"]["PortBindings"] = {"18080/tcp":[dict(HostIp="127.0.0.1",HostPort="")]}
        if case == "browser_public":
            state["outer"]["HostConfig"]["PortBindings"]["18080/tcp"][0]["HostIp"]="0.0.0.0"
        if case == "browser_extra":
            state["outer"]["HostConfig"]["PortBindings"]["18081/tcp"]=[dict(HostIp="127.0.0.1",HostPort="")]
        if case == "outer_unexpected_port":
            state["outer"]["HostConfig"]["PortBindings"]={"18080/tcp":[dict(HostIp="127.0.0.1",HostPort="")]}
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
        if state["outer"]["HostConfig"]["PortBindings"]:
            state["outer"]["NetworkSettings"] = dict(Ports={"18080/tcp":[dict(HostIp="127.0.0.1",HostPort="35002")]})
            if case == "browser_exposed":
                state["outer"]["NetworkSettings"]["Ports"].update({"2375/tcp":None,"2376/tcp":None})
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
            if case.startswith("retention_diagnostic"):
                names=["TestHostingDatabaseBackupRetentionFailuresCompiledRunner","TestHostingDatabaseBackupRetentionProtectionsCompiledRunner"]
                assert args[args.index("-test.run")+1] == "^(?:"+"|".join(names)+")$"
                entries=names+[names[0]+"/"+kind for kind in ("delete_denied","conditional_unsupported","authorization_revoked")]
                markers=["DATABASE_BACKUP_RETENTION_FAILURE_PASS postgresql "+kind for kind in ("delete_denied","conditional_unsupported","authorization_revoked")]
                markers += ["DATABASE_BACKUP_RETENTION_PROTECTION_PASS postgresql newest_shared_active_failed_upload_external","DATABASE_BACKUP_RETENTION_INDEPENDENT_LIMITS_PASS postgresql"]
                if case=="retention_diagnostic_missing_case":entries.pop()
                if case=="retention_diagnostic_missing_marker":markers.pop()
                for entry in entries:print("--- PASS: "+entry+" (0.01s)")
                for marker in markers:print(marker)
                if case=="retention_diagnostic_skipped":print("--- SKIP: "+entries[-1]+" (0.01s)")
                if case=="retention_diagnostic_unrelated":print("--- PASS: TestHostingDatabaseBackupCompiledRunner (0.01s)")
                if case=="retention_diagnostic_duplicate":print(markers[-1])
                sys.exit(0)
            if case.startswith("cutover_diagnostic"):
                assert args[args.index("-test.run")+1] == "^TestHostingApplicationDatabaseStorageFailuresCompiledRunner$/^postgresql$/^cutover$"
                output=(root/"selection-output").read_text()
                if case == "cutover_diagnostic_missing_case": output="\n".join(line for line in output.splitlines() if "--- PASS: TestHostingApplicationDatabaseStorageFailuresCompiledRunner/postgresql/cutover " not in line)
                if case == "cutover_diagnostic_missing_capacity": output=output.replace("DATABASE_CONNECTION_STORAGE_CAPACITY_PASS postgresql cutover", "")
                print(output)
                if case == "cutover_diagnostic_foreign_result": print("DATABASE_CONNECTION_STORAGE_PASS redis")
                if case == "cutover_diagnostic_duplicate_case": print("--- PASS: TestHostingApplicationDatabaseStorageFailuresCompiledRunner/postgresql/cutover (0.01s)")
                if case == "cutover_diagnostic_skipped_case": print("--- SKIP: TestHostingApplicationDatabaseStorageFailuresCompiledRunner/postgresql/cutover (0.01s)")
                sys.exit(0)
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
            if case == "connection_failed":
                print("--- FAIL: TestHostingApplicationDatabaseCompiledRunner (0.01s)")
                sys.exit(38)
            if case == "connection_skipped":
                print("--- SKIP: TestHostingApplicationDatabaseCompiledRunner (0.00s)")
            elif case != "connection_missing":
                print("--- PASS: TestHostingApplicationDatabaseCompiledRunner (0.01s)")
            if case == "connection_skipped_after_pass":
                print("--- SKIP: TestHostingApplicationDatabaseCompiledRunner (0.00s)")
            if case == "upgrade_skipped_after_pass":
                print("--- SKIP: TestHostingDatabaseUpgradeCompiledRunner (0.00s)")
            if "NEROCD_DATABASE_CONNECTION_ROUTES=1" in args:
                print("DATABASE_CONNECTION_ROUTE_PASS postgresql")
                if case != "route_missing_result": print("DATABASE_CONNECTION_ROUTE_PASS redis")
                if case == "route_duplicate_result": print("DATABASE_CONNECTION_ROUTE_PASS redis")
                if case != "route_missing_failure_test": print("--- PASS: TestHostingApplicationDatabaseRouteFailuresCompiledRunner (0.01s)")
                for engine in ("postgresql", "redis"):
                    for fault in ("readiness", "cutover"):
                        if case != "route_missing_failure": print("DATABASE_CONNECTION_ROUTE_FAILURE_PASS " + engine + " " + fault)
                    for phase in ("success", "readiness", "cutover"):
                        if case != "route_missing_capacity": print("DATABASE_CONNECTION_ROUTE_CAPACITY_PASS " + engine + " " + phase)
        elif args[1:3] == [identity, "docker"]:
            if "container" in args and "inspect" in args and "e" * 64 in args:
                print(json.dumps(dict(Id="e" * 64, Name="/"+owner+"-registry", Config=dict(Image="registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373",Labels={label:"foreign" if case=="registry_foreign" else label_value}), State=dict(Running=True), HostConfig=dict(Privileged=False,PortBindings={"5000/tcp":[dict(HostIp="0.0.0.0" if case=="registry_public" else "127.0.0.1",HostPort="")]}), NetworkSettings=dict(Ports={"5000/tcp":[dict(HostIp="127.0.0.1",HostPort="35001")]}))))
            elif "image" in args and "inspect" in args:
                digest = "b" if any(arg.endswith("-storage") for arg in args) and case != "storage_digest_collision" else "a"
                print(json.dumps(["127.0.0.1:35001/nerocd-http-app@sha256:"+digest*64] if case!="registry_digest_foreign" else ["foreign/app@sha256:"+"a"*64]))
            elif "--detach" in args:
                print("e" * 64)
            elif "--command=SELECT current_database()" in args:
                print("nerocd_database_runner")
        elif args[1:3] == [identity, "wget"]:
            print("{}")
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


class SourceManifestTest(unittest.TestCase):
    def test_combined_recovery_component_is_tracked(self):
        source = Path(__file__).with_name("gate.sh").read_text().split("# PYTHON SOURCE\n", 1)[1].rsplit("\nPY\n", 1)[0]
        manifests = [ast.literal_eval(node.args[0]) for node in ast.walk(ast.parse(source))
                     if isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id == "run"
                     and any(item.arg == "output" and isinstance(item.value, ast.Constant)
                             and item.value.value == "source-blobs" for item in node.keywords)]
        self.assertEqual(len(manifests), 1)
        self.assertEqual(manifests[0][:3], ["git", "ls-files", "-s"])
        component = "web/app/src/components/hosting/ConnectedStorageRecovery.tsx"
        # The fixture branch does not contain the sibling UI candidate. Model
        # its tracked path using real Git, which silently omits wrong pathspecs.
        with tempfile.TemporaryDirectory(prefix="nerocd-source-manifest-test-") as temporary:
            root = Path(temporary)
            environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
            environment.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1")
            def git(*args):
                return subprocess.run(["git", "-c", "core.fsmonitor=false", *args], cwd=root,
                                      env=environment, capture_output=True, text=True, timeout=5, check=True).stdout
            git("init", "--quiet")
            path = root / component
            path.parent.mkdir(parents=True)
            path.write_text("export const recoveryFixture = true;\n")
            git("add", "--", component)
            expected = "100644 " + git("hash-object", component).strip() + " 0\t" + component
            self.assertEqual(git(*manifests[0][1:]).splitlines(), [expected])
            self.assertNotIn("web/app/src/pages/ConnectedStorageRecovery.tsx", manifests[0])


class GateTest(unittest.TestCase):
    def run_gate(self, case, expected, delivered_signal=None, browser=False, routes=False, storage=False, mode="full", retention=False):
        self.installed_closure = None
        gate = Path(os.environ.get("DATABASE_GATE_UNDER_TEST", Path(__file__).with_name("gate.sh"))).resolve()
        with tempfile.TemporaryDirectory(prefix="nerocd-database-gate-test-") as temporary:
            root = Path(temporary)
            fixture = root / "internal/api/hosting_database_runtime_integration_test.go"
            fixture.parent.mkdir(parents=True)
            fixture.touch()
            ui = root / "web/app/src/pages/ResourceDatabaseConnections.tsx"
            ui.parent.mkdir(parents=True)
            if case != "browser_source_missing":
                ui.touch()
            client = root / "acceptance/runtime-hosting-database-client"
            client.mkdir(parents=True)
            (client / "Dockerfile").write_text("FROM scratch\nCOPY client /client\n")
            (client / "Dockerfile.http").write_text("FROM scratch\nCOPY client /client\nENTRYPOINT [\"/client\",\"serve\"]\n")
            (client / "Dockerfile.http-storage").write_text("FROM scratch\nCOPY client /client\nENV FIXTURE_UPLOADS=1\nUSER 0:0\nENTRYPOINT [\"/client\",\"serve\"]\n")
            proxy = root / "acceptance/runtime-proxy-control"
            proxy.mkdir(parents=True)
            (proxy / "pebble.json").write_bytes((Path(__file__).resolve().parents[2] / "acceptance/runtime-proxy-control/pebble.json").read_bytes())
            initial = {}
            if case == "preexisting_foreign":
                initial["volume"] = {"Name": VOLUME, "Labels": {LABEL: "foreign"}}
            if case == "preexisting_outer":
                initial["outer"] = {"Id": IDENTITY, "Name": "/" + NAME}
            (root / "state").write_text(json.dumps(initial))
            (root / "case").write_text(case)
            if case.startswith("cutover_diagnostic") or case == "full_receipt_from_diagnostic":
                (root / "selection-output").write_text(storage_selection_output())
            binary = root / "bin"
            binary.mkdir()
            for name in ("git", "go", "docker", "python3", "bun", "node"):
                executable = binary / name
                executable.write_text("#!" + sys.executable + "\n" + STUB)
                executable.chmod(0o700)
            environment = dict(os.environ, PATH=str(binary) + os.pathsep + os.environ["PATH"], TMPDIR=str(root),
                               DATABASE_GATE_TEST_ROOT=str(root), DATABASE_GATE_TEST_CASE=case, NEROCD_DATABASE_CONNECTION_BROWSER="bad" if case=="browser_bad_mode" else "1" if browser else "0", NEROCD_DATABASE_CONNECTION_ROUTES="bad" if case=="route_bad_mode" else "1" if routes else "0")
            environment["NEROCD_DATABASE_CONNECTION_STORAGE"] = "bad" if case == "storage_bad_mode" else "1" if storage else "0"
            environment["NEROCD_DATABASE_RUNNER_MODE"] = mode
            for key in ("NEROCD_DATABASE_BACKUP_RUNTIME","NEROCD_DATABASE_BACKUP_REMOTE_RUNTIME","NEROCD_DATABASE_BACKUP_RETENTION_RUNTIME"):
                environment[key] = "1" if retention else "0"
            environment["NEROCD_DATABASE_BACKUP_SCHEDULE_RUNTIME"] = "0"
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
                result_name, exit_key = ("result", "gate_exit") if mode == "full" else ("diagnostic-result", "diagnostic_exit")
                receipt = (evidence / result_name).read_text()
                self.assertIn(f"{exit_key}={expected}\n", receipt)
                self.receipt = receipt
                self.assertEqual(output.count("gate exit=" if mode == "full" else "diagnostic exit="), 1, output)
                if mode != "full": self.assertFalse((evidence / "result").exists())
                for pid in blocked:
                    with self.assertRaises(ProcessLookupError, msg=output):
                        os.kill(pid, 0)
                if blocked:
                    self.assertIn("owned process group joined: " + str(blocked[0]), output)
                commands = [json.loads(line)[1:] for line in (root / "commands").read_text().splitlines()]
                self.all_commands = commands
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
                                 log=log, evidence=root, say=messages.append, browser_mode="0", outer_deadline=None, cleaning=False)
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


    def test_connected_physical_result_is_required(self):
        for case in ("connection_missing", "connection_skipped", "connection_skipped_after_pass"):
            with self.subTest(case=case):
                state, _, output = self.run_gate(case, 1)
                self.assertEqual(state, {})
                self.assertIn("TestHostingApplicationDatabaseCompiledRunner", output)
        state, _, _ = self.run_gate("connection_failed", 38)
        self.assertEqual(state, {})

    def test_inner_registry_trust_and_digest_fail_closed(self):
        for case in ("registry_foreign", "registry_public", "registry_digest_foreign"):
            with self.subTest(case=case):
                state, commands, _ = self.run_gate(case, 1)
                self.assertEqual(state, {})
                self.assertFalse(any("/fixture/api.test" in args for args in commands))

    def test_connected_registry_profile_and_source_evidence(self):
        state, commands, _ = self.run_gate("success", 0)
        self.assertEqual(state, {})
        registry = next(args for args in commands if OWNER + "-registry" in args)
        self.assertEqual(registry[:5], ["exec", IDENTITY, "docker", "--host", "unix:///var/run/docker.sock"])
        self.assertEqual(registry[registry.index("--publish") + 1], "127.0.0.1::5000")
        self.assertEqual(registry[registry.index("--tmpfs") + 1], "/var/lib/registry:rw")
        self.assertEqual(registry[-1], "registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373")
        build = next(args for args in commands if "/fixture/client/Dockerfile.http" in args)
        self.assertEqual(build[build.index("--file") + 1], "/fixture/client/Dockerfile.http")
        remove = next(i for i, args in enumerate(commands) if args[5:7] == ["image", "rm"])
        execute = next(i for i, args in enumerate(commands) if "/fixture/api.test" in args)
        self.assertLess(remove, execute)
        self.assertIn("NEROCD_DATABASE_HTTP_APP_IMAGE=127.0.0.1:35001/nerocd-http-app@sha256:" + "a" * 64, commands[execute])
        self.assertTrue({"internal/api/hosting_application_database_runtime_integration_test.go",
                         "internal/api/hosting_database_connection*.go",
                         "internal/store/postgres_hosting_database_connection*.go",
                         "db/migrations/0072_hosting_application_database_connections.sql",
                         "internal/runnerruntime/hosting_application_database_execution.go",
                         "internal/runnerruntime/hosting_database_maintenance_execution.go"}.issubset(self.source_manifests[0]))

    def test_browser_bridge_and_embedding_order(self):
        state, commands, _ = self.run_gate("browser_exposed", 0, browser=True)
        self.assertEqual(state,{})
        create=next(args for args in commands if args[:2]==["container","create"])
        self.assertEqual(create[create.index("--publish")+1],"127.0.0.1::18080")
        build=next(i for i,args in enumerate(self.all_commands) if args==["run","--cwd","web/app","build"])
        compile=next(i for i,args in enumerate(self.all_commands) if args[:2]==["test","-c"])
        self.assertLess(build,compile)
        driver=next(args for args in self.all_commands if args[:1]==["acceptance/runtime-hosting-database-runner/browser-driver.mjs"])
        self.assertEqual(driver[1],"http://127.0.0.1:35002")

    def test_trusted_route_profile_uses_existing_contained_tls_fixture(self):
        state, commands, output = self.run_gate("route_success", 0, routes=True)
        self.assertEqual(state, {})
        builds = [args for args in self.all_commands if args[:2] == ["test", "-c"] or args[:1] == ["build"]]
        for args in builds[:2]:
            self.assertIn("nerocd_proxy_fixture", args)
        self.assertNotIn("nerocd_proxy_fixture", builds[2])
        pebble = "ghcr.io/letsencrypt/pebble@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199"
        self.assertIn(["exec", IDENTITY, "docker", "--host", "unix:///var/run/docker.sock", "pull", pebble], commands)
        copied = next(args for args in commands if args[0] == "cp" and args[-1] == IDENTITY+":/fixture/pebble.json")
        self.assertNotEqual(copied[1], "acceptance/runtime-proxy-control/pebble.json")
        self.assertEqual(Path(copied[1]).name, "pebble.json")
        outer = next(args for args in commands if args[:2] == ["container", "create"])
        self.assertNotIn("--publish", outer)
        runtime = next(args for args in commands if "/fixture/api.test" in args)
        self.assertIn("NEROCD_DATABASE_CONNECTION_ROUTES=1", runtime)
        self.assertIn("-test.failfast", runtime)
        self.assertIn("DATABASE_CONNECTION_ROUTE_PASS postgresql", output)
        self.assertIn("DATABASE_CONNECTION_ROUTE_PASS redis", output)

    def test_trusted_route_results_and_explicit_mode_are_required(self):
        for case in ("route_missing_result", "route_duplicate_result", "route_missing_failure", "route_missing_failure_test", "route_missing_capacity", "route_bad_mode"):
            with self.subTest(case=case):
                state, commands, _ = self.run_gate(case, 1, routes=True)
                self.assertEqual(state, {})
                if case == "route_bad_mode": self.assert_no_creation(commands)


    def test_browser_profile_rejects_extra_or_public_ports(self):
        for case in ("browser_public","browser_extra"):
            with self.subTest(case=case):
                state,commands,_=self.run_gate(case,1,browser=True)
                self.assertIn("outer",state)
                self.assertFalse(any("/fixture/api.test" in args for args in commands))
        state,_,_=self.run_gate("outer_unexpected_port",1)
        self.assertIn("outer",state)

    def test_retention_diagnostic_is_closed_and_has_no_browser_or_full_receipt(self):
        state, commands, _ = self.run_gate("retention_diagnostic_success", 0, mode="retention-failures-diagnostic", retention=True)
        self.assertEqual(state, {})
        self.assertIn("full_workflow_pass=0\ndiagnostic_pass=1", self.receipt)
        self.assertIn("gate_mode=retention-failures-diagnostic", self.receipt)
        runtime = next(args for args in commands if "/fixture/api.test" in args)
        self.assertEqual(runtime[runtime.index("-test.run")+1], "^(?:TestHostingDatabaseBackupRetentionFailuresCompiledRunner|TestHostingDatabaseBackupRetentionProtectionsCompiledRunner)$")
        self.assertFalse(any("browser-driver.mjs" in " ".join(args) or "bun" in args or "NEROCD_DATABASE_CONNECTION_BROWSER=1" in args for args in commands))
        self.assertEqual(sum("/fixture/api.test" in args for args in commands),1)
        for case in ("retention_diagnostic_missing_case","retention_diagnostic_missing_marker","retention_diagnostic_skipped","retention_diagnostic_unrelated","retention_diagnostic_duplicate"):
            with self.subTest(case=case):
                state, _, _ = self.run_gate(case,1,mode="retention-failures-diagnostic",retention=True)
                self.assertEqual(state,{})
                self.assertIn("full_workflow_pass=0\ndiagnostic_pass=0", self.receipt)
        _, commands, _ = self.run_gate("retention_diagnostic_success",1,mode="retention-failures-diagnostic")
        self.assert_no_creation(commands)

    def test_cutover_diagnostic_is_fixed_and_has_no_full_receipt(self):
        state, commands, output = self.run_gate("cutover_diagnostic_success", 0, browser=True, routes=True, storage=True, mode="storage-cutover-diagnostic")
        self.assertEqual(state, {})
        runtime = next(args for args in commands if "/fixture/api.test" in args)
        self.assertEqual(runtime[runtime.index("-test.run")+1], "^TestHostingApplicationDatabaseStorageFailuresCompiledRunner$/^postgresql$/^cutover$")
        self.assertFalse(any(args[:1] == ["acceptance/runtime-hosting-database-runner/browser-driver.mjs"] for args in self.all_commands))
        self.assertIn("full_workflow_pass=0\ndiagnostic_pass=1", self.receipt)
        self.assertIn("diagnostic-only", output)
        self.assertNotIn("private PostgreSQL and Redis gate passed", output)

    def test_cutover_diagnostic_requires_exact_case_and_cleanup(self):
        for case in ("cutover_diagnostic_missing_case", "cutover_diagnostic_missing_capacity", "cutover_diagnostic_foreign_result", "cutover_diagnostic_duplicate_case", "cutover_diagnostic_skipped_case"):
            with self.subTest(case=case):
                state, _, _ = self.run_gate(case, 1, browser=True, routes=True, storage=True, mode="storage-cutover-diagnostic")
                self.assertEqual(state, {})
                self.assertIn("full_workflow_pass=0\ndiagnostic_pass=0", self.receipt)

    def test_cutover_diagnostic_rejects_arbitrary_selectors_and_missing_profile(self):
        for mode in ("^Test.*", "storage-cutover-diagnostic;other", ""):
            with self.subTest(mode=mode):
                _, commands, _ = self.run_gate("cutover_diagnostic_success", 1, browser=True, routes=True, storage=True, mode=mode)
                self.assert_no_creation(commands)
        _, commands, _ = self.run_gate("cutover_diagnostic_success", 1, mode="storage-cutover-diagnostic")
        self.assert_no_creation(commands)

    def test_full_mode_does_not_accept_diagnostic_only_results(self):
        state, _, _ = self.run_gate("full_receipt_from_diagnostic", 1, browser=True, routes=True, storage=True)
        self.assertEqual(state, {})
        self.assertIn("full_workflow_pass=0\ndiagnostic_pass=0", self.receipt)

    def test_storage_profile_builds_distinct_root_fixture_and_preserves_ports(self):
        state, commands, output = self.run_gate("storage_success", 0, browser=True, routes=True, storage=True)
        self.assertEqual(state, {})
        create = next(args for args in commands if args[:2] == ["container", "create"])
        self.assertEqual(create[create.index("--publish")+1], "127.0.0.1::18080")
        self.assertEqual(sum(arg == "--publish" for arg in create), 1)
        build = next(args for args in commands if "/fixture/client/Dockerfile.http-storage" in args)
        self.assertIn("FIXTURE_OWNER="+OWNER, build)
        runtime = next(args for args in self.all_commands if args[:1] == ["acceptance/runtime-hosting-database-runner/browser-driver.mjs"])
        self.assertIn("NEROCD_DATABASE_CONNECTION_STORAGE=1", runtime)
        self.assertIn("NEROCD_DATABASE_STORAGE_HTTP_APP_IMAGE=127.0.0.1:35001/nerocd-http-app@sha256:"+"b"*64, runtime)
        self.assertIn("ApplicationDatabaseStorageCompiledRunner", runtime[runtime.index("-test.run")+1])
        self.assertIn("DATABASE_CONNECTION_STORAGE_PASS redis", output)

    def test_storage_profile_requires_all_results_and_distinct_digest(self):
        for case in ("storage_missing_test", "storage_missing_result", "storage_missing_browser", "storage_missing_capacity", "storage_missing_recovery_test", "storage_missing_recovery", "storage_missing_recovery_capacity", "storage_missing_recovery_browser", "storage_missing_recovery_ui", "storage_digest_collision", "storage_bad_mode"):
            with self.subTest(case=case):
                state, commands, _ = self.run_gate(case, 1, browser=True, routes=True, storage=True)
                self.assertEqual(state, {})
                if case == "storage_bad_mode": self.assert_no_creation(commands)

    def test_storage_profile_refuses_unqualified_browser_or_routes(self):
        for browser, routes in ((False,False),(True,False),(False,True)):
            with self.subTest(browser=browser,routes=routes):
                state, commands, _ = self.run_gate("storage_success", 1, browser=browser, routes=routes, storage=True)
                self.assertEqual(state, {})
                self.assert_no_creation(commands)

    def test_browser_prerequisite_and_driver_failures(self):
        for case,expected in (("browser_bad_mode",1),("browser_source_missing",1),("browser_build_failure",16),("browser_missing_result",1),("browser_driver_failure",39)):
            with self.subTest(case=case):
                state,commands,_=self.run_gate(case,expected,browser=True)
                self.assertEqual(state,{})
                if case in ("browser_bad_mode","browser_source_missing","browser_build_failure"):
                    self.assert_no_creation(commands)

    def test_harmless_complete_model(self):
        state, commands, output = self.run_gate("success", 0)
        self.assertEqual(state, {})
        self.assertIn("harmless runtime model passed", output)
        runtime_command = next(args for args in commands if "/fixture/api.test" in args)
        self.assertEqual(runtime_command[runtime_command.index("-test.run") + 1],
                         "^TestHosting(Database(CompiledRunner|CaptureCompiledRunner|UpgradeCompiledRunner)|ApplicationDatabaseCompiledRunner)$")
        self.assertIn("-test.timeout=40m", runtime_command)
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


class OuterPublicationTest(unittest.TestCase):
    def profile(self, browser=True):
        source=Path(__file__).with_name('gate.sh').read_text().split('# PYTHON SOURCE\n',1)[1].rsplit('\nPY\n',1)[0]
        function=next(node for node in ast.parse(source).body if isinstance(node,ast.FunctionDef) and node.name=='verified_outer')
        image='sha256:'+'a'*64
        value=dict(Id=IDENTITY,Name='/'+NAME,Image=image,
                   Config=dict(Labels={LABEL:OWNER},Cmd=['dockerd','--host=unix:///var/run/docker.sock']),
                   HostConfig=dict(Privileged=True,PortBindings={'18080/tcp':[dict(HostIp='127.0.0.1',HostPort='')]} if browser else {},NetworkMode='bridge'),
                   Mounts=[dict(Type='volume',Name=VOLUME,Driver='local',Destination='/var/lib/docker',RW=True)],
                   State=dict(Running=True),NetworkSettings=dict(Ports={
                       '18080/tcp':[dict(HostIp='127.0.0.1',HostPort='33077')],
                       '2375/tcp':None,'2376/tcp':None} if browser else {'2375/tcp':None,'2376/tcp':None}))
        namespace=dict(re=__import__('re'),Failure=RuntimeError,image=dict(Id=image,Config={}),LABEL=LABEL,OWNER=OWNER,OWNER_LABEL=OWNER,backup_mode="0",NAME=NAME,VOLUME=VOLUME,
                       browser_mode='1' if browser else '0',browser_origin='',inspect=lambda *_:value)
        exec(compile(ast.Module(body=[function],type_ignores=[]),'<real verified_outer>','exec'),namespace)
        return value,namespace

    def test_backup_profile_requires_exact_owner_and_resource_bounds(self):
        value,namespace=self.profile()
        namespace['backup_mode']='1'
        namespace['OWNER_LABEL']='20261025-pbr7319'
        value['Config']['Labels'][LABEL]='20261025-pbr7319'
        value['HostConfig'].update(NanoCpus=4000000000,Memory=4294967296,PidsLimit=1024)
        namespace['verified_outer'](IDENTITY)
        for field in ('NanoCpus','Memory','PidsLimit'):
            saved=value['HostConfig'][field]
            value['HostConfig'][field]=saved+1
            with self.assertRaises(RuntimeError):namespace['verified_outer'](IDENTITY)
            value['HostConfig'][field]=saved
        value['Config']['Labels'][LABEL]='foreign'
        with self.assertRaises(RuntimeError):namespace['verified_outer'](IDENTITY)

    def test_accepts_pinned_image_exposed_ports_during_preflight_and_cleanup(self):
        for inert in (None,[]):
            with self.subTest(exposure=inert):
                value,namespace=self.profile()
                value['NetworkSettings']['Ports'].update({'2375/tcp':inert,'2376/tcp':inert})
                self.assertIs(namespace['verified_outer'](IDENTITY),value)
                self.assertEqual(namespace['browser_origin'],'http://127.0.0.1:33077')
                value['State']['Running']=False
                self.assertIs(namespace['verified_outer'](IDENTITY),value)

    def test_no_browser_allows_only_inert_exposures(self):
        value,namespace=self.profile(browser=False)
        namespace['verified_outer'](IDENTITY)
        value['NetworkSettings']['Ports']['2375/tcp']=[]
        namespace['verified_outer'](IDENTITY)
        value['NetworkSettings']['Ports']['2375/tcp']=[dict(HostIp='127.0.0.1',HostPort='33078')]
        with self.assertRaises(RuntimeError):namespace['verified_outer'](IDENTITY)

    def test_rejects_every_extra_actual_publication_even_when_stopped(self):
        for key in ('2375/tcp','2376/tcp','18081/tcp','18080/udp'):
            for running in (True,False):
                with self.subTest(port=key,running=running):
                    value,namespace=self.profile()
                    value['State']['Running']=running
                    value['NetworkSettings']['Ports'][key]=[dict(HostIp='127.0.0.1',HostPort='33078')]
                    with self.assertRaises(RuntimeError):namespace['verified_outer'](IDENTITY)

    def test_rejects_malformed_or_missing_assigned_port_shapes(self):
        valid=dict(HostIp='127.0.0.1',HostPort='33077')
        malformed=[None,[],False,0,'',{'18080/tcp':None},{'18080/tcp':[]},
                   {'18080/tcp':{}},{'18080/tcp':[None]},{'18080/tcp':[valid,valid]},
                   {'18080/tcp':[dict(valid,HostIp='0.0.0.0')]},
                   {'18080/tcp':[dict(valid,HostPort=33077)]},
                   {'18080/tcp':[dict(valid,HostPort='65536')]},
                   {'18080/tcp':[dict(valid,Unexpected='field')]},
                   {'18080/tcp':[valid],'2375/tcp':False},
                   {'18080/tcp':[valid],'2375/tcp':{}},
                   {'18080/tcp':[valid],'bad-port':None}]
        for assigned in malformed:
            with self.subTest(assigned=assigned):
                value,namespace=self.profile()
                value['NetworkSettings']['Ports']=assigned
                with self.assertRaises(RuntimeError):namespace['verified_outer'](IDENTITY)
        value,namespace=self.profile()
        value['NetworkSettings']=[]
        with self.assertRaises(RuntimeError):namespace['verified_outer'](IDENTITY)

    def test_rejects_malformed_configured_publication(self):
        for ports in ([],False,'',{'18080/tcp':None},{'18080/tcp':{}},{'18080/tcp':[None]}):
            with self.subTest(ports=ports):
                value,namespace=self.profile()
                value['HostConfig']['PortBindings']=ports
                with self.assertRaises(RuntimeError):namespace['verified_outer'](IDENTITY)


class MySQLQualificationTest(unittest.TestCase):
    def test_database_ca_changes_only_workflow_leaf_validity(self):
        source = Path(__file__).with_name("gate.sh").read_text().split("# PYTHON SOURCE\n",1)[1].rsplit("\nPY\n",1)[0]
        function = next(node for node in ast.parse(source).body if isinstance(node,ast.FunctionDef) and node.name == "database_workflow_pebble_config")
        namespace = dict(json=json, Failure=RuntimeError)
        exec(compile(ast.Module(body=[function],type_ignores=[]),"gate.sh","exec"),namespace)
        path = Path(__file__).resolve().parents[2] / "acceptance/runtime-proxy-control/pebble.json"
        original = path.read_bytes()
        original_config = json.loads(original)
        effective = json.loads(namespace["database_workflow_pebble_config"](original))
        self.assertEqual(original_config["pebble"]["profiles"]["default"]["validityPeriod"],600)
        self.assertEqual(effective["pebble"]["profiles"]["default"]["validityPeriod"],86400)
        # Fixture Traefik requests one-hour certificates. The shared ten-minute
        # leaf is immediately inside that renewal window; workflow leaf is not.
        renderer = (Path(__file__).resolve().parents[2] / "internal/hostingproxy/docker.go").read_text()
        self.assertIn("--certificatesresolvers.managed.acme.certificatesduration=1",renderer)
        self.assertLess(original_config["pebble"]["profiles"]["default"]["validityPeriod"],3600)
        self.assertGreater(effective["pebble"]["profiles"]["default"]["validityPeriod"],3600+40*60)
        effective["pebble"]["profiles"]["default"]["validityPeriod"] = 600
        self.assertEqual(effective,original_config)
        self.assertEqual(path.read_bytes(),original)
        for value in (601,None,"600"):
            changed = json.loads(original)
            changed["pebble"]["profiles"]["default"]["validityPeriod"] = value
            with self.subTest(value=value),self.assertRaises(RuntimeError):namespace["database_workflow_pebble_config"](json.dumps(changed))

    def test_mysql_mode_keeps_old_backup_and_partial_profiles_closed(self):
        source = Path(__file__).with_name("gate.sh").read_text().split("# PYTHON SOURCE\n",1)[1].rsplit("\nPY\n",1)[0]
        main = next(node for node in ast.parse(source).body if isinstance(node,ast.FunctionDef) and node.name == "main")
        guard = main.body[2]
        self.assertIsInstance(guard,ast.If)
        statement = compile(ast.Module(body=main.body[1:3],type_ignores=[]),"gate.sh","exec")
        valid = dict(mysql_mode="1", backup_mode="0",schedule_mode="0",remote_mode="0",retention_mode="0",notifications_mode="0",storage_mode="1",routes_mode="1",browser_mode="1",gate_mode="full",Failure=RuntimeError)
        exec(statement,valid.copy())
        for field,value in (("mysql_mode","foreign"),("backup_mode","1"),("storage_mode","0"),("routes_mode","0"),("browser_mode","0"),("gate_mode","storage-cutover-diagnostic")):
            with self.subTest(field=field),self.assertRaises(RuntimeError):exec(statement,dict(valid,**{field:value}))

    def test_mysql_schedule_combination_is_explicit_and_postgresql_stays_private(self):
        source=Path(__file__).with_name("gate.sh").read_text().split("# PYTHON SOURCE\n",1)[1].rsplit("\nPY\n",1)[0]
        main=next(n for n in ast.parse(source).body if isinstance(n,ast.FunctionDef) and n.name=="main")
        guards=compile(ast.Module(body=main.body[1:12],type_ignores=[]),"gate.sh","exec")
        valid=dict(mysql_mode="1",backup_mode="1",schedule_mode="1",remote_mode="0",retention_mode="0",notifications_mode="0",storage_mode="1",routes_mode="1",browser_mode="1",gate_mode="full",Failure=RuntimeError)
        exec(guards,valid.copy())
        for key in ("schedule_mode","backup_mode","storage_mode","routes_mode","browser_mode"):
            with self.subTest(key=key),self.assertRaises(RuntimeError):exec(guards,dict(valid,**{key:"0"}))
        exec(guards,dict(valid,remote_mode="1"))
        for key in ("retention_mode","notifications_mode"):
            with self.subTest(key=key),self.assertRaises(RuntimeError):exec(guards,dict(valid,**{key:"1"}))
        pg=dict(valid,mysql_mode="0",storage_mode="0",routes_mode="0")
        exec(guards,pg)
        with self.assertRaises(RuntimeError):exec(guards,dict(pg,routes_mode="1"))

    def test_mysql_notifications_combination_matches_real_gate_guards(self):
        source=Path(__file__).with_name("gate.sh").read_text().split("# PYTHON SOURCE\n",1)[1].rsplit("\nPY\n",1)[0]
        main=next(n for n in ast.parse(source).body if isinstance(n,ast.FunctionDef) and n.name=="main")
        guards=compile(ast.Module(body=main.body[1:12],type_ignores=[]),"gate.sh","exec")
        valid=dict(mysql_mode="1",backup_mode="1",schedule_mode="0",remote_mode="1",retention_mode="0",notifications_mode="1",storage_mode="1",routes_mode="1",browser_mode="1",gate_mode="full",Failure=RuntimeError)
        exec(guards,valid.copy())
        for key,value in (("remote_mode","0"),("backup_mode","0"),("storage_mode","0"),("routes_mode","0"),("browser_mode","0"),("schedule_mode","1"),("retention_mode","1")):
            with self.subTest(key=key),self.assertRaises(RuntimeError):exec(guards,dict(valid,**{key:value}))

    def test_real_output_verifier_requires_every_workflow_proof(self):
        source = Path(__file__).with_name("gate.sh").read_text().split("# PYTHON SOURCE\n",1)[1].rsplit("\nPY\n",1)[0]
        function = next(node for node in ast.parse(source).body if isinstance(node,ast.FunctionDef) and node.name == "verify_mysql_runtime_output")
        namespace = dict(re=__import__("re"), Failure=RuntimeError)
        exec(compile(ast.Module(body=[function],type_ignores=[]),"gate.sh","exec"),namespace)
        verify = namespace["verify_mysql_runtime_output"]
        markers = ["DATABASE_CONNECTION_STORAGE_PASS mysql", "DATABASE_CONNECTION_STORAGE_BROWSER_PASS mysql", "DATABASE_CONNECTION_STORAGE_CAPACITY_PASS mysql success", "DATABASE_BACKUP_RESTORE_APP_PASS mysql", "DATABASE_BACKUP_BROWSER_PASS mysql"]
        for case in ("readiness", "cutover"):
            markers += ["DATABASE_CONNECTION_STORAGE_RECOVERY_PASS mysql " + case, "DATABASE_CONNECTION_STORAGE_CAPACITY_PASS mysql " + case]
        names = ["TestHostingApplicationDatabaseStorageCompiledRunner", "TestHostingApplicationDatabaseStorageFailuresCompiledRunner"]
        output = "\n".join(markers + ["--- PASS: " + name + " (1.00s)" for name in names])
        verify(output)
        for marker in markers:
            with self.subTest(missing=marker), self.assertRaises(RuntimeError): verify(output.replace(marker,""))
            with self.subTest(repeated=marker), self.assertRaises(RuntimeError): verify(output+"\n"+marker)
        for tail in ("--- SKIP: mysql (0.00s)", "--- FAIL: mysql (0.00s)", "DATABASE_BACKUP_RESTORE_APP_PASS postgresql"):
            with self.subTest(tail=tail), self.assertRaises(RuntimeError): verify(output+"\n"+tail)
        for name in names:
            with self.subTest(missing=name), self.assertRaises(RuntimeError): verify(output.replace("--- PASS: " + name, "--- SKIP: " + name))


class BackupScheduleSelectionTest(unittest.TestCase):
    def functions(self):
        source = Path(__file__).with_name("gate.sh").read_text().split("# PYTHON SOURCE\n",1)[1].rsplit("\nPY\n",1)[0]
        names = {"backup_runtime_profile", "verify_backup_runtime_output", "retention_failure_diagnostic_profile", "verify_retention_failure_diagnostic_output"}
        definitions = [node for node in ast.parse(source).body if isinstance(node,ast.FunctionDef) and node.name in names]
        namespace = dict(re=__import__("re"), Failure=RuntimeError)
        exec(compile(ast.Module(body=definitions,type_ignores=[]),"gate.sh","exec"),namespace)
        return namespace

    def test_schedule_selection_and_required_proofs(self):
        ns = self.functions()
        pattern,names,markers = ns["backup_runtime_profile"]("1")
        self.assertEqual(len(names),3)
        self.assertIn("TestHostingDatabaseBackupScheduleCompiledRunner",names)
        for name in names:self.assertRegex(name,pattern)
        self.assertNotRegex("TestHostingDatabaseBackupScheduleCompiledRunnerForeign",pattern)
        output = "\n".join(["--- PASS: "+name+" (0.01s)" for name in names]+markers)
        ns["verify_backup_runtime_output"](output,"1")
        for marker in markers:
            with self.subTest(marker=marker),self.assertRaises(RuntimeError):
                ns["verify_backup_runtime_output"](output.replace(marker,""),"1")
        for suffix in ("\n--- SKIP: foreign (0.01s)","\n--- FAIL: foreign (0.01s)","\n"+markers[-1]):
            with self.assertRaises(RuntimeError):ns["verify_backup_runtime_output"](output+suffix,"1")
        with self.assertRaises(RuntimeError):ns["backup_runtime_profile"]("foreign")
        base,base_names,base_markers = ns["backup_runtime_profile"]("0")
        self.assertEqual(len(base_names),2)
        self.assertNotRegex("TestHostingDatabaseBackupScheduleCompiledRunner",base)
        self.assertNotIn("DATABASE_BACKUP_GUEST_DUMP_TIMEOUT_PASS postgresql",base_markers)


    def test_mysql_schedule_selects_only_schedule_and_rejects_partial_proof(self):
        ns = self.functions()
        pattern,names,markers = ns["backup_runtime_profile"]("1", mysql="1")
        self.assertEqual(names,["TestHostingDatabaseBackupScheduleCompiledRunner"])
        self.assertNotRegex("TestHostingDatabaseBackupCompiledRunner",pattern)
        self.assertNotRegex("TestHostingApplicationDatabaseStorageCompiledRunner",pattern)
        output="\n".join(["--- PASS: "+names[0]+" (0.01s)"]+markers)
        ns["verify_backup_runtime_output"](output,"1",mysql="1")
        for marker in markers:
            with self.subTest(marker=marker),self.assertRaises(RuntimeError):ns["verify_backup_runtime_output"](output.replace(marker,""),"1",mysql="1")
        for suffix in ("\n--- SKIP: mysql (0.01s)","\n--- FAIL: mysql (0.01s)","\n"+markers[-1]):
            with self.assertRaises(RuntimeError):ns["verify_backup_runtime_output"](output+suffix,"1",mysql="1")
        for arguments in (("0","0","0","0","1"),("1","0","1","0","1"),("1","0","0","1","1")):
            with self.assertRaises(RuntimeError):ns["backup_runtime_profile"](*arguments)

    def test_mysql_remote_selects_only_remote_and_requires_all_proofs(self):
        ns = self.functions()
        pattern,names,markers = ns["backup_runtime_profile"]("1","1",mysql="1")
        self.assertEqual(names,["TestHostingDatabaseBackupRemoteCompiledRunner"])
        self.assertNotRegex("TestHostingDatabaseBackupScheduleCompiledRunner",pattern)
        self.assertNotRegex("TestHostingDatabaseBackupCompiledRunner",pattern)
        output="\n".join(["--- PASS: "+names[0]+" (0.01s)"]+markers)
        ns["verify_backup_runtime_output"](output,"1","1",mysql="1")
        for marker in markers:
            with self.subTest(marker=marker),self.assertRaises(RuntimeError):ns["verify_backup_runtime_output"](output.replace(marker,""),"1","1",mysql="1")
        for remote,retention,notifications in (("1","1","0"),("1","0","1")):
            with self.assertRaises(RuntimeError):ns["backup_runtime_profile"]("1",remote,retention,notifications,mysql="1")

    def test_mysql_notifications_select_only_existing_notification_workflow(self):
        ns=self.functions()
        pattern,names,markers=ns["backup_runtime_profile"]("0","1","0","1",mysql="1")
        self.assertEqual(names,["TestHostingBackupNotificationsCompiledRunner"])
        self.assertRegex(names[0],pattern)
        self.assertNotRegex("TestHostingDatabaseBackupRemoteCompiledRunner",pattern)
        self.assertEqual(len(markers),7)
        self.assertFalse(any("postgresql" in m or "local_restore" in m for m in markers))
        output="\n".join(["--- PASS: "+names[0]+" (0.01s)"]+markers)
        ns["verify_backup_runtime_output"](output,"0","1","0","1",mysql="1")
        for marker in markers:
            with self.subTest(marker=marker),self.assertRaises(RuntimeError):ns["verify_backup_runtime_output"](output.replace(marker,""),"0","1","0","1",mysql="1")
        with self.assertRaises(RuntimeError):ns["verify_backup_runtime_output"](output.replace("--- PASS:","--- SKIP:"),"0","1","0","1",mysql="1")

    def test_remote_profile_requires_all_real_proofs_and_never_accepts_skips(self):
        ns = self.functions()
        pattern,names,markers = ns["backup_runtime_profile"]("1","1")
        self.assertEqual(len(names),4)
        self.assertRegex("TestHostingDatabaseBackupRemoteCompiledRunner",pattern)
        self.assertNotRegex("TestHostingDatabaseBackupRemoteCompiledRunnerForeign",pattern)
        remote = [v for v in markers if "_REMOTE_" in v]
        self.assertEqual(len(remote),4)
        output = "\n".join(["--- PASS: "+name+" (0.01s)" for name in names]+markers)
        ns["verify_backup_runtime_output"](output,"1","1")
        for marker in remote:
            with self.subTest(marker=marker),self.assertRaises(RuntimeError):
                ns["verify_backup_runtime_output"](output.replace(marker,""),"1","1")
        for suffix in ("\n--- SKIP: TestHostingDatabaseBackupRemoteCompiledRunner (0.01s)","\n"+remote[-1]):
            with self.assertRaises(RuntimeError):ns["verify_backup_runtime_output"](output+suffix,"1","1")
        with self.assertRaises(RuntimeError):ns["backup_runtime_profile"]("1","foreign")
        _,_,local = ns["backup_runtime_profile"]("1")
        self.assertFalse(any("_REMOTE_" in v for v in local))

    def test_retention_requires_remote_and_every_physical_group(self):
        ns=self.functions()
        pattern,names,markers=ns['backup_runtime_profile']('1','1','1')
        self.assertEqual(len(names),9)
        self.assertEqual(len([v for v in markers if '_RETENTION_' in v]),13)
        output='\n'.join(['--- PASS: '+n+' (0.01s)' for n in names]+markers)
        ns['verify_backup_runtime_output'](output,'1','1','1')
        for marker in markers:
            with self.subTest(marker=marker),self.assertRaises(RuntimeError):ns['verify_backup_runtime_output'](output.replace(marker,''),'1','1','1')
        for remote in ('0','foreign'):
            with self.assertRaises(RuntimeError):ns['backup_runtime_profile']('1',remote,'1')
        with self.assertRaises(RuntimeError):ns['verify_backup_runtime_output'](output+'\n--- SKIP: retention (0.1s)','1','1','1')
        _,local_names,local_markers=ns['backup_runtime_profile']('1','1')
        self.assertFalse(any('Retention' in v for v in local_names))
        self.assertFalse(any('_RETENTION_' in v for v in local_markers))

    def test_notifications_require_remote_and_all_actual_outcomes(self):
        ns = self.functions()
        _, old_names, old_markers = ns["backup_runtime_profile"]("1", "1", "1")
        pattern, names, markers = ns["backup_runtime_profile"]("1", "1", "1", "1")
        self.assertEqual(names, old_names + ["TestHostingBackupNotificationsCompiledRunner"])
        self.assertEqual(markers[:len(old_markers)], old_markers)
        self.assertRegex("TestHostingBackupNotificationsCompiledRunner", pattern)
        self.assertNotRegex("TestHostingBackupNotificationsCompiledRunnerForeign", pattern)
        output = "\n".join(["--- PASS: " + name + " (0.01s)" for name in names] + markers)
        ns["verify_backup_runtime_output"](output, "1", "1", "1", "1")
        for marker in markers[len(old_markers):]:
            with self.subTest(marker=marker), self.assertRaises(RuntimeError):
                ns["verify_backup_runtime_output"](output.replace(marker, ""), "1", "1", "1", "1")
            with self.subTest(duplicate=marker), self.assertRaises(RuntimeError):
                ns["verify_backup_runtime_output"](output + "\n" + marker, "1", "1", "1", "1")
        for remote, notifications in (("0", "1"), ("1", "foreign")):
            with self.assertRaises(RuntimeError):
                ns["backup_runtime_profile"]("0", remote, "0", notifications)
        with self.assertRaises(RuntimeError):
            ns["verify_backup_runtime_output"](output.replace("--- PASS: TestHostingBackupNotificationsCompiledRunner", "--- SKIP: TestHostingBackupNotificationsCompiledRunner"), "1", "1", "1", "1")

    def test_retention_diagnostic_requires_exact_profile_and_every_case(self):
        ns=self.functions()
        flags=("1","1","1","0","0","0","0")
        pattern,names,markers=ns['retention_failure_diagnostic_profile'](*flags)
        self.assertEqual(len(names),2)
        self.assertEqual(len(markers),5)
        for i in range(len(flags)):
            changed=list(flags);changed[i]="0" if flags[i]=="1" else "1"
            with self.assertRaises(RuntimeError):ns['retention_failure_diagnostic_profile'](*changed)
        entries=names+[names[0]+"/"+kind for kind in ("delete_denied","conditional_unsupported","authorization_revoked")]
        output="\n".join(["--- PASS: "+entry+" (0.01s)" for entry in entries]+markers)
        ns['verify_retention_failure_diagnostic_output'](output)
        for entry in entries:
            with self.assertRaises(RuntimeError):ns['verify_retention_failure_diagnostic_output'](output.replace("--- PASS: "+entry+" (0.01s)",""))
        for marker in markers:
            with self.assertRaises(RuntimeError):ns['verify_retention_failure_diagnostic_output'](output.replace(marker,""))
            with self.assertRaises(RuntimeError):ns['verify_retention_failure_diagnostic_output'](output+"\n"+marker)
        for suffix in ("\n--- SKIP: selected (0.01s)","\n--- FAIL: selected (0.01s)","\n--- PASS: unrelated (0.01s)","\nDATABASE_BACKUP_REMOTE_PASS postgresql"):
            with self.assertRaises(RuntimeError):ns['verify_retention_failure_diagnostic_output'](output+suffix)
        with self.assertRaises(RuntimeError):ns['verify_backup_runtime_output'](output,"1","1","1")
        _,full_names,full_markers=ns['backup_runtime_profile']("1","1","1")
        full_output="\n".join(["--- PASS: "+entry+" (0.01s)" for entry in full_names]+full_markers)
        with self.assertRaises(RuntimeError):ns['verify_retention_failure_diagnostic_output'](full_output)
        self.assertRegex(names[0],pattern)
        self.assertNotRegex(names[0]+"Foreign",pattern)


DRIVER_FIXTURE = r'''
import json, pathlib, signal, subprocess, sys, time
root=pathlib.Path(sys.argv[1]);case=(root/'case').read_text()
child=subprocess.Popen([sys.executable,'-c','import time;time.sleep(120)',str(root)])
with (root/'pids').open('a') as out: out.write(str(__import__('os').getpid())+'\n'+str(child.pid)+'\n')
def stop(*_):
 child.terminate();child.wait(timeout=2);sys.exit(1)
signal.signal(signal.SIGTERM,stop)
try:
 profiles = ([('backup_notifications','postgresql')] if case.startswith('backup_notifications') else [])+([('backup_remote','postgresql')] if case.startswith(('backup_remote','backup_retention','backup_notifications')) else [])+([('backup_retention','postgresql'),('backup_retention_recovery','postgresql')] if case.startswith('backup_retention') else [])+[('backup','postgresql')]+([('backup_schedule','postgresql')] if case.startswith('backup_schedule') else []) if case.startswith('backup_') else [('base','postgresql'),('base','redis')]+([('storage','postgresql'),('storage','redis'),('storage_resume','postgresql'),('storage_complete','redis')] if case in ('storage_success','storage_duplicate','storage_unknown_profile') else [])
 if case.startswith('mysql_'):profiles=[('backup_notifications','mysql')] if case.startswith('mysql_notifications') else [('backup_remote','mysql')] if case.startswith('mysql_remote') else [('backup_schedule','mysql')] if case.startswith('mysql_schedule') else [('storage','mysql'),('backup','mysql')]
 for profile,engine in profiles:
  meta=dict(origin='http://127.0.0.1:35002',engine=engine,project_id='project',environment='environment',application_resource_id='application',application_name='application',database_resource_id='database',foreign_resource_id='foreign',control_path='/api/v1/fixture/database-connection/'+'a'*32)
  if profile!='base':meta['profile']=profile
  if profile.startswith('backup'):meta['control_path']='/api/v1/fixture/database-backup/'+'a'*32
  if profile=='backup_schedule' and case in ('backup_schedule_unknown','mysql_schedule_unknown'):meta['profile']='foreign'
  if profile=='backup_remote' and case=='backup_remote_unknown':meta['profile']='foreign'
  if profile=='backup_notifications':meta.update(origin='https://127.0.0.1:35002',tls_spki='AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=')
  if profile=='backup_notifications' and case in ('backup_notifications_bad_pin','mysql_notifications_bad_pin'):meta['tls_spki']='bad'
  if profile=='backup_notifications' and case in ('backup_notifications_http','mysql_notifications_http'):meta['origin']='http://127.0.0.1:35002'
  if profile=='backup_notifications' and case=='backup_notifications_unknown':meta['profile']='foreign'
  if profile=='backup_retention' and case=='backup_retention_unknown':meta['profile']='foreign'
  if profile=='backup_retention_recovery' and case=='backup_retention_recovery_unknown':meta['profile']='foreign'
  if profile=='storage' and case=='storage_unknown_profile':meta['profile']='foreign'
  if case=='foreign_origin':meta['origin']='http://127.0.0.1:35003'
  if case=='metadata_secret':meta['password']='SENSITIVE-CANARY'
  if profile=='backup' and case=='mysql_unknown':meta['profile']='backup_remote'
  print(('DATABASE_BACKUP_BROWSER_READY ' if profile.startswith('backup') else 'DATABASE_CONNECTION_BROWSER_READY ')+json.dumps(meta),flush=True)
  if case in ('mysql_duplicate','mysql_schedule_duplicate'):print(('DATABASE_BACKUP_BROWSER_READY ' if profile.startswith('backup') else 'DATABASE_CONNECTION_BROWSER_READY ')+json.dumps(meta),flush=True)
  if case=='duplicate':print('DATABASE_CONNECTION_BROWSER_READY '+json.dumps(meta),flush=True)
  if profile=='storage' and case=='storage_duplicate':print('DATABASE_CONNECTION_BROWSER_READY '+json.dumps(meta),flush=True)
  while not (root/('done-'+profile+'-'+engine)).exists():time.sleep(.01)
  if case=='mysql_missing':break
  if case in ('missing_second','backup_schedule_missing','backup_remote_missing','backup_notifications_missing') or case=='backup_retention_missing' and profile=='backup_remote' or case=='backup_retention_recovery_missing' and profile=='backup_retention':break
finally:
 child.terminate();child.wait(timeout=2)
'''

DRIVER_BROWSER = r'''
import json, os, pathlib, signal, subprocess, sys, time
root=pathlib.Path(__file__).resolve().parent.parent
case=(root/'case').read_text();meta=json.loads(os.environ['NEROCD_DATABASE_CONNECTION_BROWSER_META'])
assert 'NEROCD_TEST_DATABASE_URL' not in os.environ
expected_spec = 'backup-notifications-live.spec.ts' if meta.get('profile')=='backup_notifications' else 'database-backup-retention-live.spec.ts' if meta.get('profile') in ('backup_retention','backup_retention_recovery') else 'database-remote-backups-live.spec.ts' if meta.get('profile')=='backup_remote' else 'database-backup-schedules-live.spec.ts' if meta.get('profile')=='backup_schedule' else 'database-backups-live.spec.ts' if meta.get('profile')=='backup' else 'database-connection-live.spec.ts'
assert expected_spec in sys.argv
assert os.environ['PLAYWRIGHT_BASE_URL']==meta['origin']
if meta.get('profile')=='backup_notifications':
 assert 'playwright.notifications.config.ts' in sys.argv
 assert os.environ['NEROCD_NOTIFICATION_BROWSER_SPKI']==meta['tls_spki']
else:assert 'NEROCD_NOTIFICATION_BROWSER_SPKI' not in os.environ
child=subprocess.Popen([sys.executable,'-c','import time;time.sleep(120)',str(root)],start_new_session=case=='detached_cancel')
with (root/'pids').open('a') as out:out.write(str(os.getpid())+'\n'+str(child.pid)+'\n')
if case=='detached_cancel':(root/'detached-pid').write_text(str(child.pid))
def stop(*_):
 if case=='detached_cancel':time.sleep(3)
 child.terminate();child.wait(timeout=2);sys.exit(1)
signal.signal(signal.SIGTERM,stop)
if case in ('cancel','detached_cancel'):
 (root/'blocked').touch()
 while True:time.sleep(.01)
child.terminate();child.wait(timeout=2)
if case=='browser_failure':sys.exit(9)
(root/('done-'+meta.get('profile','base')+'-'+meta['engine'])).touch()
'''

class BrowserDriverTest(unittest.TestCase):
    def cleanup_stub(self, pid, root):
        def owned():
            state=subprocess.run(['ps','-p',str(pid),'-o','command='],capture_output=True,text=True,check=False)
            if state.returncode==1 and not state.stdout.strip():return False
            self.assertEqual(state.returncode,0)
            self.assertIn(str(root),state.stdout,'refusing to signal a PID without the private stub identity')
            return True
        if not owned():return
        try:os.kill(pid,signal.SIGTERM)
        except ProcessLookupError:return
        deadline=time.monotonic()+2
        while owned() and time.monotonic()<deadline:time.sleep(.02)
        if owned():
            try:os.kill(pid,signal.SIGKILL)
            except ProcessLookupError:return
            deadline=time.monotonic()+2
            while owned() and time.monotonic()<deadline:time.sleep(.02)
        self.assertFalse(owned(),'private browser-driver stub did not exit')

    def run_driver(self, case, expected, cancel=False):
        node=shutil.which('node')
        self.assertIsNotNone(node)
        driver=Path(__file__).with_name('browser-driver.mjs').resolve()
        with tempfile.TemporaryDirectory(prefix='nerocd-browser-driver-test-') as temporary:
            root=Path(temporary);(root/'case').write_text(case)
            fixture=root/'fixture.py';fixture.write_text(DRIVER_FIXTURE)
            (root/'bin').mkdir();browser=root/'bin/bun'
            browser.write_text('#!'+sys.executable+'\n'+DRIVER_BROWSER);browser.chmod(0o700)
            environment=dict(os.environ,PATH=str(root/'bin')+os.pathsep+os.environ['PATH'],NEROCD_TEST_DATABASE_URL='SENSITIVE-CANARY')
            environment['NEROCD_DATABASE_MYSQL_RUNTIME']='1' if case.startswith('mysql_') else '0'
            environment['NEROCD_DATABASE_CONNECTION_STORAGE']='1' if case.startswith(('storage_','mysql_')) else '0'
            environment['NEROCD_DATABASE_BACKUP_RUNTIME']='1' if case.startswith(('backup_','mysql_schedule','mysql_remote','mysql_notifications')) else '0'
            environment['NEROCD_DATABASE_BACKUP_SCHEDULE_RUNTIME']='1' if case.startswith(('backup_schedule','mysql_schedule','mysql_remote')) else '0'
            environment['NEROCD_DATABASE_BACKUP_REMOTE_RUNTIME']='1' if case.startswith(('backup_remote','backup_retention','backup_notifications','mysql_remote','mysql_notifications')) else '0'
            environment['NEROCD_DATABASE_BACKUP_NOTIFICATIONS_RUNTIME']='1' if case.startswith(('backup_notifications','mysql_notifications')) else '0'
            environment['NEROCD_DATABASE_BACKUP_RETENTION_RUNTIME']='1' if case.startswith('backup_retention') else '0'
            process=subprocess.Popen([node,str(driver),'http://127.0.0.1:35002',str(root/'evidence'),'--',sys.executable,str(fixture),str(root)],cwd=Path(__file__).resolve().parents[2],env=environment,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True,start_new_session=True)
            pids=[]
            try:
                if cancel:
                    deadline=time.monotonic()+5
                    while not (root/'blocked').exists() and time.monotonic()<deadline:time.sleep(.01)
                    self.assertTrue((root/'blocked').exists())
                    process.send_signal(signal.SIGTERM)
                output,_=process.communicate(timeout=12)
                self.assertEqual(process.returncode,expected,output)
                self.assertNotIn('SENSITIVE-CANARY',output)
                if expected==0:
                    if case.startswith('mysql_notifications'):
                        self.assertEqual(output.count('DATABASE_BACKUP_NOTIFICATIONS_BROWSER_PASS mysql'),1)
                        self.assertNotIn('DATABASE_BACKUP_REMOTE_BROWSER_PASS mysql',output)
                    elif case.startswith('mysql_remote'):
                        self.assertEqual(output.count('DATABASE_BACKUP_REMOTE_BROWSER_PASS mysql'),1)
                        self.assertNotIn('DATABASE_BACKUP_BROWSER_PASS mysql',output)
                    elif case.startswith('mysql_schedule'):
                        self.assertEqual(output.count('DATABASE_BACKUP_SCHEDULE_BROWSER_PASS mysql'),1)
                        self.assertNotIn('DATABASE_CONNECTION_STORAGE_BROWSER_PASS',output)
                    elif case.startswith('mysql_'):
                        self.assertEqual(output.count('DATABASE_CONNECTION_STORAGE_BROWSER_PASS mysql'),1)
                        self.assertEqual(output.count('DATABASE_BACKUP_BROWSER_PASS mysql'),1)
                    elif case.startswith('backup_'):
                        self.assertEqual(output.count('DATABASE_BACKUP_BROWSER_PASS postgresql'),1)
                        self.assertEqual(output.count('DATABASE_BACKUP_SCHEDULE_BROWSER_PASS postgresql'),1 if case.startswith('backup_schedule') else 0)
                        self.assertEqual(output.count('DATABASE_BACKUP_NOTIFICATIONS_BROWSER_PASS postgresql'),1 if case.startswith('backup_notifications') else 0)
                        self.assertEqual(output.count('DATABASE_BACKUP_REMOTE_BROWSER_PASS postgresql'),1 if case.startswith(('backup_remote','backup_retention','backup_notifications')) else 0)
                        self.assertEqual(output.count('DATABASE_BACKUP_RETENTION_BROWSER_PASS postgresql'),1 if case.startswith('backup_retention') else 0)
                        self.assertEqual(output.count('DATABASE_BACKUP_RETENTION_RECOVERY_BROWSER_PASS postgresql'),1 if case.startswith('backup_retention') else 0)
                    else:
                        self.assertEqual(output.count('DATABASE_CONNECTION_BROWSER_PASS postgresql'),1)
                        self.assertEqual(output.count('DATABASE_CONNECTION_BROWSER_PASS redis'),1)
                    if case=='storage_success':
                        self.assertEqual(output.count('DATABASE_CONNECTION_STORAGE_BROWSER_PASS postgresql'),1)
                        self.assertEqual(output.count('DATABASE_CONNECTION_STORAGE_BROWSER_PASS redis'),1)
                        self.assertEqual(output.count('DATABASE_CONNECTION_STORAGE_RECOVERY_BROWSER_PASS postgresql resume_predecessor'),1)
                        self.assertEqual(output.count('DATABASE_CONNECTION_STORAGE_RECOVERY_BROWSER_PASS redis complete_replacement'),1)
                detached=int((root/'detached-pid').read_text()) if case=='detached_cancel' else None
                if detached is not None:
                    self.assertIn('database connection browser cleanup unresolved: forced runner termination cannot prove detached browser exit',output)
                    self.assertIn('database connection browser driver failed',output)
                    os.kill(detached,0)
                else:
                    self.assertNotIn('cleanup unresolved',output)
                pids=[int(pid) for pid in (root/'pids').read_text().splitlines()]
                for pid in pids:
                    if pid==detached:continue
                    with self.assertRaises(ProcessLookupError,msg=output):os.kill(pid,0)
            finally:
                if process.poll() is None:
                    process.send_signal(signal.SIGTERM)
                    try:process.communicate(timeout=5)
                    except subprocess.TimeoutExpired:
                        os.killpg(process.pid,signal.SIGKILL);process.communicate(timeout=2)
                # Verify the private path before signaling, including on failed assertions.
                if (root/'pids').exists():
                    cleanup_errors=[]
                    for pid in [int(pid) for pid in (root/'pids').read_text().splitlines()]:
                        try:self.cleanup_stub(pid,root)
                        except (AssertionError,OSError) as error:cleanup_errors.append(error)
                    self.assertEqual(cleanup_errors,[],'private browser-driver stub cleanup failed')

    def test_backup_and_schedule_select_distinct_live_specs(self):
        self.run_driver('backup_success',0)
        self.run_driver('backup_schedule_success',0)
        self.run_driver('backup_remote_success',0)
        self.run_driver('backup_retention_success',0)
        self.run_driver('backup_notifications_success',0)
        for case in ('backup_notifications_bad_pin','backup_notifications_http','backup_notifications_missing','backup_notifications_unknown','backup_schedule_missing','backup_schedule_unknown','backup_remote_missing','backup_remote_unknown','backup_retention_missing','backup_retention_unknown','backup_retention_recovery_missing','backup_retention_recovery_unknown'):
            with self.subTest(case=case):self.run_driver(case,1)

    def test_mysql_notifications_reuses_existing_tls_browser(self):
        self.run_driver('mysql_notifications_success',0)
        self.run_driver('mysql_notifications_bad_pin',1)
        self.run_driver('mysql_notifications_http',1)

    def test_mysql_remote_reuses_single_existing_browser_flow(self):
        self.run_driver('mysql_remote_success',0)

    def test_mysql_schedule_reuses_single_existing_browser_flow(self):
        self.run_driver('mysql_schedule_success',0)
        for case in ('mysql_schedule_duplicate','mysql_schedule_unknown'):
            self.run_driver(case,1)

    def test_mysql_storage_then_backup_reuses_existing_browser_driver(self):
        self.run_driver('mysql_success',0)
        for case in ('mysql_missing','mysql_duplicate','mysql_unknown'):
            with self.subTest(case=case):self.run_driver(case,1)

    def test_successful_two_engine_driver(self):
        self.run_driver('success',0)

    def test_storage_profile_requires_both_distinct_engine_browser_runs(self):
        self.run_driver('storage_success',0)
        for case in ('storage_missing','storage_duplicate','storage_unknown_profile'):
            with self.subTest(case=case): self.run_driver(case,1)

    def test_rejects_metadata_and_missing_results(self):
        for case in ('foreign_origin','metadata_secret','duplicate','missing_second','browser_failure'):
            with self.subTest(case=case):self.run_driver(case,1)

    def test_cancellation_joins_fixture_browser_and_children(self):
        self.run_driver('cancel',1,cancel=True)

    def test_forced_runner_exit_reports_detached_browser_cleanup_unresolved(self):
        self.run_driver('detached_cancel',1,cancel=True)


if __name__ == "__main__":
    unittest.main()
