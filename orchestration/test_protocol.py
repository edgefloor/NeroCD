from __future__ import annotations

import copy
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from orchestration.protocol import (
    ProtocolError,
    advance_checkpoint,
    digest_record,
    preflight,
    run_skills_validator,
    validate_checkpoint,
    validate_packet,
    validate_result,
    validate_review,
)


def run(repo: Path, *args: str) -> str:
    completed = subprocess.run(args, cwd=repo, check=True, text=True, stdout=subprocess.PIPE)
    return completed.stdout.strip()


class ProtocolTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        run(self.repo, "git", "init", "-b", "pilot")
        run(self.repo, "git", "config", "user.email", "tests@example.invalid")
        run(self.repo, "git", "config", "user.name", "Protocol Tests")
        (self.repo / "owned").mkdir()
        (self.repo / "owned" / "file.txt").write_text("base\n", encoding="utf-8")
        (self.repo / "outside.txt").write_text("base\n", encoding="utf-8")
        run(self.repo, "git", "add", ".")
        run(self.repo, "git", "commit", "-m", "base")
        self.base = run(self.repo, "git", "rev-parse", "HEAD")
        self.packet = self.make_packet("worker-a", ["owned/"])
        self.capabilities = {
            "record_type": "runtime_capabilities",
            "schema_version": 1,
            "attested": True,
            "source": "test harness",
            "models": {"gpt-5.6-terra": ["medium"]},
            "tools": ["colgrep", "python3"],
            "permissions": {"repo_read": True, "workspace_write": True},
        }

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def make_packet(self, task_id: str, writes: list[str]) -> dict:
        return {
            "record_type": "packet",
            "schema_version": 1,
            "task_id": task_id,
            "mode": "WORKER",
            "goal": "Make the bounded test change.",
            "repository": {
                "workspace_root": str(self.repo),
                "base_sha": self.base,
                "branch": "pilot",
            },
            "ownership": {"read_paths": ["owned/"], "write_paths": writes},
            "interfaces": ["Text file remains UTF-8."],
            "constraints": ["Do not edit outside ownership."],
            "validation": ["unit"],
            "handoff": ["Return candidate SHA and bounded evidence."],
            "route": {
                "role": "bounded_worker",
                "model": "gpt-5.6-terra",
                "effort": "medium",
                "required_tools": ["colgrep", "python3"],
            },
            "authorization": {"grants": []},
            "selected_skills": {},
            "required_review_lenses": ["code_hygiene", "specification"],
            "required_completion_stages": ["source"],
        }

    def commit_owned_change(self) -> str:
        (self.repo / "owned" / "file.txt").write_text("candidate\n", encoding="utf-8")
        run(self.repo, "git", "add", ".")
        run(self.repo, "git", "commit", "-m", "candidate")
        return run(self.repo, "git", "rev-parse", "HEAD")

    def make_result(self, candidate: str, *, check_status: str = "passed") -> dict:
        return {
            "record_type": "result",
            "schema_version": 1,
            "task_id": self.packet["task_id"],
            "mode": "WORKER",
            "packet_digest": digest_record(self.packet),
            "base_sha": self.base,
            "candidate_sha": candidate,
            "outcome": "changed",
            "changed_paths": ["owned/file.txt"],
            "checks": [{
                "id": "unit", "status": check_status,
                "summary": "Synthetic unit result for protocol tests.",
                "location": "test runner summary",
            }],
            "evidence": [{"summary": "One owned file changed.", "location": "owned/file.txt"}],
            "completion": {
                "source": "complete", "artifact": "not_applicable",
                "published": "not_applicable", "deployed": "not_applicable",
            },
        }

    def make_review(self, candidate: str, lens: str, status: str = "passed") -> dict:
        return {
            "record_type": "review",
            "schema_version": 1,
            "task_id": self.packet["task_id"],
            "packet_digest": digest_record(self.packet),
            "candidate_sha": candidate,
            "lens": lens,
            "status": status,
            "findings": [{"summary": "No findings.", "location": "owned/file.txt"}],
        }

    def write_records(self, result: dict, reviews: list[dict]) -> tuple[Path, Path, list[Path]]:
        records = self.root / "records"
        records.mkdir(exist_ok=True)
        packet_path = records / "packet.json"
        result_path = records / "result.json"
        packet_path.write_text(json.dumps(self.packet), encoding="utf-8")
        result_path.write_text(json.dumps(result), encoding="utf-8")
        review_paths = []
        for index, review in enumerate(reviews):
            path = records / f"review-{index}.json"
            path.write_text(json.dumps(review), encoding="utf-8")
            review_paths.append(path)
        return packet_path, result_path, review_paths

    def write_checkpoint_record(
        self,
        state: Path,
        packet_path: Path,
        result_path: Path,
        review_paths: list[Path],
        result: dict,
        reviews: list[dict],
        *,
        stage: str = "accepted",
        task_id: str | None = None,
    ) -> None:
        checkpoint = {
            "record_type": "checkpoint",
            "schema_version": 1,
            "task_id": task_id or self.packet["task_id"],
            "stage": stage,
            "packet_digest": digest_record(self.packet),
            "candidate_sha": result["candidate_sha"],
            "records": {
                "packet": {
                    "path": str(packet_path.resolve()),
                    "digest": digest_record(self.packet),
                },
                "result": {
                    "path": str(result_path.resolve()),
                    "digest": digest_record(result),
                },
                "reviews": [
                    {
                        "path": str(path.resolve()),
                        "digest": digest_record(review),
                    }
                    for path, review in zip(review_paths, reviews, strict=True)
                ],
            },
        }
        state.parent.mkdir(parents=True, exist_ok=True)
        state.write_text(json.dumps(checkpoint), encoding="utf-8")

    def test_malformed_and_unknown_records_fail(self) -> None:
        malformed = copy.deepcopy(self.packet)
        del malformed["goal"]
        with self.assertRaisesRegex(ProtocolError, "missing fields: goal"):
            validate_packet(malformed)
        malformed = copy.deepcopy(self.packet)
        malformed["schema_version"] = 2
        with self.assertRaisesRegex(ProtocolError, "unsupported schema_version"):
            validate_packet(malformed)
        malformed["schema_version"] = True
        with self.assertRaisesRegex(ProtocolError, "unsupported schema_version"):
            validate_packet(malformed)
        malformed = copy.deepcopy(self.packet)
        malformed["mode"] = "AUTONOMOUS"
        with self.assertRaisesRegex(ProtocolError, "unsupported mode"):
            validate_packet(malformed)

    def test_record_size_is_bounded(self) -> None:
        from orchestration.protocol import load_json

        oversized = self.root / "oversized.json"
        oversized.write_bytes(b" " * (1024 * 1024 + 1))
        with self.assertRaisesRegex(ProtocolError, "byte limit"):
            load_json(oversized)

    def test_external_grant_requires_target(self) -> None:
        malformed = copy.deepcopy(self.packet)
        malformed["authorization"]["grants"] = [
            {"action": "publish", "target": "", "granted_by": "user"},
        ]
        with self.assertRaisesRegex(ProtocolError, "target"):
            validate_packet(malformed)

    def test_preflight_routes_tools_permissions_and_disjoint_ownership(self) -> None:
        second = self.make_packet("worker-b", ["other/"])
        preflight([self.packet, second], self.capabilities)
        conflicting = self.make_packet("worker-b", ["owned/file.txt"])
        with self.assertRaisesRegex(ProtocolError, "overlapping write ownership"):
            preflight([self.packet, conflicting], self.capabilities)
        identical = self.make_packet("worker-b", ["owned/"])
        with self.assertRaisesRegex(ProtocolError, "overlapping write ownership"):
            preflight([self.packet, identical], self.capabilities)
        missing_tool = copy.deepcopy(self.capabilities)
        missing_tool["tools"] = ["python3"]
        with self.assertRaisesRegex(ProtocolError, "missing required tools"):
            preflight([self.packet], missing_tool)

    def test_preflight_checks_checkout_and_role_mode(self) -> None:
        preflight([self.packet], self.capabilities, [self.repo])
        wrong_mode = copy.deepcopy(self.packet)
        wrong_mode["mode"] = "INTEGRATOR"
        with self.assertRaisesRegex(ProtocolError, "incompatible with role"):
            preflight([wrong_mode], self.capabilities)
        (self.repo / "dirty.txt").write_text("dirty\n", encoding="utf-8")
        with self.assertRaisesRegex(ProtocolError, "dirty"):
            preflight([self.packet], self.capabilities, [self.repo])

    def test_clis_do_not_dirty_clean_checkout_without_python_env_override(self) -> None:
        source_root = Path(__file__).resolve().parents[1]
        cli_repo = self.root / "cli-repo"
        cli_repo.mkdir()
        run(cli_repo, "git", "init", "-b", "pilot")
        run(cli_repo, "git", "config", "user.email", "tests@example.invalid")
        run(cli_repo, "git", "config", "user.name", "Protocol Tests")
        ignore_bytecode = shutil.ignore_patterns("__pycache__", "*.pyc")
        shutil.copytree(
            source_root / "orchestration",
            cli_repo / "orchestration",
            ignore=ignore_bytecode,
        )
        shutil.copytree(
            source_root / "scripts" / "orchestration",
            cli_repo / "scripts" / "orchestration",
            ignore=ignore_bytecode,
        )
        run(cli_repo, "git", "add", ".")
        run(cli_repo, "git", "commit", "-m", "CLI fixture")
        base = run(cli_repo, "git", "rev-parse", "HEAD")
        packet = copy.deepcopy(self.packet)
        packet["task_id"] = "cli-regression"
        packet["repository"] = {
            "workspace_root": str(cli_repo),
            "base_sha": base,
            "branch": "pilot",
        }
        packet_path = self.root / "cli-packet.json"
        capability_path = self.root / "cli-capabilities.json"
        packet_path.write_text(json.dumps(packet), encoding="utf-8")
        capability_path.write_text(json.dumps(self.capabilities), encoding="utf-8")
        environment = os.environ.copy()
        environment.pop("PYTHONDONTWRITEBYTECODE", None)
        commands = [
            [sys.executable, "scripts/orchestration/validate.py", "--help"],
            [sys.executable, "scripts/orchestration/checkpoint.py", "--help"],
            [
                sys.executable,
                "scripts/orchestration/preflight.py",
                "--capabilities",
                str(capability_path),
                "--packet",
                str(packet_path),
                "--repo-root",
                str(cli_repo),
            ],
        ]
        for command in commands:
            completed = subprocess.run(
                command,
                cwd=cli_repo,
                env=environment,
                check=False,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.assertEqual(0, completed.returncode, completed.stderr)
        self.assertIn("preflight passed: cli-regression", completed.stdout)
        self.assertEqual("", run(cli_repo, "git", "status", "--porcelain"))
        self.assertFalse(list(cli_repo.rglob("__pycache__")))

    def test_preflight_rejects_missing_base_and_wrong_branch(self) -> None:
        missing = copy.deepcopy(self.packet)
        missing["repository"]["base_sha"] = "0" * 40
        with self.assertRaisesRegex(ProtocolError, "does not exist"):
            preflight([missing], self.capabilities, [self.repo])
        wrong_branch = copy.deepcopy(self.packet)
        wrong_branch["repository"]["branch"] = "other"
        with self.assertRaisesRegex(ProtocolError, "branch mismatch"):
            preflight([wrong_branch], self.capabilities, [self.repo])

    def test_writable_packet_requires_review_and_skill_hashes_match(self) -> None:
        no_review = copy.deepcopy(self.packet)
        no_review["required_review_lenses"] = []
        with self.assertRaisesRegex(ProtocolError, "requires independent review"):
            validate_packet(no_review)
        expected = "a" * 64
        selected = copy.deepcopy(self.packet)
        selected["selected_skills"] = {"golang-testing": expected}
        validator = self.root / "skills.py"
        validator.write_text(
            'import json\nprint(json.dumps({"skills": {"golang-testing": "' + expected + '"}}))\n',
            encoding="utf-8",
        )
        installed = run_skills_validator(validator)
        preflight([selected], self.capabilities, installed_skills=installed)

    def test_result_uses_actual_git_paths_and_requires_clean_candidate(self) -> None:
        candidate = self.commit_owned_change()
        result = self.make_result(candidate)
        validate_result(result, self.packet, self.repo)
        (self.repo / "untracked.txt").write_text("dirty\n", encoding="utf-8")
        with self.assertRaisesRegex(ProtocolError, "dirty"):
            validate_result(result, self.packet, self.repo)

    def test_result_rejects_alternate_clone_and_non_toplevel(self) -> None:
        candidate = self.commit_owned_change()
        result = self.make_result(candidate)
        clone = self.root / "clean-clone"
        run(self.root, "git", "clone", "--quiet", "--branch", "pilot", str(self.repo), str(clone))
        (self.repo / "dirty.txt").write_text("dirty\n", encoding="utf-8")
        with self.assertRaisesRegex(ProtocolError, "does not match packet workspace_root"):
            validate_result(result, self.packet, clone)
        with self.assertRaisesRegex(ProtocolError, "must be the Git toplevel"):
            validate_result(result, self.packet, self.repo / "owned")

    def test_git_backslash_filename_cannot_alias_into_owned_directory(self) -> None:
        (self.repo / "owned\\escape.txt").write_text("escape\n", encoding="utf-8")
        run(self.repo, "git", "add", ".")
        run(self.repo, "git", "commit", "-m", "backslash candidate")
        candidate = run(self.repo, "git", "rev-parse", "HEAD")
        result = self.make_result(candidate)
        result["changed_paths"] = ["owned/escape.txt"]
        with self.assertRaisesRegex(ProtocolError, "backslashes"):
            validate_result(result, self.packet, self.repo)

    def test_rename_and_deletion_cannot_escape_ownership(self) -> None:
        run(self.repo, "git", "mv", "outside.txt", "owned/moved.txt")
        run(self.repo, "git", "commit", "-m", "rename", "--", "outside.txt", "owned/moved.txt")
        candidate = run(self.repo, "git", "rev-parse", "HEAD")
        result = self.make_result(candidate)
        result["changed_paths"] = ["outside.txt", "owned/moved.txt"]
        with self.assertRaisesRegex(ProtocolError, "escape ownership: outside.txt"):
            validate_result(result, self.packet, self.repo)

    def test_not_run_never_passes_and_checkpoint_stays_unchanged(self) -> None:
        candidate = self.commit_owned_change()
        passing = self.make_result(candidate)
        reviews = [self.make_review(candidate, lens) for lens in self.packet["required_review_lenses"]]
        packet_path, result_path, review_paths = self.write_records(passing, reviews)
        state = self.root / "state" / "checkpoint.json"
        advance_checkpoint(state, "reviewed", packet_path, result_path, review_paths, self.repo)
        before = state.read_bytes()
        failing = self.make_result(candidate, check_status="not_run")
        result_path.write_text(json.dumps(failing), encoding="utf-8")
        with self.assertRaisesRegex(ProtocolError, "did not pass"):
            advance_checkpoint(state, "accepted", packet_path, result_path, review_paths, self.repo)
        self.assertEqual(before, state.read_bytes())

    def test_stale_review_fails(self) -> None:
        candidate = self.commit_owned_change()
        stale = self.make_review(self.base, "code_hygiene")
        with self.assertRaisesRegex(ProtocolError, "stale"):
            validate_review(stale, self.packet, candidate)

    def test_recover_accepted_checkpoint_from_records(self) -> None:
        candidate = self.commit_owned_change()
        result = self.make_result(candidate)
        reviews = [self.make_review(candidate, lens) for lens in self.packet["required_review_lenses"]]
        packet_path, result_path, review_paths = self.write_records(result, reviews)
        state = self.root / "fresh-state" / "checkpoint.json"
        checkpoint = advance_checkpoint(state, "accepted", packet_path, result_path, review_paths, self.repo)
        self.assertEqual("accepted", checkpoint["stage"])
        self.assertEqual(candidate, json.loads(state.read_text())["candidate_sha"])
        validate_checkpoint(state, self.repo)
        validate_checkpoint(state, None)

    def test_checkpoint_replays_semantic_gates_and_rejects_header_tampering(self) -> None:
        candidate = self.commit_owned_change()
        state = self.root / "adversarial-state" / "checkpoint.json"

        failed = self.make_result(candidate, check_status="not_run")
        failed["outcome"] = "failed"
        packet_path, result_path, review_paths = self.write_records(failed, [])
        self.write_checkpoint_record(
            state, packet_path, result_path, review_paths, failed, [],
        )
        with self.assertRaisesRegex(ProtocolError, "required checks did not pass"):
            validate_checkpoint(state, None)
        script = Path(__file__).resolve().parents[1] / "scripts/orchestration/validate.py"
        cli = subprocess.run(
            [
                sys.executable,
                str(script),
                "--json",
                "checkpoint",
                str(state),
                "--offline-recovery",
            ],
            check=False,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        self.assertEqual(1, cli.returncode)
        self.assertIn("required checks did not pass", cli.stdout)

        passing = self.make_result(candidate)
        mismatched = copy.deepcopy(passing)
        mismatched["task_id"] = "other-task"
        result_path.write_text(json.dumps(mismatched), encoding="utf-8")
        self.write_checkpoint_record(
            state, packet_path, result_path, [], mismatched, [],
            stage="candidate_validated",
        )
        with self.assertRaisesRegex(ProtocolError, "result task_id/mode does not match packet"):
            validate_checkpoint(state, None)

        result_path.write_text(json.dumps(passing), encoding="utf-8")
        self.write_checkpoint_record(
            state, packet_path, result_path, [], passing, [],
        )
        with self.assertRaisesRegex(ProtocolError, "missing required review lenses"):
            validate_checkpoint(state, None)

        reviews = [
            self.make_review(candidate, lens)
            for lens in self.packet["required_review_lenses"]
        ]
        _, result_path, review_paths = self.write_records(passing, reviews)
        self.write_checkpoint_record(
            state,
            packet_path,
            result_path,
            review_paths,
            passing,
            reviews,
            task_id="tampered-task",
        )
        with self.assertRaisesRegex(ProtocolError, "checkpoint task_id"):
            validate_checkpoint(state, None)

        incomplete = copy.deepcopy(passing)
        incomplete["completion"]["source"] = "pending"
        result_path.write_text(json.dumps(incomplete), encoding="utf-8")
        self.write_checkpoint_record(
            state, packet_path, result_path, review_paths, incomplete, reviews,
        )
        with self.assertRaisesRegex(ProtocolError, "completion stages are incomplete"):
            validate_checkpoint(state, None)

    def test_checkpoint_detects_record_mutation(self) -> None:
        candidate = self.commit_owned_change()
        result = self.make_result(candidate)
        reviews = [self.make_review(candidate, lens) for lens in self.packet["required_review_lenses"]]
        packet_path, result_path, review_paths = self.write_records(result, reviews)
        state = self.root / "state" / "checkpoint.json"
        advance_checkpoint(state, "accepted", packet_path, result_path, review_paths, self.repo)
        result["evidence"][0]["summary"] = "Mutated after acceptance."
        result_path.write_text(json.dumps(result), encoding="utf-8")
        with self.assertRaisesRegex(ProtocolError, "content digest mismatch"):
            validate_checkpoint(state, self.repo)

    def test_no_change_is_distinct(self) -> None:
        packet = copy.deepcopy(self.packet)
        packet["ownership"]["write_paths"] = ["owned/"]
        result = self.make_result(self.base)
        result["packet_digest"] = digest_record(packet)
        result["outcome"] = "no_change"
        result["changed_paths"] = []
        validate_result(result, packet, self.repo)


if __name__ == "__main__":
    unittest.main()
