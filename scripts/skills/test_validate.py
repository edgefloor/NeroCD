#!/usr/bin/env python3
"""Mutation tests for the read-only skill inventory validator."""

from __future__ import annotations

import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("validate.py")
REPO_ROOT = SCRIPT.parents[2]
SPEC = importlib.util.spec_from_file_location("nerocd_skill_validate", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
VALIDATE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(VALIDATE)


class SkillValidatorTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "repo"
        self.root.mkdir()
        shutil.copytree(REPO_ROOT / ".agents", self.root / ".agents", symlinks=True)
        shutil.copytree(REPO_ROOT / ".claude", self.root / ".claude", symlinks=True)
        shutil.copytree(REPO_ROOT / "scripts/skills", self.root / "scripts/skills")
        shutil.copy2(REPO_ROOT / "skills-lock.json", self.root / "skills-lock.json")

    def load_lock(self) -> dict:
        return json.loads((self.root / "skills-lock.json").read_text(encoding="utf-8"))

    def save_lock(self, lock: dict) -> None:
        (self.root / "skills-lock.json").write_text(
            json.dumps(lock, indent=2) + "\n", encoding="utf-8"
        )

    def refresh_local_hash(self, skill_id: str, lock: dict) -> None:
        lock["skills"][skill_id]["localHash"] = VALIDATE.package_hash(
            self.root / ".agents/skills" / skill_id
        )

    def test_live_inventory_validates(self) -> None:
        hashes, scenarios = VALIDATE.validate_repository(REPO_ROOT)
        self.assertEqual(set(hashes), {path.name for path in (REPO_ROOT / ".agents/skills").iterdir() if path.is_dir()})
        self.assertEqual({scenario["kind"] for scenario in scenarios}, {"positive", "negative", "overlap"})

    def test_digest_mismatch_is_rejected(self) -> None:
        skill = self.root / ".agents/skills/go-context/SKILL.md"
        skill.write_text(skill.read_text(encoding="utf-8") + "\nmutation\n", encoding="utf-8")
        with self.assertRaisesRegex(VALIDATE.ValidationError, "local hash mismatch: go-context"):
            VALIDATE.validate_repository(self.root)

    def test_broken_compatibility_link_is_rejected(self) -> None:
        link = self.root / ".claude/skills/go-context"
        link.unlink()
        link.symlink_to("../../.agents/skills/go-naming")
        with self.assertRaisesRegex(VALIDATE.ValidationError, "compatibility link: go-context"):
            VALIDATE.validate_repository(self.root)

    def test_duplicate_tree_is_rejected(self) -> None:
        duplicate = self.root / "agent/skills/go-context"
        duplicate.mkdir(parents=True)
        (duplicate / "SKILL.md").write_text("duplicate", encoding="utf-8")
        with self.assertRaisesRegex(VALIDATE.ValidationError, "duplicate agent/skills"):
            VALIDATE.validate_repository(self.root)

    def test_internal_package_symlink_is_rejected(self) -> None:
        target = self.root / ".agents/skills/go-context/SKILL.md"
        link = self.root / ".agents/skills/go-context/references/internal-link"
        try:
            link.symlink_to(target)
        except OSError as exc:
            self.skipTest(f"symlinks unavailable: {exc}")
        with self.assertRaisesRegex(VALIDATE.ValidationError, "internal package symlink"):
            VALIDATE.validate_repository(self.root)

    def test_research_policy_uses_active_boolean_not_comment_text(self) -> None:
        metadata = self.root / ".agents/skills/research/agents/openai.yaml"
        text = metadata.read_text(encoding="utf-8").replace(
            "  allow_implicit_invocation: false",
            "  allow_implicit_invocation: true\n  # allow_implicit_invocation: false",
        )
        metadata.write_text(text, encoding="utf-8")
        lock = self.load_lock()
        self.refresh_local_hash("research", lock)
        self.save_lock(lock)
        with self.assertRaisesRegex(VALIDATE.ValidationError, "research must remain explicit-only"):
            VALIDATE.validate_repository(self.root)

    def test_duplicate_or_contradictory_policy_values_are_rejected(self) -> None:
        metadata = self.root / ".agents/skills/research/agents/openai.yaml"
        metadata.write_text(
            metadata.read_text(encoding="utf-8")
            + "  allow_implicit_invocation: true\n",
            encoding="utf-8",
        )
        lock = self.load_lock()
        self.refresh_local_hash("research", lock)
        self.save_lock(lock)
        with self.assertRaisesRegex(VALIDATE.ValidationError, "exactly once"):
            VALIDATE.validate_repository(self.root)

    def test_nested_policy_value_is_rejected(self) -> None:
        metadata = self.root / ".agents/skills/research/agents/openai.yaml"
        text = metadata.read_text(encoding="utf-8").replace(
            "policy:\n  allow_implicit_invocation: false",
            "policy:\n  nested:\n    allow_implicit_invocation: false",
        )
        metadata.write_text(text, encoding="utf-8")
        lock = self.load_lock()
        self.refresh_local_hash("research", lock)
        self.save_lock(lock)
        with self.assertRaisesRegex(VALIDATE.ValidationError, "direct policy child"):
            VALIDATE.validate_repository(self.root)

    def test_pinned_lock_status_license_and_path_are_enforced(self) -> None:
        for field, value in (
            ("provenanceStatus", "unverified"),
            ("license", "Apache-2.0"),
            ("skillPath", "skills/elsewhere/SKILL.md"),
        ):
            with self.subTest(field=field):
                lock = self.load_lock()
                lock["skills"]["golang-security"][field] = value
                self.save_lock(lock)
                expected = (
                    "invalid pinned license"
                    if field == "license"
                    else "pinned provenance mismatch"
                )
                with self.assertRaisesRegex(VALIDATE.ValidationError, expected):
                    VALIDATE.validate_repository(self.root)
                shutil.copy2(REPO_ROOT / "skills-lock.json", self.root / "skills-lock.json")

    def test_pinned_source_path_cannot_be_mutated_with_matching_lock(self) -> None:
        skill_id = "golang-security"
        source_path = self.root / f".agents/skills/{skill_id}/SOURCE.json"
        source = json.loads(source_path.read_text(encoding="utf-8"))
        source["sourcePath"] = "skills/elsewhere/SKILL.md"
        source_path.write_text(json.dumps(source, indent=2) + "\n", encoding="utf-8")
        lock = self.load_lock()
        lock["skills"][skill_id]["skillPath"] = source["sourcePath"]
        self.refresh_local_hash(skill_id, lock)
        self.save_lock(lock)
        with self.assertRaisesRegex(VALIDATE.ValidationError, "pinned provenance mismatch"):
            VALIDATE.validate_repository(self.root)

    def test_pinned_upstream_hash_must_be_audited_sha256(self) -> None:
        skill_id = "golang-testing"
        source_path = self.root / f".agents/skills/{skill_id}/SOURCE.json"
        source = json.loads(source_path.read_text(encoding="utf-8"))
        source["upstreamLocalHash"] = "not-a-hash"
        source_path.write_text(json.dumps(source, indent=2) + "\n", encoding="utf-8")
        lock = self.load_lock()
        lock["skills"][skill_id]["upstreamLocalHash"] = "not-a-hash"
        self.refresh_local_hash(skill_id, lock)
        self.save_lock(lock)
        with self.assertRaisesRegex(VALIDATE.ValidationError, "invalid upstream package hash"):
            VALIDATE.validate_repository(self.root)

    def test_json_contract_is_exact_and_read_only(self) -> None:
        before = subprocess.run(
            ["git", "status", "--porcelain=v1"], cwd=REPO_ROOT, check=True, stdout=subprocess.PIPE
        ).stdout
        env = os.environ.copy()
        env["PYTHONDONTWRITEBYTECODE"] = "1"
        result = subprocess.run(
            ["python3", str(SCRIPT), "--json"], cwd=REPO_ROOT, env=env,
            check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
        payload = json.loads(result.stdout)
        self.assertEqual(result.stdout, json.dumps(payload, sort_keys=True, separators=(",", ":")) + "\n")
        self.assertEqual(set(payload), {"skills"})
        self.assertTrue(all(len(value) == 64 for value in payload["skills"].values()))
        after = subprocess.run(
            ["git", "status", "--porcelain=v1"], cwd=REPO_ROOT, check=True, stdout=subprocess.PIPE
        ).stdout
        self.assertEqual(after, before)


if __name__ == "__main__":
    unittest.main()
