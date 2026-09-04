#!/usr/bin/env python3
"""Hermetic tests for product_go_advisory.py."""

from __future__ import annotations

import ast
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import time
from typing import Dict, Optional
import unittest


HOOK_DIR = Path(__file__).resolve().parent
ADAPTER = HOOK_DIR / "product_go_advisory.py"
REPO_ROOT = HOOK_DIR.parents[1]
HOOKS_CONFIG = REPO_ROOT / ".codex/hooks.json"
NAMING_CHECKER = REPO_ROOT / ".agents/skills/go-naming/scripts/check-naming.sh"


class AdvisoryTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "repo"
        self.root.mkdir()
        subprocess.run(
            ["git", "init", "-q"], cwd=self.root, check=True, stdout=subprocess.DEVNULL
        )
        for directory in ("cmd/app", "internal/core", "docs"):
            (self.root / directory).mkdir(parents=True)
        checker = self.root / ".agents/skills/go-naming/scripts/check-naming.sh"
        checker.parent.mkdir(parents=True)
        shutil.copy2(NAMING_CHECKER, checker)
        checker.chmod(0o755)
        adapter = self.root / ".codex/hooks/product_go_advisory.py"
        adapter.parent.mkdir(parents=True)
        shutil.copy2(ADAPTER, adapter)

    def write(self, relative: str, content: str) -> Path:
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
        return path

    @staticmethod
    def event(command: object, **updates: object) -> Dict[str, object]:
        payload: Dict[str, object] = {
            "hook_event_name": "PostToolUse",
            "tool_name": "apply_patch",
            "tool_input": {"command": command},
        }
        payload.update(updates)
        return payload

    def run_hook(
        self,
        payload: object,
        *,
        cwd: Optional[Path] = None,
        env: Optional[Dict[str, str]] = None,
        raw: bool = False,
    ) -> subprocess.CompletedProcess:
        data = payload if raw else json.dumps(payload).encode("utf-8")
        assert isinstance(data, bytes)
        test_env = os.environ.copy()
        test_env["PYTHONDONTWRITEBYTECODE"] = "1"
        if env:
            test_env.update(env)
        return subprocess.run(
            ["python3", str(ADAPTER)],
            cwd=cwd or self.root,
            input=data,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=test_env,
            timeout=6,
            check=False,
        )

    def assert_no_output(self, result: subprocess.CompletedProcess) -> None:
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, b"")
        self.assertEqual(result.stderr, b"")

    def run_configured_hook(
        self, payload: object, *, env: Optional[Dict[str, str]] = None
    ) -> subprocess.CompletedProcess:
        config = json.loads(HOOKS_CONFIG.read_text(encoding="utf-8"))
        command = config["hooks"]["PostToolUse"][0]["hooks"][0]["command"]
        test_env = os.environ.copy()
        test_env["PYTHONDONTWRITEBYTECODE"] = "1"
        if env:
            test_env.update(env)
        return subprocess.run(
            command,
            shell=True,
            executable="/bin/sh",
            cwd=self.root,
            input=json.dumps(payload).encode("utf-8"),
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=test_env,
            timeout=6,
            check=False,
        )

    def test_valid_clean_edit_has_exact_empty_output_from_subdirectory(self) -> None:
        self.write("cmd/app/main.go", "package app\n\nconst GoodName = 1\n")
        result = self.run_hook(
            self.event("*** Begin Patch\n*** Update File: cmd/app/main.go\n*** End Patch"),
            cwd=self.root / "internal/core",
        )
        self.assert_no_output(result)

    def test_malformed_input_fails_open(self) -> None:
        for payload, raw in (
            (b"{", True),
            (b"\xff", True),
            ([], False),
            (self.event(7), False),
        ):
            with self.subTest(payload=payload):
                self.assert_no_output(self.run_hook(payload, raw=raw))

    def test_wrong_event_and_tool_fail_open(self) -> None:
        patch = "*** Update File: cmd/app/main.go"
        for updates in (
            {"hook_event_name": "PreToolUse"},
            {"tool_name": "Bash"},
            {"tool_input": []},
        ):
            with self.subTest(updates=updates):
                self.assert_no_output(self.run_hook(self.event(patch, **updates)))

    def test_non_product_and_unsupported_headers_are_ignored(self) -> None:
        self.write("docs/example.go", "package docs\nconst BAD_NAME=1\n")
        self.write("cmd/app/deleted.go", "package app\nconst BAD_NAME=1\n")
        patch = "\n".join(
            (
                "*** Add File: docs/example.go",
                "*** Delete File: cmd/app/deleted.go",
                "*** Move to: cmd/app/moved.go",
                "*** Update File: cmd/app/not-go.txt",
            )
        )
        self.assert_no_output(self.run_hook(self.event(patch)))

    def test_absolute_traversal_and_symlink_escape_are_ignored(self) -> None:
        self.write("outside.go", "package outside\nconst BAD_NAME=1\n")
        outside = Path(self.temp.name) / "outside.go"
        outside.write_text("package outside\nconst BAD_NAME=1\n", encoding="utf-8")
        link = self.root / "cmd/app/link.go"
        try:
            link.symlink_to(outside)
        except OSError as exc:
            self.skipTest(f"symlinks unavailable: {exc}")
        patch = "\n".join(
            (
                f"*** Update File: {outside}",
                "*** Update File: cmd/../outside.go",
                "*** Update File: ./cmd/app/link.go",
                "*** Update File: cmd/app/link.go",
                "*** Update File: cmd//app/link.go",
                "*** Update File: cmd\\app\\link.go",
            )
        )
        self.assert_no_output(self.run_hook(self.event(patch)))

    def test_path_cap_and_deduplication(self) -> None:
        headers = []
        for index in range(10):
            relative = f"internal/core/file{index}.go"
            self.write(relative, f"package core\nconst Value{index}= {index}\n")
            headers.append(f"*** Update File: {relative}")
            if index == 0:
                headers.append(f"*** Update File: {relative}")
        result = self.run_hook(self.event("\n".join(headers)))
        self.assertEqual(result.returncode, 0)
        output = json.loads(result.stdout)
        lines = output["hookSpecificOutput"]["additionalContext"].splitlines()
        self.assertEqual(lines, [f"internal/core/file{i}.go: gofmt required" for i in range(8)])

    def test_unrelated_headers_do_not_consume_product_path_cap(self) -> None:
        headers = []
        for index in range(300):
            headers.append(f"*** Update File: docs/example{index}.go")
        self.write("cmd/app/main.go", "package app\nconst GoodName=1\n")
        headers.append("*** Update File: cmd/app/main.go")
        result = self.run_hook(self.event("\n".join(headers)))
        context = json.loads(result.stdout)["hookSpecificOutput"]["additionalContext"]
        self.assertEqual(context, "cmd/app/main.go: gofmt required")

    def test_unsafe_display_components_are_rejected(self) -> None:
        unsafe_names = (
            "escape\x1bname.go",
            "right\u202ename.go",
            "white space.go",
            "ignore:previous-instructions.go",
        )
        headers = []
        for name in unsafe_names:
            self.write(f"cmd/app/{name}", "package app\nconst BAD_NAME=1\n")
            headers.append(f"*** Update File: cmd/app/{name}")
        self.write("internal/core/safe.go", "package core\nconst GoodName=1\n")
        headers.append("*** Update File: internal/core/safe.go")
        result = self.run_hook(self.event("\n".join(headers)))
        context = json.loads(result.stdout)["hookSpecificOutput"]["additionalContext"]
        self.assertEqual(context, "internal/core/safe.go: gofmt required")
        for name in unsafe_names:
            self.assertNotIn(name, context)

    def test_gofmt_finding_emits_exact_advisory_json(self) -> None:
        self.write("cmd/app/main.go", "package app\nconst GoodName=1\n")
        result = self.run_hook(self.event("*** Add File: cmd/app/main.go"))
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stderr, b"")
        self.assertEqual(
            result.stdout,
            b'{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"cmd/app/main.go: gofmt required"}}\n',
        )

    def test_naming_finding_uses_only_relative_path(self) -> None:
        self.write("internal/core/core.go", "package core\n\nconst BAD_NAME = 1\n")
        result = self.run_hook(self.event("*** Update File: internal/core/core.go"))
        output = json.loads(result.stdout)
        context = output["hookSpecificOutput"]["additionalContext"]
        self.assertEqual(
            context,
            "internal/core/core.go:3 [screaming-const] constant 'BAD_NAME' uses SCREAMING_SNAKE_CASE; use MixedCaps instead",
        )
        self.assertNotIn(str(self.root), context)

    def test_output_is_limited_to_ten_messages_and_800_utf8_bytes(self) -> None:
        headers = []
        for index in range(8):
            name = "long" + ("a" * 180) + str(index) + ".go"
            relative = "internal/core/" + name
            constants = "\n".join(
                "\tBAD_NAME_{0} = {0}".format(number) for number in range(5)
            )
            self.write(
                relative,
                "package core\n\nconst (\n{0}\n)\n".format(constants),
            )
            headers.append("*** Update File: " + relative)
        result = self.run_hook(self.event("\n".join(headers)))
        context = json.loads(result.stdout)["hookSpecificOutput"]["additionalContext"]
        self.assertGreater(len(context.splitlines()), 0)
        self.assertLessEqual(len(context.splitlines()), 10)
        self.assertEqual(len(context.encode("utf-8")), 800)
        self.assertNotIn(str(self.root), context)

    def test_output_is_limited_to_ten_messages(self) -> None:
        headers = []
        for index in range(3):
            relative = "cmd/app/names{0}.go".format(index)
            constants = "\n".join(
                "\tBAD_NAME_{0} = {0}".format(number) for number in range(5)
            )
            self.write(
                relative,
                "package app\n\nconst (\n{0}\n)\n".format(constants),
            )
            headers.append("*** Update File: " + relative)
        result = self.run_hook(self.event("\n".join(headers)))
        context = json.loads(result.stdout)["hookSpecificOutput"]["additionalContext"]
        self.assertGreater(len(context.splitlines()), 0)
        self.assertLessEqual(len(context.splitlines()), 10)
        self.assertLessEqual(len(context.encode("utf-8")), 800)

    def _prepend_fake_gofmt(self, body: str) -> Dict[str, str]:
        bin_dir = Path(self.temp.name) / "bin"
        bin_dir.mkdir(exist_ok=True)
        gofmt = bin_dir / "gofmt"
        gofmt.write_text(f"#!/bin/sh\n{body}\n", encoding="utf-8")
        gofmt.chmod(0o755)
        return {"PATH": f"{bin_dir}{os.pathsep}{os.environ.get('PATH', '')}"}

    def test_checker_failure_fails_open(self) -> None:
        self.write("cmd/app/main.go", "package app\nconst GoodName=1\n")
        result = self.run_hook(
            self.event("*** Update File: cmd/app/main.go"),
            env=self._prepend_fake_gofmt("exit 2"),
        )
        self.assert_no_output(result)

    def test_checker_timeout_fails_open_within_bound(self) -> None:
        self.write("cmd/app/main.go", "package app\nconst GoodName=1\n")
        started = time.monotonic()
        result = self.run_hook(
            self.event("*** Update File: cmd/app/main.go"),
            env=self._prepend_fake_gofmt("sleep 2; exit 0"),
        )
        elapsed = time.monotonic() - started
        self.assert_no_output(result)
        self.assertLess(elapsed, 1.8)

    def test_background_grandchild_is_terminated_after_parent_exits(self) -> None:
        self.write("cmd/app/main.go", "package app\n\nconst GoodName = 1\n")
        pid_file = Path(self.temp.name) / "grandchild.pid"
        marker = Path(self.temp.name) / "grandchild-survived"
        body = "\n".join(
            (
                '(sleep 2; printf survived > "$GRANDCHILD_MARKER") >/dev/null 2>&1 &',
                'printf "%s" "$!" > "$GRANDCHILD_PID_FILE"',
                'last=""',
                'for arg in "$@"; do last="$arg"; done',
                'printf "%s\\n" "$last"',
                "exit 0",
            )
        )
        result = self.run_hook(
            self.event("*** Update File: cmd/app/main.go"),
            env={
                **self._prepend_fake_gofmt(body),
                "GRANDCHILD_PID_FILE": str(pid_file),
                "GRANDCHILD_MARKER": str(marker),
            },
        )
        self.assertEqual(result.returncode, 0)
        self.assertTrue(pid_file.exists())
        pid = int(pid_file.read_text(encoding="utf-8"))
        for _ in range(40):
            status = subprocess.run(
                ["ps", "-p", str(pid), "-o", "state="],
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                check=False,
            ).stdout.decode("ascii", errors="ignore").strip()
            if not status or status.startswith("Z"):
                break
            time.sleep(0.025)
        else:
            self.fail("background grandchild remained alive after hook cleanup")
        time.sleep(2.1)
        self.assertFalse(marker.exists())

    def test_child_stdout_and_stderr_overflow_fail_open(self) -> None:
        self.write("cmd/app/main.go", "package app\n")
        outputs = (
            "python3 -c 'import sys; sys.stdout.buffer.write(b\"x\" * 70000)'",
            "python3 -c 'import sys; sys.stderr.buffer.write(b\"x\" * 20000)'",
        )
        for body in outputs:
            with self.subTest(body=body):
                result = self.run_hook(
                    self.event("*** Update File: cmd/app/main.go"),
                    env=self._prepend_fake_gofmt(body),
                )
                self.assert_no_output(result)

    def test_files_larger_than_one_mibibyte_are_skipped(self) -> None:
        source = self.root / "cmd/app/large.go"
        source.write_bytes(b"package app\n/*" + (b"x" * (1024 * 1024)) + b"*/\n")
        before = hashlib.sha256(source.read_bytes()).digest()
        result = self.run_hook(self.event("*** Update File: cmd/app/large.go"))
        self.assert_no_output(result)
        self.assertEqual(hashlib.sha256(source.read_bytes()).digest(), before)

    def test_parent_replacement_cannot_redirect_open_descriptor(self) -> None:
        source = self.write(
            "internal/core/main.go", "package core\nconst OriginalName=1\n"
        )
        external = Path(self.temp.name) / "replacement"
        external.mkdir()
        replacement = external / "main.go"
        replacement.write_text(
            "package core\n\nconst BAD_NAME = 1\n", encoding="utf-8"
        )
        before = hashlib.sha256(source.read_bytes()).digest()
        race_dir = self.root / "internal/core"
        old_dir = self.root / "internal/core-old"
        body = "\n".join(
            (
                'mv "$RACE_DIR" "$RACE_OLD"',
                'ln -s "$RACE_EXTERNAL" "$RACE_DIR"',
                'last=""',
                'for arg in "$@"; do last="$arg"; done',
                'grep -q OriginalName "$last" || exit 2',
                'printf "%s\\n" "$last"',
            )
        )
        env = self._prepend_fake_gofmt(body)
        env.update(
            {
                "RACE_DIR": str(race_dir),
                "RACE_OLD": str(old_dir),
                "RACE_EXTERNAL": str(external),
            }
        )
        result = self.run_hook(
            self.event("*** Update File: internal/core/main.go"), env=env
        )
        context = json.loads(result.stdout)["hookSpecificOutput"]["additionalContext"]
        self.assertEqual(context, "internal/core/main.go: gofmt required")
        self.assertTrue(race_dir.is_symlink())
        self.assertEqual(hashlib.sha256((old_dir / "main.go").read_bytes()).digest(), before)
        self.assertEqual(
            replacement.read_text(encoding="utf-8"),
            "package core\n\nconst BAD_NAME = 1\n",
        )

    def test_modified_naming_script_neither_executes_nor_writes_marker(self) -> None:
        source = self.write("cmd/app/main.go", "package app\n\nconst GoodName = 1\n")
        checker = self.root / ".agents/skills/go-naming/scripts/check-naming.sh"
        marker = Path(self.temp.name) / "naming-executed"
        checker.write_text(
            "#!/bin/sh\nprintf executed > \"$NAMING_MARKER\"\nexit 0\n",
            encoding="utf-8",
        )
        result = self.run_hook(
            self.event("*** Update File: cmd/app/main.go"),
            env={"NAMING_MARKER": str(marker)},
        )
        self.assert_no_output(result)
        self.assertFalse(marker.exists())
        self.assertEqual(source.read_text(encoding="utf-8"), "package app\n\nconst GoodName = 1\n")

    def test_legacy_naming_checker_path_is_ignored(self) -> None:
        self.write("cmd/app/main.go", "package app\n\nconst BAD_NAME = 1\n")
        legacy = self.root / "agent/skills/go-naming/scripts/check-naming.sh"
        legacy.parent.mkdir(parents=True)
        marker = Path(self.temp.name) / "legacy-naming-executed"
        legacy.write_text(
            "#!/bin/sh\nprintf executed > \"$LEGACY_NAMING_MARKER\"\nexit 0\n",
            encoding="utf-8",
        )
        legacy.chmod(0o755)
        result = self.run_hook(
            self.event("*** Update File: cmd/app/main.go"),
            env={"LEGACY_NAMING_MARKER": str(marker)},
        )
        context = json.loads(result.stdout)["hookSpecificOutput"]["additionalContext"]
        self.assertIn("[screaming-const]", context)
        self.assertFalse(marker.exists())

    def test_in_place_naming_mutation_after_verification_uses_verified_text(self) -> None:
        self.write("cmd/app/main.go", "package app\n\nconst BAD_NAME = 1\n")
        checker = self.root / ".agents/skills/go-naming/scripts/check-naming.sh"
        marker = Path(self.temp.name) / "post-verification-executed"
        malicious = (
            "#!/bin/sh\nprintf executed > \"$POST_VERIFY_MARKER\"\nexit 0\n"
        )
        env = self._prepend_fake_gofmt(
            'printf "%s" "$MUTATED_NAMING_SCRIPT" > "$NAMING_SCRIPT_PATH"'
        )
        env.update(
            {
                "MUTATED_NAMING_SCRIPT": malicious,
                "NAMING_SCRIPT_PATH": str(checker),
                "POST_VERIFY_MARKER": str(marker),
            }
        )
        result = self.run_hook(
            self.event("*** Update File: cmd/app/main.go"), env=env
        )
        output = json.loads(result.stdout)
        context = output["hookSpecificOutput"]["additionalContext"]
        self.assertEqual(
            context,
            "cmd/app/main.go:3 [screaming-const] constant 'BAD_NAME' uses SCREAMING_SNAKE_CASE; use MixedCaps instead",
        )
        self.assertEqual(checker.read_text(encoding="utf-8"), malicious)
        self.assertFalse(marker.exists())

    def test_naming_script_does_not_need_executable_bit(self) -> None:
        self.write("cmd/app/main.go", "package app\n\nconst BAD_NAME = 1\n")
        checker = self.root / ".agents/skills/go-naming/scripts/check-naming.sh"
        checker.chmod(0o644)
        result = self.run_hook(self.event("*** Update File: cmd/app/main.go"))
        context = json.loads(result.stdout)["hookSpecificOutput"]["additionalContext"]
        self.assertIn("[screaming-const]", context)

    def test_integrity_pins_match_live_sources(self) -> None:
        config = json.loads(HOOKS_CONFIG.read_text(encoding="utf-8"))
        handler = config["hooks"]["PostToolUse"][0]["hooks"][0]
        command_hashes = re.findall(r"\b[0-9a-f]{64}\b", handler["command"])
        self.assertEqual(
            command_hashes,
            [hashlib.sha256(ADAPTER.read_bytes()).hexdigest()],
        )
        match = re.search(
            r'^NAMING_SCRIPT_SHA256 = "([0-9a-f]{64})"$',
            ADAPTER.read_text(encoding="utf-8"),
            re.MULTILINE,
        )
        self.assertIsNotNone(match)
        assert match is not None
        self.assertEqual(
            match.group(1), hashlib.sha256(NAMING_CHECKER.read_bytes()).hexdigest()
        )
        self.assertEqual(config["hooks"]["PostToolUse"][0]["matcher"], "^apply_patch$")
        self.assertTrue(handler["command"].startswith("/usr/bin/python3 -I -c "))

    def test_configured_launcher_passes_original_stdin(self) -> None:
        self.write("internal/core/core.go", "package core\n\nconst BAD_NAME = 1\n")
        result = self.run_configured_hook(
            self.event("*** Update File: internal/core/core.go")
        )
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stderr, b"")
        context = json.loads(result.stdout)["hookSpecificOutput"]["additionalContext"]
        self.assertIn("[screaming-const]", context)

    def test_configured_launcher_rejects_modified_adapter(self) -> None:
        marker = Path(self.temp.name) / "adapter-executed"
        adapter = self.root / ".codex/hooks/product_go_advisory.py"
        adapter.write_text(
            "import os\nopen(os.environ['ADAPTER_MARKER'], 'w').write('executed')\n",
            encoding="utf-8",
        )
        result = self.run_configured_hook(
            self.event("*** Update File: cmd/app/main.go"),
            env={"ADAPTER_MARKER": str(marker)},
        )
        self.assert_no_output(result)
        self.assertFalse(marker.exists())

    def test_source_is_immutable(self) -> None:
        source = self.write("cmd/app/main.go", "package app\nconst BAD_NAME=1\n")
        before = hashlib.sha256(source.read_bytes()).digest()
        result = self.run_hook(self.event("*** Update File: cmd/app/main.go"))
        self.assertTrue(result.stdout)
        self.assertEqual(hashlib.sha256(source.read_bytes()).digest(), before)

    def test_sources_parse_as_python_39(self) -> None:
        for path in (ADAPTER, Path(__file__).resolve()):
            with self.subTest(path=path):
                ast.parse(
                    path.read_text(encoding="utf-8"),
                    filename=str(path),
                    feature_version=9,
                )


if __name__ == "__main__":
    unittest.main()
