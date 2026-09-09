from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("native_check.py")


class NativeCheckTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.external_temp = tempfile.TemporaryDirectory()
        self.repo = Path(self.temp.name).resolve()
        self.external = Path(self.external_temp.name).resolve()
        self.git("init", "-b", "bench/native")
        self.git("config", "user.name", "Native Check Test")
        self.git("config", "user.email", "native-check@example.invalid")
        self.write("owned/old.txt", "one\n")
        self.write("outside.txt", "outside\n")
        self.git("add", ".")
        self.git("commit", "-m", "base")
        self.base = self.rev("HEAD")

    def tearDown(self) -> None:
        self.temp.cleanup()
        self.external_temp.cleanup()

    def git(self, *args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            ["git", "-C", str(self.repo), *args],
            check=check,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )

    def rev(self, value: str) -> str:
        return self.git("rev-parse", value).stdout.strip()

    def write(self, relative: str, content: str) -> None:
        path = self.repo / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")

    def commit(self, message: str = "candidate") -> str:
        self.git("add", "-A")
        self.git("commit", "-m", message)
        return self.rev("HEAD")

    def run_check(
        self,
        command: str,
        *args: str,
        repo: Path | None = None,
        env: dict[str, str] | None = None,
        base: str | None = None,
        branch: str = "bench/native",
    ) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [
                sys.executable,
                "-B",
                str(SCRIPT),
                "--repo-root",
                str(repo or self.repo),
                "--base",
                base or self.base,
                "--branch",
                branch,
                command,
                *args,
            ],
            check=False,
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )

    def assert_failed(self, process: subprocess.CompletedProcess[str], text: str) -> None:
        self.assertNotEqual(process.returncode, 0, process.stdout)
        self.assertIn(text, process.stderr)

    def test_start_accepts_exact_clean_base(self) -> None:
        process = self.run_check("start")
        self.assertEqual(process.returncode, 0, process.stderr)
        self.assertEqual(process.stdout, "native start passed\n")

    def test_start_ignores_ambient_git_redirection_and_alternate_index(self) -> None:
        other = self.external / "other-repo"
        subprocess.run(
            ["git", "init", "-b", "other", str(other)],
            check=True, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        environment = os.environ.copy()
        environment["GIT_DIR"] = str(other / ".git")
        process = self.run_check("start", env=environment)
        self.assertEqual(process.returncode, 0, process.stderr)

        alternate_index = self.external / "alternate-index"
        alternate_environment = os.environ.copy()
        alternate_environment["GIT_INDEX_FILE"] = str(alternate_index)
        self.git("read-tree", "HEAD", check=True)
        subprocess.run(
            ["git", "-C", str(self.repo), "read-tree", "HEAD"],
            check=True, env=alternate_environment,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        self.write("owned/old.txt", "hidden by alternate index\n")
        subprocess.run(
            ["git", "-C", str(self.repo), "add", "owned/old.txt"],
            check=True, env=alternate_environment,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        self.assert_failed(
            self.run_check("start", env=alternate_environment),
            "tracked or untracked",
        )

    def test_start_rejects_wrong_root_branch_base_and_dirty_states(self) -> None:
        subdir = self.repo / "owned"
        self.assert_failed(self.run_check("start", repo=subdir), "Git toplevel")

        process = self.run_check("start", branch="wrong/branch")
        self.assert_failed(process, "branch mismatch")

        process = self.run_check("start", base="0" * 40)
        self.assert_failed(process, "not a commit")

        self.write("owned/old.txt", "dirty\n")
        self.assert_failed(self.run_check("start"), "tracked or untracked")
        self.git("restore", "owned/old.txt")
        self.write("untracked.txt", "new\n")
        self.assert_failed(self.run_check("start"), "tracked or untracked")

    def test_candidate_rejects_wrong_candidate_and_outside_ownership(self) -> None:
        self.write("outside.txt", "changed\n")
        candidate = self.commit()
        self.assert_failed(
            self.run_check("candidate", "--candidate", self.base, "--allow", "owned/"),
            "requires HEAD",
        )
        self.assert_failed(
            self.run_check("candidate", "--candidate", candidate, "--allow", "owned/"),
            "outside ownership",
        )

    def test_candidate_does_not_execute_fake_git_from_path(self) -> None:
        self.write("outside.txt", "forbidden change\n")
        candidate = self.commit()
        fake_bin = self.external / "fake-bin"
        fake_bin.mkdir()
        marker = self.external / "fake-git-ran"
        fake_git = fake_bin / "git"
        fake_git.write_text(
            """#!/bin/sh
: > "$FAKE_GIT_MARKER"
case "$*" in
  *"rev-parse --show-toplevel"*) printf '%s\\n' "$FAKE_GIT_ROOT" ;;
  *"cat-file -e"*) exit 0 ;;
  *"check-ref-format --branch"*) printf '%s\\n' 'bench/native' ;;
  *"symbolic-ref --quiet --short HEAD"*) printf '%s\\n' 'bench/native' ;;
  *"rev-parse HEAD"*) printf '%s\\n' "$FAKE_GIT_CANDIDATE" ;;
  *"merge-base --is-ancestor"*) exit 0 ;;
  *"status --porcelain=v1"*) exit 0 ;;
  *"diff --no-ext-diff"*) exit 0 ;;
  *) exit 1 ;;
esac
""",
            encoding="utf-8",
        )
        fake_git.chmod(0o755)
        environment = os.environ.copy()
        environment.update(
            {
                "PATH": str(fake_bin) + os.pathsep + environment.get("PATH", ""),
                "FAKE_GIT_MARKER": str(marker),
                "FAKE_GIT_ROOT": str(self.repo),
                "FAKE_GIT_CANDIDATE": candidate,
            }
        )
        process = self.run_check(
            "candidate", "--candidate", candidate, "--allow", "owned/", env=environment,
        )
        self.assert_failed(process, "outside ownership")
        self.assertFalse(marker.exists(), "fake git executable was invoked")

    def test_candidate_ignores_replacement_objects_that_hide_diff(self) -> None:
        self.write("outside.txt", "forbidden change\n")
        candidate = self.commit()
        candidate_tree = self.rev(f"{candidate}^{{tree}}")
        replacement = self.git(
            "commit-tree", candidate_tree, "-m", "replacement base with candidate tree",
        ).stdout.strip()
        self.git("replace", self.base, replacement)
        self.assertEqual(
            self.git("diff", "--name-only", self.base, candidate).stdout,
            "",
            "replacement-aware Git did not hide the fixture diff",
        )

        process = self.run_check(
            "candidate", "--candidate", candidate, "--allow", "owned/",
        )
        self.assert_failed(process, "outside ownership")
        self.assertIn("outside.txt", process.stderr)

    def test_candidate_rejects_nonancestor_base(self) -> None:
        self.git("checkout", "--orphan", "other")
        self.git("rm", "-rf", ".")
        self.write("owned/new.txt", "new\n")
        candidate = self.commit("unrelated")
        process = self.run_check(
            "candidate", "--candidate", candidate, "--allow", "owned/", branch="other",
        )
        self.assert_failed(process, "must be an ancestor")

    def test_candidate_requires_clean_tracked_and_untracked_state(self) -> None:
        self.write("owned/old.txt", "two\n")
        candidate = self.commit()
        self.write("owned/old.txt", "dirty\n")
        self.assert_failed(
            self.run_check("candidate", "--candidate", candidate, "--allow", "owned/"),
            "tracked or untracked",
        )
        self.git("restore", "owned/old.txt")
        self.write("scratch.txt", "new\n")
        self.assert_failed(
            self.run_check("candidate", "--candidate", candidate, "--allow", "owned/"),
            "tracked or untracked",
        )

    def test_candidate_checks_both_rename_paths_and_deletions(self) -> None:
        self.git("mv", "owned/old.txt", "owned/new.txt")
        candidate = self.commit("rename")
        self.assert_failed(
            self.run_check("candidate", "--candidate", candidate, "--allow", "owned/new.txt"),
            "owned/old.txt",
        )
        process = self.run_check(
            "candidate", "--candidate", candidate,
            "--allow", "owned/old.txt", "--allow", "owned/new.txt",
        )
        self.assertEqual(process.returncode, 0, process.stderr)

        self.git("rm", "outside.txt")
        deleted_candidate = self.commit("delete")
        self.assert_failed(
            self.run_check("candidate", "--candidate", deleted_candidate, "--allow", "owned/"),
            "outside.txt",
        )

    def test_candidate_rejects_hostile_allow_paths(self) -> None:
        self.write("owned/old.txt", "two\n")
        candidate = self.commit()
        hostile = [
            "/owned/", "../owned/", "owned/../outside.txt", "owned\\file",
            ".git/", ".GIT/config", ".git~1/config", "./owned/", "~/owned/",
        ]
        for value in hostile:
            with self.subTest(value=value):
                self.assert_failed(
                    self.run_check("candidate", "--candidate", candidate, "--allow", value),
                    "native check failed",
                )


if __name__ == "__main__":
    unittest.main()
