#!/usr/bin/env python3
"""Validate NeroCD's canonical, pinned skill inventory without modifying it."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import sys
from typing import Any


HASH_ALGORITHM = "sha256-path-content-v1"
HEX_SHA256 = re.compile(r"[0-9a-f]{64}\Z")
SKILL_ID = re.compile(r"[a-z0-9]+(?:-[a-z0-9]+)*\Z")
FORBIDDEN_INSTRUCTIONS = (
    "**Thinking mode:**",
    "**Orchestration mode:**",
    "ultrathink",
    "ultracode",
    "Spin up a **background agent**",
)
OVERBROAD_DESCRIPTIONS = (
    "any Go code changes",
    "any Go identifier",
    "adding or refactoring any Go function",
    "writing a simple if/else or for loop",
)
LOCK_BASE_FIELDS = {
    "source",
    "sourceType",
    "skillPath",
    "sourceCommit",
    "provenanceStatus",
    "normalizationVersion",
    "hashAlgorithm",
    "localHash",
}
PINNED_SOURCES = {
    "golang-security": {
        "source": "samber/cc-skills-golang",
        "sourceCommit": "bac46b0bed2677f840837e16be2c790341bda2df",
        "sourcePath": "skills/golang-security/SKILL.md",
        "license": "MIT",
        "normalizationVersion": 1,
        "upstreamLocalHash": "ed6b120dae82dff9267b58bb17186c4289643698e4284c51185bdc06b4b8078f",
    },
    "golang-testing": {
        "source": "samber/cc-skills-golang",
        "sourceCommit": "bac46b0bed2677f840837e16be2c790341bda2df",
        "sourcePath": "skills/golang-testing/SKILL.md",
        "license": "MIT",
        "normalizationVersion": 1,
        "upstreamLocalHash": "8dc90b6844c55a1bf424fbc86e51e63d22a2c2443950e52e041a62f2bc58ebc8",
    },
}
LOCAL_SKILLS = {"nerocd-runner-lifecycle", "nerocd-ui-artifact-verification"}


class ValidationError(Exception):
    """The skill inventory is inconsistent or unpinned."""


def _sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def package_hash(skill_dir: Path) -> str:
    """Return sha256-path-content-v1 for one symlink-free skill package."""
    entries: list[dict[str, Any]] = []
    for directory, dirnames, filenames in os.walk(skill_dir, followlinks=False):
        parent = Path(directory)
        for name in sorted(dirnames + filenames):
            path = parent / name
            mode = path.lstat().st_mode
            relative = path.relative_to(skill_dir).as_posix()
            if stat.S_ISLNK(mode):
                raise ValidationError(f"internal package symlink rejected: {skill_dir.name}/{relative}")
            if stat.S_ISDIR(mode):
                continue
            if not stat.S_ISREG(mode):
                raise ValidationError(f"non-regular package entry rejected: {skill_dir.name}/{relative}")
            entries.append(
                {
                    "executable": bool(mode & 0o111),
                    "path": relative,
                    "sha256": _sha256(path.read_bytes()),
                }
            )
    entries.sort(key=lambda entry: entry["path"])
    encoded = json.dumps(
        entries, sort_keys=True, separators=(",", ":"), ensure_ascii=True
    ).encode("utf-8")
    return _sha256(encoded)


def _frontmatter(skill_file: Path) -> dict[str, str]:
    try:
        text = skill_file.read_text(encoding="utf-8")
    except (OSError, UnicodeError) as exc:
        raise ValidationError(f"cannot read {skill_file}: {exc}") from exc
    lines = text.splitlines()
    if len(lines) < 4 or lines[0] != "---":
        raise ValidationError(f"missing frontmatter: {skill_file}")
    try:
        end = lines.index("---", 1)
    except ValueError as exc:
        raise ValidationError(f"unterminated frontmatter: {skill_file}") from exc
    result: dict[str, str] = {}
    for line in lines[1:end]:
        if not line or line[0].isspace() or ":" not in line:
            continue
        key, value = line.split(":", 1)
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] == '"':
            try:
                value = json.loads(value)
            except json.JSONDecodeError as exc:
                raise ValidationError(f"invalid quoted frontmatter in {skill_file}: {key}") from exc
        result[key.strip()] = value
    return result


def _implicit_invocation_policy(metadata: Path) -> bool:
    """Parse the one supported policy boolean without treating comments as data."""
    try:
        lines = metadata.read_text(encoding="utf-8").splitlines()
    except (OSError, UnicodeError) as exc:
        raise ValidationError(f"cannot read {metadata}: {exc}") from exc
    policy_headers = [
        index
        for index, line in enumerate(lines)
        if re.fullmatch(r"policy:\s*(?:#.*)?", line)
    ]
    policy_values = [
        (index, len(match.group(1)), match.group(2) == "true")
        for index, line in enumerate(lines)
        if (match := re.fullmatch(
            r"( +)allow_implicit_invocation:\s*(true|false)\s*(?:#.*)?", line
        ))
    ]
    if len(policy_headers) != 1 or len(policy_values) != 1:
        raise ValidationError(
            f"{metadata}: policy must declare allow_implicit_invocation exactly once"
        )
    header = policy_headers[0]
    next_top_level = next(
        (
            index
            for index in range(header + 1, len(lines))
            if lines[index].strip()
            and not lines[index].lstrip().startswith("#")
            and not lines[index][0].isspace()
        ),
        len(lines),
    )
    value_index, value_indent, value = policy_values[0]
    if not header < value_index < next_top_level:
        raise ValidationError(
            f"{metadata}: allow_implicit_invocation must be nested under policy"
        )
    active_policy_lines = [
        line
        for line in lines[header + 1 : next_top_level]
        if line.strip() and not line.lstrip().startswith("#")
    ]
    if any(line.startswith("\t") for line in active_policy_lines):
        raise ValidationError(f"{metadata}: policy indentation must use spaces")
    direct_child_indent = min(
        len(line) - len(line.lstrip(" ")) for line in active_policy_lines
    )
    if value_indent != direct_child_indent:
        raise ValidationError(
            f"{metadata}: allow_implicit_invocation must be a direct policy child"
        )
    return value


def _load_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise ValidationError(f"cannot decode {path}: {exc}") from exc


def _validate_skill(skill_id: str, skill_dir: Path) -> None:
    skill_file = skill_dir / "SKILL.md"
    if not skill_file.is_file():
        raise ValidationError(f"missing SKILL.md: {skill_id}")
    frontmatter = _frontmatter(skill_file)
    if frontmatter.get("name") != skill_id:
        raise ValidationError(f"frontmatter name mismatch: {skill_id}")
    description = frontmatter.get("description", "").strip()
    if not description:
        raise ValidationError(f"missing description: {skill_id}")
    if any(phrase in description for phrase in OVERBROAD_DESCRIPTIONS):
        raise ValidationError(f"overbroad description: {skill_id}")
    text = skill_file.read_text(encoding="utf-8")
    if any(phrase in text for phrase in FORBIDDEN_INSTRUCTIONS):
        raise ValidationError(f"skill chooses orchestration or reasoning policy: {skill_id}")

    metadata = skill_dir / "agents/openai.yaml"
    if skill_id == "research":
        if not metadata.is_file() or _implicit_invocation_policy(metadata) is not False:
            raise ValidationError("research must remain explicit-only")
    elif metadata.is_file() and _implicit_invocation_policy(metadata) is not True:
        raise ValidationError(f"interactive skill unexpectedly explicit-only: {skill_id}")


def _validate_provenance(skill_id: str, skill_dir: Path, record: dict[str, Any]) -> None:
    source_type = record.get("sourceType")
    expected_source_type = (
        "github-pinned"
        if skill_id in PINNED_SOURCES
        else "local" if skill_id in LOCAL_SKILLS else "github-legacy"
    )
    if source_type != expected_source_type:
        raise ValidationError(f"source type mismatch for {skill_id}")
    extra_fields = {
        "github-pinned": {"license", "upstreamLocalHash"},
        "github-legacy": {"computedHash"},
        "local": set(),
    }.get(source_type)
    if extra_fields is None:
        raise ValidationError(f"unknown source type for {skill_id}")
    expected_fields = LOCK_BASE_FIELDS | extra_fields
    missing = sorted(expected_fields - record.keys())
    extra = sorted(record.keys() - expected_fields)
    if missing:
        raise ValidationError(f"missing lock fields for {skill_id}: {', '.join(missing)}")
    if extra:
        raise ValidationError(f"unknown lock fields for {skill_id}: {', '.join(extra)}")
    if not isinstance(record["source"], str) or not record["source"].strip():
        raise ValidationError(f"invalid source for {skill_id}")
    if not isinstance(record["skillPath"], str) or not record["skillPath"].strip():
        raise ValidationError(f"invalid skill path for {skill_id}")
    if record["hashAlgorithm"] != HASH_ALGORITHM:
        raise ValidationError(f"unsupported hash algorithm for {skill_id}")
    if type(record["normalizationVersion"]) is not int or record["normalizationVersion"] != 1:
        raise ValidationError(f"invalid normalization version for {skill_id}")
    if not isinstance(record["localHash"], str) or HEX_SHA256.fullmatch(record["localHash"]) is None:
        raise ValidationError(f"invalid local hash for {skill_id}")
    computed = record.get("computedHash")
    if computed is not None and (not isinstance(computed, str) or HEX_SHA256.fullmatch(computed) is None):
        raise ValidationError(f"invalid installer computedHash for {skill_id}")
    commit = record["sourceCommit"]
    if commit is not None and (not isinstance(commit, str) or re.fullmatch(r"[0-9a-f]{40}", commit) is None):
        raise ValidationError(f"invalid source commit for {skill_id}")

    source_file = skill_dir / "SOURCE.json"
    if source_type == "github-pinned":
        if (
            not isinstance(record["upstreamLocalHash"], str)
            or HEX_SHA256.fullmatch(record["upstreamLocalHash"]) is None
        ):
            raise ValidationError(f"invalid upstream package hash for {skill_id}")
        if record["license"] != "MIT":
            raise ValidationError(f"invalid pinned license for {skill_id}")
        expected = PINNED_SOURCES.get(skill_id)
        if expected is None:
            raise ValidationError(f"unexpected pinned source package: {skill_id}")
        expected_lock = {
            "source": expected["source"],
            "sourceType": "github-pinned",
            "skillPath": expected["sourcePath"],
            "sourceCommit": expected["sourceCommit"],
            "provenanceStatus": "pinned-upstream-normalized",
            "normalizationVersion": expected["normalizationVersion"],
            "license": expected["license"],
            "upstreamLocalHash": expected["upstreamLocalHash"],
            "hashAlgorithm": HASH_ALGORITHM,
            "localHash": record["localHash"],
        }
        if record != expected_lock:
            mismatched = sorted(
                key for key in expected_lock if record.get(key) != expected_lock[key]
            )
            raise ValidationError(
                f"pinned provenance mismatch for {skill_id}: {', '.join(mismatched)}"
            )
        if not source_file.is_file():
            raise ValidationError(f"missing pinned source metadata for {skill_id}")
        source = _load_json(source_file)
        if source != expected:
            mismatched = sorted(
                set(source) ^ set(expected)
                | {key for key in set(source) & set(expected) if source[key] != expected[key]}
            ) if isinstance(source, dict) else ["record"]
            raise ValidationError(
                f"source metadata mismatch for {skill_id}: {', '.join(mismatched)}"
            )
        if record["skillPath"] != source["sourcePath"]:
            raise ValidationError(f"source path mismatch for {skill_id}")
        if not (skill_dir / "LICENSE").is_file():
            raise ValidationError(f"missing MIT attribution for {skill_id}")
    elif source_type == "github-legacy":
        expected_source = "mattpocock/skills" if skill_id == "research" else "cxuu/golang-skills"
        expected_path = (
            "skills/engineering/research/SKILL.md"
            if skill_id == "research"
            else f"skills/{skill_id}/SKILL.md"
        )
        if (
            commit is not None
            or record["source"] != expected_source
            or record["skillPath"] != expected_path
            or record["provenanceStatus"] != "upstream-commit-unknown"
        ):
            raise ValidationError(f"legacy provenance must remain explicitly unknown: {skill_id}")
    elif source_type == "local":
        if (
            commit is not None
            or record["source"] != "NeroCD"
            or record["skillPath"] != f".agents/skills/{skill_id}/SKILL.md"
            or record["provenanceStatus"] != "repository-local"
        ):
            raise ValidationError(f"invalid local provenance: {skill_id}")


def _validate_scenarios(root: Path, inventory: set[str]) -> list[dict[str, Any]]:
    payload = _load_json(root / "scripts/skills/selection-scenarios.json")
    if not isinstance(payload, dict) or payload.get("version") != 1:
        raise ValidationError("invalid selection scenario fixture")
    scenarios = payload.get("scenarios")
    if not isinstance(scenarios, list) or not scenarios:
        raise ValidationError("selection scenario fixture is empty")
    kinds: set[str] = set()
    names: set[str] = set()
    for scenario in scenarios:
        if not isinstance(scenario, dict):
            raise ValidationError("selection scenario must be an object")
        name = scenario.get("name")
        kind = scenario.get("kind")
        expected = scenario.get("expectedSkills")
        if not isinstance(name, str) or not name or name in names:
            raise ValidationError("selection scenario names must be unique")
        if kind not in {"positive", "negative", "overlap"}:
            raise ValidationError(f"invalid selection scenario kind: {name}")
        if not isinstance(expected, list) or any(not isinstance(item, str) for item in expected):
            raise ValidationError(f"invalid expected skill list: {name}")
        if len(expected) != len(set(expected)) or not set(expected) <= inventory:
            raise ValidationError(f"unknown or duplicate expected skill: {name}")
        if kind == "negative" and expected:
            raise ValidationError(f"negative scenario selects a skill: {name}")
        if kind == "overlap" and len(expected) < 2:
            raise ValidationError(f"overlap scenario needs multiple skills: {name}")
        names.add(name)
        kinds.add(kind)
    if kinds != {"positive", "negative", "overlap"}:
        raise ValidationError("selection scenarios must cover positive, negative, and overlap cases")
    return scenarios


def validate_repository(root: Path) -> tuple[dict[str, str], list[dict[str, Any]]]:
    root = root.resolve()
    canonical = root / ".agents/skills"
    lock = _load_json(root / "skills-lock.json")
    if (
        not isinstance(lock, dict)
        or lock.get("version") != 1
        or lock.get("hashAlgorithm") != HASH_ALGORITHM
        or not isinstance(lock.get("skills"), dict)
    ):
        raise ValidationError("skills-lock.json must use version 1 with a skills object")
    records: dict[str, Any] = lock["skills"]
    inventory = {
        path.name
        for path in canonical.iterdir()
        if path.is_dir() and not path.is_symlink()
    }
    if any(SKILL_ID.fullmatch(skill_id) is None for skill_id in inventory):
        raise ValidationError("canonical inventory contains an invalid skill ID")
    if inventory != set(records):
        missing = sorted(set(records) - inventory)
        extra = sorted(inventory - set(records))
        raise ValidationError(f"canonical inventory mismatch: missing={missing} extra={extra}")
    duplicate = root / "agent/skills"
    if duplicate.exists() and any(duplicate.iterdir()):
        raise ValidationError("duplicate agent/skills tree must not exist")

    hashes: dict[str, str] = {}
    for skill_id in sorted(inventory):
        skill_dir = canonical / skill_id
        record = records[skill_id]
        if not isinstance(record, dict):
            raise ValidationError(f"invalid lock record: {skill_id}")
        _validate_skill(skill_id, skill_dir)
        _validate_provenance(skill_id, skill_dir, record)
        local_hash = package_hash(skill_dir)
        if local_hash != record["localHash"]:
            raise ValidationError(f"local hash mismatch: {skill_id}")
        hashes[skill_id] = local_hash

        link = root / ".claude/skills" / skill_id
        expected_target = f"../../.agents/skills/{skill_id}"
        if not link.is_symlink() or os.readlink(link) != expected_target:
            raise ValidationError(f"broken compatibility link: {skill_id}")
        try:
            if link.resolve(strict=True) != skill_dir.resolve(strict=True):
                raise ValidationError(f"misdirected compatibility link: {skill_id}")
        except OSError as exc:
            raise ValidationError(f"broken compatibility link: {skill_id}") from exc

    links = {path.name for path in (root / ".claude/skills").iterdir() if path.is_symlink()}
    if links != inventory:
        raise ValidationError("compatibility link inventory differs from canonical inventory")
    scenarios = _validate_scenarios(root, inventory)
    return hashes, scenarios


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--json", action="store_true", help="emit only the pinned skill hash map")
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[2]
    try:
        hashes, scenarios = validate_repository(root)
    except ValidationError as exc:
        print(f"skill validation failed: {exc}", file=sys.stderr)
        return 1
    if args.json:
        print(json.dumps({"skills": hashes}, sort_keys=True, separators=(",", ":")))
    else:
        print(f"validated {len(hashes)} canonical skills with {HASH_ALGORITHM}")
        for scenario in scenarios:
            expected = ", ".join(scenario["expectedSkills"]) or "none"
            print(f"scenario {scenario['kind']}: {scenario['name']} -> {expected}")
        print("scenario expectations are static routing fixtures; runtime skill selection requires independent behavioral evaluation")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
