"""Offline packaging tests. No Docker or external fixture is used."""

import base64
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import signal
import struct
import subprocess
import sys
import tarfile
import tempfile
import time
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("packager", ROOT / "scripts/build-storage-archive-helper.py")
packager = importlib.util.module_from_spec(spec)
spec.loader.exec_module(packager)


def static_elf():
    data = bytearray(120)
    data[:7] = b"\x7fELF\x02\x01\x01"
    struct.pack_into("<HH", data, 16, 2, 183)
    struct.pack_into("<Q", data, 32, 64)
    struct.pack_into("<HH", data, 54, 56, 1)
    struct.pack_into("<I", data, 64, 1)
    return bytes(data)


class PackagingTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.repo = self.root / "repository"
        self.repo.mkdir()
        self.git("init", "-b", "fixture")
        self.git("config", "user.name", "Archive fixture")
        self.git("config", "user.email", "archive@example.invalid")
        self.write("go.mod", "module nerocd\n\ngo 1.25.0\n\ntoolchain go1.26.10\n")
        self.write("go.sum", "")
        self.write("cmd/nerocd-storage-archive/main.go", "package main\nfunc main() {}\n")
        self.write("internal/storagearchive/archive.go", "package storagearchive\n")
        self.write("Dockerfile.storage-archive", (ROOT / "Dockerfile.storage-archive").read_text())
        self.commit()
        self.output = self.root / "prepared"
        self.log = self.root / "compiler.json"
        self.go = self.root / "go"
        self.go.write_text(f"#!{sys.executable}\n" + """
import base64, json, os, pathlib, sys
if sys.argv[1:] == ['version']:
    print('go version go1.26.10 fixture/host')
else:
    source = pathlib.Path.cwd()
    assert not (source / 'cmd/nerocd-storage-archive/untracked.go').exists()
    assert not (source / 'unrelated-secret').exists()
    pathlib.Path(LOG).write_text(json.dumps({'args':sys.argv[1:], 'env':dict(os.environ)}))
    pathlib.Path(sys.argv[sys.argv.index('-o')+1]).write_bytes(base64.b64decode(ELF))
""".replace("LOG", repr(str(self.log))).replace("ELF", repr(base64.b64encode(static_elf()).decode())))
        self.go.chmod(0o755)

    def git(self, *args):
        return subprocess.check_output(["/usr/bin/git", "-C", str(self.repo), *args],
                                       stderr=subprocess.DEVNULL, text=True).strip()

    def write(self, name, content):
        file = self.repo / name
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(content)

    def commit(self):
        self.git("add", ".")
        self.git("commit", "-m", "fixture")

    def test_prepares_exact_context_and_provenance_without_untracked_inputs(self):
        self.write("unrelated-secret", "not part of the helper")
        self.write("cmd/nerocd-storage-archive/untracked.go", "invalid untracked source")
        with patch.dict(os.environ, {"GOFLAGS": "-overlay=/untrusted", "GOENV": "/untrusted",
                                     "GOTOOLCHAIN": "auto", "GOEXPERIMENT": "untrusted",
                                     "GOCACHEPROG": "/untrusted", "GIT_DIR": "/untrusted"}):
            metadata = packager.prepare(self.repo, str(self.output), str(self.go))
        self.assertEqual(sorted(p.name for p in self.output.iterdir()), ["context", "provenance.json"])
        context = self.output / "context"
        self.assertEqual(sorted(p.name for p in context.iterdir()), ["Dockerfile", "nerocd-storage-archive"])
        executable = context / "nerocd-storage-archive"
        self.assertEqual(executable.read_bytes(), static_elf())
        self.assertEqual(executable.stat().st_mode & 0o777, 0o555)
        self.assertEqual(metadata["source_commit"], self.git("rev-parse", "HEAD"))
        self.assertEqual(metadata["executable_sha256"], hashlib.sha256(static_elf()).hexdigest())
        self.assertEqual(metadata["dockerfile_sha256"], hashlib.sha256((context / "Dockerfile").read_bytes()).hexdigest())
        self.assertEqual(json.loads((self.output / "provenance.json").read_text()), metadata)
        self.assertEqual(metadata["status"], "prepared_only")
        self.assertIsNone(metadata["image_config_id"])
        recipe = (context / "Dockerfile").read_text()
        self.assertNotIn("@", recipe)
        self.assertIn('ENTRYPOINT ["/nerocd-storage-archive"]', recipe)
        self.assertIn(metadata["source_commit"], recipe)
        self.assertIn(metadata["executable_sha256"], recipe)
        self.assertEqual(recipe.count("COPY "), 1)
        for directory in ("/source", "/target", "/tmp", "/"):
            self.assertIn("WORKDIR " + directory + "\n", recipe)
        log = json.loads(self.log.read_text())
        for key, value in {"GOENV": "off", "GOTOOLCHAIN": "local", "GOFLAGS": "", "GOWORK": "off",
                           "GOPROXY": "off", "GOSUMDB": "off", "GOOS": "linux", "GOARCH": "arm64", "CGO_ENABLED": "0"}.items():
            self.assertEqual(log["env"][key], value)
        for key in ("GOEXPERIMENT", "GOCACHEPROG", "GIT_DIR"):
            self.assertNotIn(key, log["env"])
        self.assertIn("-mod=readonly", log["args"])

    def test_rejects_dirty_tracked_source_and_wrong_toolchain_before_publication(self):
        self.write("go.sum", "changed")
        with self.assertRaisesRegex(packager.PreparationError, "clean"):
            packager.prepare(self.repo, str(self.output), str(self.go))
        self.assertFalse(self.output.exists())
        self.commit()
        self.go.write_text(f"#!{sys.executable}\nprint('go version go1.26.4 fixture/host')\n")
        with self.assertRaisesRegex(packager.PreparationError, "required version"):
            packager.prepare(self.repo, str(self.output), str(self.go))
        self.assertFalse(self.output.exists())

    def test_output_paths_are_exclusive_canonical_and_outside_source(self):
        self.output.mkdir()
        marker = self.output / "existing"
        marker.write_text("preserve")
        alias = self.root / "alias"
        alias.symlink_to(self.root, target_is_directory=True)
        for value in ("relative", str(self.root / ".." / "escape"), str(self.output),
                      str(alias / "other"), str(self.repo / "output"), str(self.root / "bad\nname")):
            with self.subTest(value=value), self.assertRaises(packager.PreparationError):
                packager.output_path(value, self.repo)
        self.assertEqual(marker.read_text(), "preserve")
        self.assertFalse(self.log.exists())

    def test_rejects_nonstatic_wrong_arch_and_truncated_executables(self):
        packager.validate_executable(static_elf())
        for offset, fmt, value in ((16, "<H", 3), (18, "<H", 62), (64, "<I", 3), (64, "<I", 2)):
            data = bytearray(static_elf())
            struct.pack_into(fmt, data, offset, value)
            with self.subTest(offset=offset, value=value), self.assertRaises(packager.PreparationError):
                packager.validate_executable(data)
        for data in (b"not ELF", static_elf()[:80]):
            with self.assertRaises(packager.PreparationError):
                packager.validate_executable(data)

    def test_export_rejects_traversal_links_duplicates_and_budgets(self):
        for name, kind in (("../escape", tarfile.REGTYPE), ("/escape", tarfile.REGTYPE),
                           ("cmd/nerocd-storage-archive/link", tarfile.SYMTYPE),
                           ("unrelated-secret", tarfile.REGTYPE)):
            archive = io.BytesIO()
            with tarfile.open(fileobj=archive, mode="w") as writer:
                header = tarfile.TarInfo(name)
                header.type = kind
                writer.addfile(header)
            with tempfile.TemporaryDirectory(dir=self.root) as target:
                with self.subTest(name=name), self.assertRaises(packager.PreparationError):
                    packager.export_source(archive.getvalue(), Path(target))
        with self.assertRaises(packager.PreparationError):
            packager.export_source(b"x" * (packager.MAX_SOURCE_BYTES + 1), self.root)
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode="w") as writer:
            for _ in range(2):
                writer.addfile(tarfile.TarInfo("go.mod"))
        with tempfile.TemporaryDirectory(dir=self.root) as target:
            with self.assertRaises(packager.PreparationError):
                packager.export_source(archive.getvalue(), Path(target))

    def test_subprocess_timeout_and_output_budget_kill_and_join_owned_group(self):
        env = packager.clean_environment(self.root)
        pidfile = self.root / "child.pid"
        code = "import os,time,pathlib;pathlib.Path(%r).write_text(str(os.getpid())+','+str(os.getpgrp()));time.sleep(30)" % str(pidfile)
        with self.assertRaisesRegex(packager.PreparationError, "timed out"):
            packager.run([sys.executable, "-c", code], self.root, env, timeout=0.3)
        pid, group = map(int, pidfile.read_text().split(","))
        self.assertEqual(group, pid)
        with self.assertRaises(ProcessLookupError):
            os.kill(pid, 0)
        with self.assertRaisesRegex(packager.PreparationError, "output budget"):
            packager.run([sys.executable, "-c", "print('x'*100000)"], self.root, env, limit=1024)
        # The current process remains alive and is never in a child session.
        self.assertNotEqual(os.getpgrp(), pid)

    def test_launcher_signals_terminate_and_join_compiler_even_during_cleanup(self):
        self.write("scripts/build-storage-archive-helper.py", (ROOT / "scripts/build-storage-archive-helper.py").read_text())
        self.commit()
        for requested, close_output in ((signal.SIGTERM, False), (signal.SIGINT, True)):
            with self.subTest(signal=requested):
                ready = self.root / f"ready-{requested}"
                cleaning = self.root / f"cleaning-{requested}"
                receipt = self.root / f"joined-{requested}"
                self.go.write_text(f"#!{sys.executable}\n" + f"""
import json, os, pathlib, sys, time
if sys.argv[1:] == ['version']:
    print('go version go1.26.10 fixture/host')
else:
    if {close_output!r}:
        os.close(1)
        os.close(2)
    pathlib.Path({str(ready)!r}).write_text(json.dumps({{'pid': os.getpid(), 'group': os.getpgrp()}}))
    time.sleep(30)
""")
                wrapper = self.root / f"launcher-{requested}.py"
                # A guard outside main makes the old implementation stall on
                # SIGTERM instead of orphaning the compiler during a red run.
                # SIGUSR1 requests bounded test cleanup through Python finally.
                wrapper.write_text(f"""
import importlib.util, json, os, pathlib, signal, subprocess, sys, time
spec = importlib.util.spec_from_file_location('subject', {str(self.repo / 'scripts/build-storage-archive-helper.py')!r})
subject = importlib.util.module_from_spec(spec)
spec.loader.exec_module(subject)
children = []
original = subprocess.Popen
def launch(*args, **kwargs):
    process = original(*args, **kwargs)
    children.append(process)
    if args[0][:2] == [{str(self.go)!r}, 'build']:
        original_wait = process.wait
        def wait(*args, **kwargs):
            if kwargs.get('timeout') == 5:
                pathlib.Path({str(cleaning)!r}).write_text('cleanup entered')
                time.sleep(0.2)
            return original_wait(*args, **kwargs)
        process.wait = wait
    return process
subprocess.Popen = launch
def guard(signum, frame):
    pass
class CleanupRequested(BaseException):
    pass
def cleanup(signum, frame):
    raise CleanupRequested()
signal.signal(signal.SIGTERM, guard)
signal.signal(signal.SIGINT, guard)
signal.signal(signal.SIGUSR1, cleanup)
code = 1
try:
    subject.main()
    code = 0
except SystemExit as error:
    code = error.code
except CleanupRequested:
    pass
finally:
    signal.signal(signal.SIGUSR1, signal.SIG_IGN)
    for process in children:
        if process.returncode is None:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
        process.wait(timeout=5)
    pathlib.Path({str(receipt)!r}).write_text(json.dumps([{{'pid': p.pid, 'returncode': p.returncode}} for p in children]))
sys.exit(code)
""")
                process = subprocess.Popen([sys.executable, "-B", str(wrapper), "--output", str(self.output),
                                            "--go", str(self.go)], stdout=subprocess.PIPE, stderr=subprocess.PIPE)

                def join_launcher(process=process):
                    if process.poll() is None:
                        process.send_signal(signal.SIGUSR1)
                    process.communicate(timeout=5)

                self.addCleanup(join_launcher)
                deadline = time.monotonic() + 3
                while not ready.exists() and time.monotonic() < deadline and process.poll() is None:
                    time.sleep(0.005)
                self.assertTrue(ready.exists(), "compiler did not start")
                identity = json.loads(ready.read_text())
                self.assertEqual(identity["pid"], identity["group"])
                # The SIGINT case closes both compiler output descriptors;
                # allow the watchdog to enter its separate wait-after-EOF path.
                if close_output:
                    time.sleep(0.1)
                begin = time.monotonic()
                process.send_signal(requested)
                deadline = begin + 1
                while not cleaning.exists() and time.monotonic() < deadline and process.poll() is None:
                    time.sleep(0.005)
                if not cleaning.exists():
                    join_launcher()
                    self.fail("termination did not enter owned compiler cleanup; guard joined child")
                # These arrive while the child's wait is deliberately delayed.
                process.send_signal(signal.SIGTERM)
                process.send_signal(signal.SIGINT)
                stdout, stderr = process.communicate(timeout=3)
                self.assertLess(time.monotonic() - begin, 2)
                self.assertEqual(process.returncode, 128 + requested)
                self.assertEqual(stdout, b"")
                self.assertEqual(stderr, b"storage archive image preparation interrupted\n")
                joined = json.loads(receipt.read_text())
                self.assertIn({"pid": identity["pid"], "returncode": -signal.SIGKILL}, joined)
                with self.assertRaises(ProcessLookupError):
                    os.kill(identity["pid"], 0)
                self.assertFalse(self.output.exists())


if __name__ == "__main__":
    unittest.main()
