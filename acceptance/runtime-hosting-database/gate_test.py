"""Exercise the actual gate's signal traps using harmless executable stubs."""

import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest


STUB = r'''
import json, os, pathlib, sys, time
root = pathlib.Path(os.environ["DATABASE_GATE_TEST_ROOT"])
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
    sys.exit(int(os.environ["DATABASE_GATE_TEST_BUILD_EXIT"]))
else:
    raise RuntimeError("unexpected executable")
'''


class GateExitTest(unittest.TestCase):
    def run_gate(self, delivered_signal, expected_exit):
        gate = Path(os.environ.get("DATABASE_GATE_UNDER_TEST", Path(__file__).with_name("gate.sh"))).resolve()
        with tempfile.TemporaryDirectory(prefix="nerocd-database-gate-test-") as temporary:
            root = Path(temporary)
            (root / "internal/runnerruntime").mkdir(parents=True)
            (root / "internal/runnerruntime/hosting_database_runtime_integration_test.go").touch()
            binary = root / "bin"
            binary.mkdir()
            for name in ("git", "go", "docker", "mktemp"):
                executable = binary / name
                executable.write_text("#!" + sys.executable + "\n" + STUB)
                executable.chmod(0o700)
            environment = os.environ.copy()
            environment.update(
                PATH=str(binary) + os.pathsep + environment["PATH"],
                DATABASE_GATE_TEST_ROOT=str(root),
                DATABASE_GATE_TEST_BUILD_EXIT="0" if delivered_signal else str(expected_exit),
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


if __name__ == "__main__":
    unittest.main()
