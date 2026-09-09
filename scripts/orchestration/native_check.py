#!/usr/bin/env python3
"""Run stateless Git checks for the experimental native orchestration mode."""

from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
from pathlib import Path, PurePosixPath


SHA_RE = re.compile(r"^[0-9a-f]{40}$")
MAX_GIT_OUTPUT = 1024 * 1024
GIT_TIMEOUT_SECONDS = 10


class CheckError(ValueError):
    """A native orchestration check failed."""


def _run_git(repo: Path, *args: str, text: bool = True) -> str | bytes:
    environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    environment.update(
        {
            "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_NO_LAZY_FETCH": "1",
            "GIT_OPTIONAL_LOCKS": "0",
            "GIT_TERMINAL_PROMPT": "0",
        }
    )
    try:
        process = subprocess.run(
            [
                "git", "--no-pager", "--no-optional-locks",
                "-c", "core.fsmonitor=false", "-C", str(repo), *args,
            ],
            check=False,
            env=environment,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=text,
            timeout=GIT_TIMEOUT_SECONDS,
        )
    except subprocess.TimeoutExpired as exc:
        raise CheckError(f"git {' '.join(args)} timed out") from exc
    except OSError as exc:
        raise CheckError(f"cannot run git {' '.join(args)}: {exc}") from exc
    if process.returncode:
        stderr = process.stderr if text else process.stderr.decode(errors="replace")
        detail = stderr.strip()
        raise CheckError(f"git {' '.join(args)} failed" + (f": {detail}" if detail else ""))
    output = process.stdout
    if len(output) > MAX_GIT_OUTPUT:
        raise CheckError(f"git {' '.join(args)} output exceeds {MAX_GIT_OUTPUT} bytes")
    return output


def _require_sha(value: str, label: str) -> str:
    if not SHA_RE.fullmatch(value):
        raise CheckError(f"{label} must be a full lowercase 40-character Git SHA")
    return value


def _require_repo_root(value: str) -> Path:
    path = Path(value)
    if not path.is_absolute():
        raise CheckError("--repo-root must be absolute")
    canonical = Path(os.path.realpath(path))
    if str(path) != str(canonical):
        raise CheckError("--repo-root must use its canonical absolute spelling")
    if not canonical.is_dir():
        raise CheckError("--repo-root must be an existing directory")
    top = str(_run_git(canonical, "rev-parse", "--show-toplevel")).strip()
    if top != str(canonical):
        raise CheckError("--repo-root must equal the Git toplevel")
    return canonical


def _require_branch(repo: Path, expected: str) -> None:
    if not expected or expected.startswith("-"):
        raise CheckError("--branch must be an explicit named branch")
    _run_git(repo, "check-ref-format", "--branch", expected)
    actual = str(_run_git(repo, "symbolic-ref", "--quiet", "--short", "HEAD")).strip()
    if actual != expected:
        raise CheckError(f"branch mismatch: expected {expected}, found {actual}")


def _require_commit(repo: Path, sha: str, label: str) -> None:
    try:
        _run_git(repo, "cat-file", "-e", f"{sha}^{{commit}}")
    except CheckError as exc:
        raise CheckError(f"{label} is not a commit in this repository: {sha}") from exc


def _head(repo: Path) -> str:
    return str(_run_git(repo, "rev-parse", "HEAD")).strip()


def _require_clean(repo: Path) -> None:
    status = bytes(
        _run_git(repo, "status", "--porcelain=v1", "--untracked-files=all", text=False)
    )
    if status:
        raise CheckError("worktree has tracked or untracked changes")


def _git_alias(part: str) -> bool:
    normalized = part.rstrip(" .").casefold()
    return normalized == ".git" or normalized.startswith(".git~")


def _safe_path(value: str, label: str, *, allow_prefix: bool) -> str:
    if not value or "\\" in value or "\x00" in value or "\n" in value or "\r" in value:
        raise CheckError(f"{label} is not a safe repository-relative POSIX path: {value!r}")
    if value.startswith(("/", "~")):
        raise CheckError(f"{label} is not a safe repository-relative POSIX path: {value!r}")
    is_prefix = value.endswith("/")
    if is_prefix and not allow_prefix:
        raise CheckError(f"{label} must name a file")
    raw = value[:-1] if is_prefix else value
    path = PurePosixPath(raw)
    if not path.parts or any(part in {"", ".", ".."} for part in path.parts):
        raise CheckError(f"{label} is not a safe repository-relative POSIX path: {value!r}")
    if any(_git_alias(part) for part in path.parts):
        raise CheckError(f"{label} cannot address Git metadata: {value!r}")
    normalized = path.as_posix() + ("/" if is_prefix else "")
    if normalized != value:
        raise CheckError(f"{label} must use canonical repository-relative POSIX spelling")
    return normalized


def _changed_paths(repo: Path, base: str, candidate: str) -> list[str]:
    output = bytes(
        _run_git(
            repo, "diff", "--no-ext-diff", "--no-textconv", "--name-status", "-z", "-M",
            base, candidate, text=False,
        )
    )
    fields = output.split(b"\0")
    if fields and fields[-1] == b"":
        fields.pop()
    paths: list[str] = []
    index = 0
    while index < len(fields):
        status = fields[index].decode("ascii", errors="strict")
        index += 1
        path_count = 2 if status.startswith(("R", "C")) else 1
        if index + path_count > len(fields):
            raise CheckError("git returned malformed changed-path data")
        for raw_path in fields[index:index + path_count]:
            try:
                path = raw_path.decode("utf-8", errors="strict")
            except UnicodeDecodeError as exc:
                raise CheckError("changed path is not valid UTF-8") from exc
            paths.append(_safe_path(path, "changed path", allow_prefix=False))
        index += path_count
    return sorted(set(paths))


def _covers(spec: str, path: str) -> bool:
    return path.startswith(spec) if spec.endswith("/") else path == spec


def check_start(repo: Path, base: str, branch: str) -> None:
    _require_commit(repo, base, "--base")
    _require_branch(repo, branch)
    actual = _head(repo)
    if actual != base:
        raise CheckError(f"start requires HEAD={base}, found {actual}")
    _require_clean(repo)


def check_candidate(repo: Path, base: str, branch: str, candidate: str, allow: list[str]) -> int:
    _require_commit(repo, base, "--base")
    _require_commit(repo, candidate, "--candidate")
    _require_branch(repo, branch)
    actual = _head(repo)
    if actual != candidate:
        raise CheckError(f"candidate requires HEAD={candidate}, found {actual}")
    try:
        _run_git(repo, "merge-base", "--is-ancestor", base, candidate)
    except CheckError as exc:
        raise CheckError("--base must be an ancestor of --candidate") from exc
    _require_clean(repo)
    specs = [_safe_path(value, "--allow", allow_prefix=True) for value in allow]
    paths = _changed_paths(repo, base, candidate)
    outside = [path for path in paths if not any(_covers(spec, path) for spec in specs)]
    if outside:
        raise CheckError("changed paths outside ownership: " + ", ".join(outside))
    return len(paths)


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Run stateless Git checks for experimental native orchestration.",
    )
    parser.add_argument("--repo-root", required=True, help="canonical absolute Git toplevel")
    parser.add_argument("--base", required=True, help="full lowercase 40-character base SHA")
    parser.add_argument("--branch", required=True, help="expected named writer branch")
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("start", help="require HEAD at base and a clean worktree")
    candidate = commands.add_parser("candidate", help="validate a clean candidate and its ownership")
    candidate.add_argument("--candidate", required=True, help="full lowercase 40-character candidate SHA")
    candidate.add_argument(
        "--allow",
        required=True,
        action="append",
        help="owned repository-relative file or directory prefix ending in /; repeat as needed",
    )
    return parser


def main() -> int:
    args = _parser().parse_args()
    try:
        repo = _require_repo_root(args.repo_root)
        base = _require_sha(args.base, "--base")
        if args.command == "start":
            check_start(repo, base, args.branch)
            print("native start passed")
        else:
            candidate = _require_sha(args.candidate, "--candidate")
            count = check_candidate(repo, base, args.branch, candidate, args.allow)
            print(f"native candidate passed: {count} changed path(s)")
    except CheckError as exc:
        print(f"native check failed: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
