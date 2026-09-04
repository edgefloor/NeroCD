"""Validation for NeroCD delegation records.

This module deliberately validates records and repository state only.  It does not
schedule agents, execute commands found in records, or grant permissions.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import subprocess
import tempfile
import tomllib
from pathlib import Path, PurePosixPath
from typing import Any, Iterable


VERSION = 1
MODES = {"ORCHESTRATOR", "WORKER", "REVIEWER", "INTEGRATOR"}
OUTCOMES = {"changed", "no_change", "blocked", "failed"}
CHECK_STATUSES = {"passed", "failed", "not_run"}
REVIEW_STATUSES = {"passed", "failed"}
COMPLETION_STATUSES = {"complete", "pending", "failed", "not_applicable"}
COMPLETION_STAGES = ("source", "artifact", "published", "deployed")
CHECKPOINT_STAGES = ("planned", "candidate_validated", "reviewed", "accepted")
SHA_RE = re.compile(r"^[0-9a-f]{40}$")
SKILL_HASH_RE = re.compile(r"^[0-9a-f]{64}$")
MAX_RECORD_BYTES = 1024 * 1024

ROLE_ROUTES = {
    "read_only_explorer": ("gpt-5.6-terra", "medium", "read-only"),
    "mechanical_worker": ("gpt-5.6-luna", "low", "workspace-write"),
    "bounded_worker": ("gpt-5.6-terra", "medium", "workspace-write"),
    "complex_worker": ("gpt-5.6-sol", "medium", "workspace-write"),
    "critical_worker": ("gpt-5.6-sol", "high", "workspace-write"),
    "integration_steward": ("gpt-5.6-terra", "medium", "workspace-write"),
    "code_hygiene_reviewer": ("gpt-5.6-terra", "medium", "read-only"),
    "specification_reviewer": ("gpt-5.6-terra", "medium", "read-only"),
    "cryptographic_correctness_reviewer": ("gpt-5.6-terra", "high", "read-only"),
    "critical_security_reviewer": ("gpt-5.6-terra", "high", "read-only"),
    "escalation_resolver": ("gpt-5.6-sol", "high", "workspace-write"),
}
ROLE_MODES = {
    "read_only_explorer": "ORCHESTRATOR",
    "mechanical_worker": "WORKER",
    "bounded_worker": "WORKER",
    "complex_worker": "WORKER",
    "critical_worker": "WORKER",
    "integration_steward": "INTEGRATOR",
    "code_hygiene_reviewer": "REVIEWER",
    "specification_reviewer": "REVIEWER",
    "cryptographic_correctness_reviewer": "REVIEWER",
    "critical_security_reviewer": "REVIEWER",
    "escalation_resolver": "WORKER",
}


class ProtocolError(ValueError):
    """A record or repository state violates the protocol."""


def _fail(message: str) -> None:
    raise ProtocolError(message)


def load_json(path: Path) -> dict[str, Any]:
    try:
        if path.stat().st_size > MAX_RECORD_BYTES:
            _fail(f"record exceeds {MAX_RECORD_BYTES} byte limit: {path}")
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        _fail(f"cannot load JSON {path}: {exc}")
    if not isinstance(value, dict):
        _fail(f"record must be a JSON object: {path}")
    return value


def _expect_keys(value: dict[str, Any], required: set[str], allowed: set[str], where: str) -> None:
    missing = sorted(required - value.keys())
    extra = sorted(value.keys() - allowed)
    if missing:
        _fail(f"{where}: missing fields: {', '.join(missing)}")
    if extra:
        _fail(f"{where}: unknown fields: {', '.join(extra)}")


def _text(value: Any, where: str) -> str:
    if not isinstance(value, str) or not value.strip():
        _fail(f"{where}: expected non-empty string")
    return value


def _string_list(value: Any, where: str, *, nonempty: bool = False) -> list[str]:
    if not isinstance(value, list) or (nonempty and not value):
        _fail(f"{where}: expected {'non-empty ' if nonempty else ''}array")
    result: list[str] = []
    for index, item in enumerate(value):
        result.append(_text(item, f"{where}[{index}]"))
    if len(set(result)) != len(result):
        _fail(f"{where}: duplicate values")
    return result


def _versioned(record: dict[str, Any], record_type: str) -> None:
    if record.get("record_type") != record_type:
        _fail(f"expected record_type {record_type!r}")
    if type(record.get("schema_version")) is not int or record.get("schema_version") != VERSION:
        _fail(f"unsupported schema_version: {record.get('schema_version')!r}")


def _sha(value: Any, where: str) -> str:
    value = _text(value, where)
    if not SHA_RE.fullmatch(value):
        _fail(f"{where}: expected full lowercase 40-character Git SHA")
    return value


def _relative_path(value: Any, where: str, *, directory: bool = False) -> str:
    value = _text(value, where)
    if "\\" in value:
        _fail(f"{where}: backslashes are not valid protocol path separators")
    if value.startswith("/") or value.startswith("~"):
        _fail(f"{where}: path must be repository-relative")
    path = PurePosixPath(value.rstrip("/"))
    if not path.parts or any(part in {"", ".", ".."} for part in path.parts):
        _fail(f"{where}: invalid repository-relative path")
    if path.parts[0] == ".git":
        _fail(f"{where}: .git is never an allowed ownership target")
    normalized = path.as_posix()
    return normalized + "/" if directory or value.endswith("/") else normalized


def _path_specs(value: Any, where: str, *, nonempty: bool = False) -> list[str]:
    raw = _string_list(value, where, nonempty=nonempty)
    normalized = [
        _relative_path(item, f"{where}[{index}]", directory=item.endswith("/"))
        for index, item in enumerate(raw)
    ]
    if normalized != raw:
        _fail(f"{where}: paths must use canonical repository-relative POSIX spelling")
    return normalized


def _covers(spec: str, path: str) -> bool:
    return path.startswith(spec) if spec.endswith("/") else path == spec


def ownership_conflicts(packets: Iterable[dict[str, Any]]) -> list[str]:
    indexed: list[tuple[str, list[str]]] = []
    for packet in packets:
        task_id = _text(packet.get("task_id"), "packet.task_id")
        writes = _path_specs(packet.get("ownership", {}).get("write_paths"), "packet.ownership.write_paths")
        indexed.append((task_id, writes))
    conflicts: list[str] = []
    for left_index, (left_id, left_paths) in enumerate(indexed):
        for right_id, right_paths in indexed[left_index + 1 :]:
            for left in left_paths:
                for right in right_paths:
                    left_base = left.rstrip("/")
                    right_base = right.rstrip("/")
                    if (
                        left_base == right_base
                        or right_base.startswith(left_base + "/")
                        or left_base.startswith(right_base + "/")
                    ):
                        conflicts.append(f"{left_id}:{left} conflicts with {right_id}:{right}")
    return conflicts


def validate_packet(packet: dict[str, Any]) -> dict[str, Any]:
    _versioned(packet, "packet")
    required = {
        "record_type", "schema_version", "task_id", "mode", "goal", "repository",
        "ownership", "interfaces", "constraints", "validation", "handoff", "route",
        "authorization", "selected_skills", "required_review_lenses", "required_completion_stages",
    }
    _expect_keys(packet, required, required, "packet")
    _text(packet["task_id"], "packet.task_id")
    _text(packet["goal"], "packet.goal")
    if packet["mode"] not in MODES:
        _fail(f"packet.mode: unsupported mode {packet['mode']!r}")

    repository = packet["repository"]
    if not isinstance(repository, dict):
        _fail("packet.repository: expected object")
    _expect_keys(
        repository,
        {"workspace_root", "base_sha", "branch"},
        {"workspace_root", "base_sha", "branch"},
        "packet.repository",
    )
    workspace_root = Path(_text(repository["workspace_root"], "packet.repository.workspace_root"))
    if not workspace_root.is_absolute():
        _fail("packet.repository.workspace_root: expected absolute path")
    _sha(repository["base_sha"], "packet.repository.base_sha")
    _text(repository["branch"], "packet.repository.branch")

    ownership = packet["ownership"]
    if not isinstance(ownership, dict):
        _fail("packet.ownership: expected object")
    _expect_keys(ownership, {"read_paths", "write_paths"}, {"read_paths", "write_paths"}, "packet.ownership")
    _path_specs(ownership["read_paths"], "packet.ownership.read_paths")
    writes = _path_specs(ownership["write_paths"], "packet.ownership.write_paths")
    if packet["mode"] in {"WORKER", "INTEGRATOR"} and not writes:
        _fail("packet.ownership.write_paths: writers and integrators require bounded ownership")
    if packet["mode"] in {"ORCHESTRATOR", "REVIEWER"} and writes:
        _fail("packet.ownership.write_paths: read-only modes cannot own writes")

    _string_list(packet["interfaces"], "packet.interfaces")
    _string_list(packet["constraints"], "packet.constraints")
    _string_list(packet["validation"], "packet.validation", nonempty=True)
    _string_list(packet["handoff"], "packet.handoff", nonempty=True)
    selected_skills = packet["selected_skills"]
    if not isinstance(selected_skills, dict):
        _fail("packet.selected_skills: expected object mapping skill IDs to hashes")
    for skill_id, local_hash in selected_skills.items():
        _text(skill_id, "packet.selected_skills key")
        _text(local_hash, f"packet.selected_skills.{skill_id}")
        if not SKILL_HASH_RE.fullmatch(local_hash):
            _fail(f"packet.selected_skills.{skill_id}: expected raw lowercase SHA-256")
    review_lenses = _string_list(packet["required_review_lenses"], "packet.required_review_lenses")
    if packet["mode"] in {"WORKER", "INTEGRATOR"} and not review_lenses:
        _fail("packet.required_review_lenses: writable work requires independent review")
    completion = _string_list(packet["required_completion_stages"], "packet.required_completion_stages")
    unknown_completion = sorted(set(completion) - set(COMPLETION_STAGES))
    if unknown_completion:
        _fail(f"packet.required_completion_stages: unknown stages: {', '.join(unknown_completion)}")

    route = packet["route"]
    if not isinstance(route, dict):
        _fail("packet.route: expected object")
    route_keys = {"role", "model", "effort", "required_tools"}
    _expect_keys(route, route_keys, route_keys, "packet.route")
    role = _text(route["role"], "packet.route.role")
    if role not in ROLE_ROUTES:
        _fail(f"packet.route.role: unsupported role {role!r}")
    model, effort, sandbox = ROLE_ROUTES[role]
    if (route["model"], route["effort"]) != (model, effort):
        _fail(f"packet.route: {role} requires model={model} effort={effort}")
    if ROLE_MODES[role] != packet["mode"]:
        _fail(f"packet.mode {packet['mode']} is incompatible with role {role}; expected {ROLE_MODES[role]}")
    expected_mode = "read-only" if packet["mode"] in {"ORCHESTRATOR", "REVIEWER"} else "workspace-write"
    if sandbox != expected_mode:
        _fail(f"packet.mode {packet['mode']} is incompatible with role {role}")
    _string_list(route["required_tools"], "packet.route.required_tools")

    authorization = packet["authorization"]
    if not isinstance(authorization, dict):
        _fail("packet.authorization: expected object")
    _expect_keys(authorization, {"grants"}, {"grants"}, "packet.authorization")
    if not isinstance(authorization["grants"], list):
        _fail("packet.authorization.grants: expected array")
    for index, grant in enumerate(authorization["grants"]):
        where = f"packet.authorization.grants[{index}]"
        if not isinstance(grant, dict):
            _fail(f"{where}: expected object")
        _expect_keys(grant, {"action", "target", "granted_by"}, {"action", "target", "granted_by"}, where)
        _text(grant["action"], f"{where}.action")
        _text(grant["target"], f"{where}.target")
        _text(grant["granted_by"], f"{where}.granted_by")
    return packet


def validate_capabilities(capabilities: dict[str, Any]) -> dict[str, Any]:
    _versioned(capabilities, "runtime_capabilities")
    required = {"record_type", "schema_version", "attested", "source", "models", "tools", "permissions"}
    _expect_keys(capabilities, required, required, "runtime_capabilities")
    if capabilities["attested"] is not True:
        _fail("runtime_capabilities.attested must be true")
    _text(capabilities["source"], "runtime_capabilities.source")
    models = capabilities["models"]
    if not isinstance(models, dict):
        _fail("runtime_capabilities.models: expected object")
    for model, efforts in models.items():
        _text(model, "runtime_capabilities.models key")
        _string_list(efforts, f"runtime_capabilities.models.{model}", nonempty=True)
    _string_list(capabilities["tools"], "runtime_capabilities.tools")
    permissions = capabilities["permissions"]
    if not isinstance(permissions, dict):
        _fail("runtime_capabilities.permissions: expected object")
    permission_keys = {"repo_read", "workspace_write"}
    _expect_keys(permissions, permission_keys, permission_keys, "runtime_capabilities.permissions")
    if not all(isinstance(permissions[key], bool) for key in permissions):
        _fail("runtime_capabilities.permissions values must be booleans")
    return capabilities


def run_skills_validator(path: Path) -> dict[str, str]:
    try:
        process = subprocess.run(
            ["python3", str(path), "--json"], check=False, stdout=subprocess.PIPE,
            stderr=subprocess.PIPE, timeout=10,
        )
    except subprocess.TimeoutExpired:
        _fail("skills validator exceeded 10 second timeout")
    if process.returncode:
        _fail(f"skills validator failed: {process.stderr.decode(errors='replace').strip()}")
    if len(process.stdout) > MAX_RECORD_BYTES:
        _fail("skills validator output exceeds compact record limit")
    try:
        payload = json.loads(process.stdout)
    except json.JSONDecodeError as exc:
        _fail(f"skills validator returned invalid JSON: {exc}")
    if not isinstance(payload, dict) or set(payload) != {"skills"} or not isinstance(payload["skills"], dict):
        _fail("skills validator must return exactly {\"skills\": {id: localHash}}")
    skills: dict[str, str] = {}
    for skill_id, local_hash in payload["skills"].items():
        _text(skill_id, "skills validator skill ID")
        if not isinstance(local_hash, str) or not SKILL_HASH_RE.fullmatch(local_hash):
            _fail(f"skills validator returned invalid hash for {skill_id}")
        skills[skill_id] = local_hash
    return skills


def preflight(
    packets: list[dict[str, Any]],
    capabilities: dict[str, Any],
    repos: list[Path] | None = None,
    installed_skills: dict[str, str] | None = None,
) -> None:
    validate_capabilities(capabilities)
    if not packets:
        _fail("preflight requires at least one packet")
    if repos is not None and len(repos) != len(packets):
        _fail("preflight requires one repository root per packet")
    for index, packet in enumerate(packets):
        validate_packet(packet)
        route = packet["route"]
        efforts = capabilities["models"].get(route["model"])
        if efforts is None or route["effort"] not in efforts:
            _fail(f"runtime does not attest route {route['model']}/{route['effort']}")
        missing = sorted(set(route["required_tools"]) - set(capabilities["tools"]))
        if missing:
            _fail(f"runtime is missing required tools: {', '.join(missing)}")
        if not capabilities["permissions"]["repo_read"]:
            _fail("runtime does not attest repository read permission")
        if packet["mode"] in {"WORKER", "INTEGRATOR"} and not capabilities["permissions"]["workspace_write"]:
            _fail(f"runtime does not attest workspace-write permission for {packet['task_id']}")
        selected = packet["selected_skills"]
        if selected and installed_skills is None:
            _fail("selected skills require hashes from the canonical skills validator")
        mismatched = (
            [
                skill_id
                for skill_id, expected in selected.items()
                if installed_skills.get(skill_id) != expected
            ]
            if installed_skills is not None
            else []
        )
        if mismatched:
            _fail("selected skill hash mismatch or missing skill: " + ", ".join(sorted(mismatched)))
        if repos is not None:
            validate_preflight_repository(packet, repos[index])
    conflicts = ownership_conflicts(packets)
    if conflicts:
        _fail("overlapping write ownership: " + "; ".join(conflicts))


def _git(repo: Path, *args: str) -> str:
    process = subprocess.run(
        ["git", *args], cwd=repo, check=False, text=True, stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if process.returncode:
        _fail(f"git {' '.join(args)} failed: {process.stderr.strip()}")
    return process.stdout


def _commit_exists(repo: Path, sha: str, where: str) -> None:
    process = subprocess.run(
        ["git", "cat-file", "-e", f"{sha}^{{commit}}"], cwd=repo, check=False,
        stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True,
    )
    if process.returncode:
        _fail(f"{where}: Git commit does not exist: {sha}")


def validate_preflight_repository(packet: dict[str, Any], repo: Path) -> None:
    repo = repo.resolve()
    if Path(packet["repository"]["workspace_root"]).resolve() != repo:
        _fail(f"preflight --repo-root does not match packet workspace_root for {packet['task_id']}")
    if Path(_git(repo, "rev-parse", "--show-toplevel").strip()).resolve() != repo:
        _fail(f"preflight --repo-root is not the Git toplevel for {packet['task_id']}")
    base = packet["repository"]["base_sha"]
    _commit_exists(repo, base, "packet.repository.base_sha")
    if _git(repo, "branch", "--show-current").strip() != packet["repository"]["branch"]:
        _fail(f"preflight branch mismatch for {packet['task_id']}")
    if _git(repo, "rev-parse", "HEAD").strip() != base:
        _fail(f"preflight HEAD must equal packet base for {packet['task_id']}")
    if _git(repo, "status", "--porcelain", "--untracked-files=all"):
        _fail(f"preflight repository is dirty for {packet['task_id']}")


def changed_paths(repo: Path, base_sha: str, candidate_sha: str) -> list[str]:
    output = subprocess.run(
        ["git", "diff", "--name-status", "-z", "--find-renames", base_sha, candidate_sha],
        cwd=repo, check=False, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    if output.returncode:
        _fail(f"git diff failed: {output.stderr.decode(errors='replace').strip()}")
    fields = output.stdout.decode("utf-8", errors="strict").split("\0")
    if fields and fields[-1] == "":
        fields.pop()
    paths: list[str] = []
    index = 0
    while index < len(fields):
        status = fields[index]
        index += 1
        count = 2 if status.startswith(("R", "C")) else 1
        if index + count > len(fields):
            _fail("unexpected git --name-status output")
        for path in fields[index : index + count]:
            paths.append(_relative_path(path, "git changed path"))
        index += count
    return sorted(set(paths))


def _validate_repository_state(
    packet: dict[str, Any],
    result: dict[str, Any],
    repo: Path,
) -> list[str]:
    repo = repo.resolve()
    git_toplevel = Path(_git(repo, "rev-parse", "--show-toplevel").strip()).resolve()
    if git_toplevel != repo:
        _fail("result --repo-root must be the Git toplevel")
    packet_root = Path(packet["repository"]["workspace_root"]).resolve()
    if repo != packet_root:
        _fail("result --repo-root does not match packet workspace_root")
    base = packet["repository"]["base_sha"]
    candidate = result["candidate_sha"]
    _commit_exists(repo, base, "packet.repository.base_sha")
    _commit_exists(repo, candidate, "result.candidate_sha")
    if _git(repo, "branch", "--show-current").strip() != packet["repository"]["branch"]:
        _fail("repository branch does not match packet.repository.branch")
    if _git(repo, "rev-parse", "HEAD").strip() != candidate:
        _fail("repository HEAD does not match result.candidate_sha")
    ancestor = subprocess.run(["git", "merge-base", "--is-ancestor", base, candidate], cwd=repo, check=False)
    if ancestor.returncode != 0:
        _fail("packet base is not an ancestor of candidate")
    if _git(repo, "status", "--porcelain", "--untracked-files=all"):
        _fail("candidate repository is dirty")
    actual = changed_paths(repo, base, candidate)
    allowed = packet["ownership"]["write_paths"]
    escaped = [path for path in actual if not any(_covers(spec, path) for spec in allowed)]
    if escaped:
        _fail("changed paths escape ownership: " + ", ".join(escaped))
    declared = sorted(result["changed_paths"])
    if declared != actual:
        _fail(f"result.changed_paths does not match Git (declared={declared}, actual={actual})")
    return actual


def _validate_evidence(value: Any, where: str) -> None:
    if not isinstance(value, list):
        _fail(f"{where}: expected array")
    for index, item in enumerate(value):
        item_where = f"{where}[{index}]"
        if not isinstance(item, dict):
            _fail(f"{item_where}: expected object")
        _expect_keys(item, {"summary", "location"}, {"summary", "location"}, item_where)
        summary = _text(item["summary"], f"{item_where}.summary")
        location = _text(item["location"], f"{item_where}.location")
        if len(summary) > 1000 or len(location) > 500:
            _fail(f"{item_where}: evidence must be a bounded summary and location")


def validate_result(
    result: dict[str, Any],
    packet: dict[str, Any],
    repo: Path | None,
) -> dict[str, Any]:
    validate_packet(packet)
    _versioned(result, "result")
    required = {
        "record_type", "schema_version", "task_id", "mode", "packet_digest", "base_sha",
        "candidate_sha", "outcome", "changed_paths", "checks", "evidence", "completion",
    }
    _expect_keys(result, required, required, "result")
    if result["task_id"] != packet["task_id"] or result["mode"] != packet["mode"]:
        _fail("result task_id/mode does not match packet")
    if result["packet_digest"] != digest_record(packet):
        _fail("result.packet_digest does not match packet")
    if _sha(result["base_sha"], "result.base_sha") != packet["repository"]["base_sha"]:
        _fail("result.base_sha does not match packet")
    _sha(result["candidate_sha"], "result.candidate_sha")
    if result["outcome"] not in OUTCOMES:
        _fail(f"result.outcome: unsupported outcome {result['outcome']!r}")
    _path_specs(result["changed_paths"], "result.changed_paths")
    actual = (
        _validate_repository_state(packet, result, repo)
        if repo is not None
        else result["changed_paths"]
    )
    if result["outcome"] == "no_change" and (actual or result["candidate_sha"] != result["base_sha"]):
        _fail("no_change requires candidate_sha == base_sha and no Git changes")
    if result["outcome"] == "changed" and not actual:
        _fail("changed outcome requires at least one Git-changed path")

    checks = result["checks"]
    if not isinstance(checks, list):
        _fail("result.checks: expected array")
    by_id: dict[str, str] = {}
    for index, check in enumerate(checks):
        where = f"result.checks[{index}]"
        if not isinstance(check, dict):
            _fail(f"{where}: expected object")
        check_keys = {"id", "status", "summary", "location"}
        _expect_keys(check, check_keys, check_keys, where)
        check_id = _text(check["id"], f"{where}.id")
        if check_id in by_id:
            _fail(f"{where}: duplicate check id {check_id}")
        if check["status"] not in CHECK_STATUSES:
            _fail(f"{where}.status: expected passed, failed, or not_run")
        _text(check["summary"], f"{where}.summary")
        _text(check["location"], f"{where}.location")
        if len(check["summary"]) > 1000 or len(check["location"]) > 500:
            _fail(f"{where}: check evidence must be a bounded summary and location")
        by_id[check_id] = check["status"]
    missing = [check_id for check_id in packet["validation"] if check_id not in by_id]
    if missing:
        _fail("result is missing required checks: " + ", ".join(missing))
    _validate_evidence(result["evidence"], "result.evidence")
    completion = result["completion"]
    if not isinstance(completion, dict) or set(completion) != set(COMPLETION_STAGES):
        _fail("result.completion must contain exactly source, artifact, published, and deployed")
    for stage, status in completion.items():
        if status not in COMPLETION_STATUSES:
            _fail(f"result.completion.{stage}: unsupported status {status!r}")
    return result


def require_result_ready(result: dict[str, Any], packet: dict[str, Any]) -> None:
    by_id = {check["id"]: check["status"] for check in result["checks"]}
    not_passed = [check_id for check_id in packet["validation"] if by_id[check_id] != "passed"]
    if not_passed:
        _fail("required checks did not pass: " + ", ".join(not_passed))
    if result["outcome"] not in {"changed", "no_change"}:
        _fail(f"result outcome is not ready for advancement: {result['outcome']}")


def validate_review(review: dict[str, Any], packet: dict[str, Any], candidate_sha: str) -> dict[str, Any]:
    validate_packet(packet)
    _versioned(review, "review")
    required = {
        "record_type", "schema_version", "task_id", "packet_digest",
        "candidate_sha", "lens", "status", "findings",
    }
    _expect_keys(review, required, required, "review")
    if review["task_id"] != packet["task_id"] or review["packet_digest"] != digest_record(packet):
        _fail("review does not identify the packet")
    if _sha(review["candidate_sha"], "review.candidate_sha") != candidate_sha:
        _fail("review is stale: candidate_sha does not match exact candidate")
    lens = _text(review["lens"], "review.lens")
    if lens not in packet["required_review_lenses"]:
        _fail(f"review.lens {lens!r} was not required by packet")
    if review["status"] not in REVIEW_STATUSES:
        _fail("review.status: expected passed or failed")
    _validate_evidence(review["findings"], "review.findings")
    return review


def validate_reviews(reviews: list[dict[str, Any]], packet: dict[str, Any], candidate_sha: str) -> None:
    by_lens: dict[str, dict[str, Any]] = {}
    for review in reviews:
        validate_review(review, packet, candidate_sha)
        if review["lens"] in by_lens:
            _fail(f"duplicate review lens: {review['lens']}")
        by_lens[review["lens"]] = review
    missing = [lens for lens in packet["required_review_lenses"] if lens not in by_lens]
    if missing:
        _fail("missing required review lenses: " + ", ".join(missing))
    failed = [lens for lens in packet["required_review_lenses"] if by_lens[lens]["status"] != "passed"]
    if failed:
        _fail("required review lenses failed: " + ", ".join(failed))


def digest_record(record: dict[str, Any]) -> str:
    encoded = json.dumps(record, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")
    return "sha256:" + hashlib.sha256(encoded).hexdigest()


def validate_config(repo: Path) -> None:
    config_path = repo / ".codex" / "config.toml"
    try:
        config = tomllib.loads(config_path.read_text(encoding="utf-8"))
    except (OSError, tomllib.TOMLDecodeError) as exc:
        _fail(f"invalid Codex config: {exc}")
    if config.get("sandbox_mode") != "read-only":
        _fail("primary Codex sandbox_mode must be read-only")
    agents = config.get("agents")
    if not isinstance(agents, dict) or agents.get("max_concurrent_threads_per_session") != 3:
        _fail("Codex config must cap child threads at three")
    if (
        agents.get("default_subagent_model") != "gpt-5.6-terra"
        or agents.get("default_subagent_reasoning_effort") != "medium"
    ):
        _fail("Codex default subagent route must be Terra/medium")
    configured_roles = {key for key, value in agents.items() if isinstance(value, dict)}
    if configured_roles != set(ROLE_ROUTES):
        _fail("Codex configured roles do not match protocol roles")
    for role, (model, effort, sandbox) in ROLE_ROUTES.items():
        relative = agents[role].get("config_file")
        if not isinstance(relative, str):
            _fail(f"agent {role} has no config_file")
        path = repo / ".codex" / relative
        try:
            role_config = tomllib.loads(path.read_text(encoding="utf-8"))
        except (OSError, tomllib.TOMLDecodeError) as exc:
            _fail(f"invalid agent config {path}: {exc}")
        expected = (role, model, effort, sandbox)
        actual = (
            role_config.get("name"), role_config.get("model"),
            role_config.get("model_reasoning_effort"), role_config.get("sandbox_mode"),
        )
        if actual != expected:
            _fail(f"agent {role} route mismatch: expected {expected}, got {actual}")


def advance_checkpoint(
    state_path: Path,
    stage: str,
    packet_path: Path,
    result_path: Path,
    review_paths: list[Path],
    repo: Path,
) -> dict[str, Any]:
    if stage not in CHECKPOINT_STAGES[1:]:
        _fail(f"unsupported checkpoint target stage: {stage}")
    resolved_repo = repo.resolve()
    resolved_state = state_path.resolve()
    if resolved_state == resolved_repo or resolved_repo in resolved_state.parents:
        _fail("checkpoint state must live outside the product repository")
    record_paths = [("packet", packet_path), ("result", result_path)]
    record_paths.extend(("review", path) for path in review_paths)
    for label, path in record_paths:
        resolved_record = path.resolve()
        if resolved_record == resolved_repo or resolved_repo in resolved_record.parents:
            _fail(f"live {label} record must live outside the product repository")
    packet = load_json(packet_path)
    result = load_json(result_path)
    validate_result(result, packet, resolved_repo)
    require_result_ready(result, packet)
    reviews = [load_json(path) for path in review_paths]
    target_index = CHECKPOINT_STAGES.index(stage)
    if target_index >= CHECKPOINT_STAGES.index("reviewed"):
        validate_reviews(reviews, packet, result["candidate_sha"])
    if stage == "accepted":
        incomplete = [
            item for item in packet["required_completion_stages"]
            if result["completion"][item] != "complete"
        ]
        if incomplete:
            _fail("required completion stages are incomplete: " + ", ".join(incomplete))
    if state_path.exists():
        previous = validate_checkpoint(state_path, resolved_repo)
        if previous.get("task_id") != packet["task_id"]:
            _fail("checkpoint belongs to another task")
        if previous.get("packet_digest") != digest_record(packet):
            _fail("checkpoint pins a different packet; start a new checkpoint or record an explicit repair")
        if previous.get("candidate_sha") != result["candidate_sha"]:
            _fail(
                "checkpoint pins a different candidate; start a new checkpoint "
                "or record an explicit repair"
            )
        previous_stage = previous.get("stage")
        if previous_stage not in CHECKPOINT_STAGES:
            _fail("checkpoint has unknown stage")
        if CHECKPOINT_STAGES.index(previous_stage) > target_index:
            _fail("checkpoint cannot move backwards")
    checkpoint = {
        "record_type": "checkpoint",
        "schema_version": VERSION,
        "task_id": packet["task_id"],
        "stage": stage,
        "packet_digest": digest_record(packet),
        "candidate_sha": result["candidate_sha"],
        "records": {
            "packet": {"path": str(packet_path.resolve()), "digest": digest_record(packet)},
            "result": {"path": str(result_path.resolve()), "digest": digest_record(result)},
            "reviews": [
                {"path": str(path.resolve()), "digest": digest_record(review)}
                for path, review in zip(review_paths, reviews, strict=True)
            ],
        },
    }
    state_path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary_name = tempfile.mkstemp(prefix=state_path.name + ".", dir=state_path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(checkpoint, handle, indent=2, sort_keys=True)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary_name, state_path)
    finally:
        if os.path.exists(temporary_name):
            os.unlink(temporary_name)
    return checkpoint


def validate_checkpoint(
    state_path: Path,
    repo: Path | None,
) -> dict[str, Any]:
    if repo is not None:
        repo = repo.resolve()
        resolved_state = state_path.resolve()
        if resolved_state == repo or repo in resolved_state.parents:
            _fail("checkpoint state must live outside the verification repository")
    checkpoint = load_json(state_path)
    _versioned(checkpoint, "checkpoint")
    required = {
        "record_type", "schema_version", "task_id", "stage",
        "packet_digest", "candidate_sha", "records",
    }
    _expect_keys(checkpoint, required, required, "checkpoint")
    if checkpoint["stage"] not in CHECKPOINT_STAGES:
        _fail("checkpoint has unknown stage")
    _sha(checkpoint["candidate_sha"], "checkpoint.candidate_sha")
    records = checkpoint["records"]
    if not isinstance(records, dict):
        _fail("checkpoint.records: expected object")
    record_keys = {"packet", "result", "reviews"}
    _expect_keys(records, record_keys, record_keys, "checkpoint.records")
    refs = [records["packet"], records["result"]]
    if not isinstance(records["reviews"], list):
        _fail("checkpoint.records.reviews: expected array")
    refs.extend(records["reviews"])
    loaded: list[dict[str, Any]] = []
    for index, ref in enumerate(refs):
        where = f"checkpoint record reference {index}"
        if not isinstance(ref, dict):
            _fail(f"{where}: expected object")
        _expect_keys(ref, {"path", "digest"}, {"path", "digest"}, where)
        record_path = Path(_text(ref["path"], f"{where}.path"))
        if not record_path.is_absolute():
            _fail(f"{where}: record path must be absolute")
        resolved_record = record_path.resolve()
        if repo is not None and (resolved_record == repo or repo in resolved_record.parents):
            _fail(f"{where}: live record must be outside the verification repository")
        record = load_json(resolved_record)
        if digest_record(record) != ref["digest"]:
            _fail(f"{where}: content digest mismatch")
        loaded.append(record)
    packet, result, *reviews = loaded
    validate_result(result, packet, repo)
    require_result_ready(result, packet)
    if checkpoint["task_id"] != packet["task_id"]:
        _fail("checkpoint task_id does not match packet")
    if digest_record(packet) != checkpoint["packet_digest"]:
        _fail("checkpoint packet digest mismatch")
    if result.get("candidate_sha") != checkpoint["candidate_sha"]:
        _fail("checkpoint candidate does not match result")
    target_index = CHECKPOINT_STAGES.index(checkpoint["stage"])
    if target_index >= CHECKPOINT_STAGES.index("reviewed"):
        validate_reviews(reviews, packet, checkpoint["candidate_sha"])
    if checkpoint["stage"] == "accepted":
        incomplete = [
            item for item in packet["required_completion_stages"]
            if result["completion"][item] != "complete"
        ]
        if incomplete:
            _fail("required completion stages are incomplete: " + ", ".join(incomplete))
    return checkpoint
