#!/usr/bin/env python3
"""Emit bounded formatting and naming advice for product Go files."""

from __future__ import annotations

import hashlib
import hmac
import json
import os
from pathlib import Path, PurePosixPath
import re
import selectors
import signal
import stat
import subprocess
import sys
import time
from typing import Any, Dict, List, Optional, Sequence, Set, Tuple


MAX_INPUT_BYTES = 2 * 1024 * 1024
MAX_PATCH_LINES = 4096
MAX_PATHS = 8
MAX_FILE_BYTES = 1024 * 1024
MAX_NAMING_SCRIPT_BYTES = 128 * 1024
MAX_CHILD_STDOUT_BYTES = 64 * 1024
MAX_CHILD_STDERR_BYTES = 16 * 1024
MAX_MESSAGES = 10
MAX_CONTEXT_BYTES = 800
READ_CHUNK_BYTES = 8192
COMMAND_TIMEOUT_SECONDS = 1.0
TOTAL_BUDGET_SECONDS = 4.0
PATCH_PREFIXES = ("*** Add File: ", "*** Update File: ")
PRODUCT_DIRS = ("cmd", "internal")
NAMING_SCRIPT_COMPONENTS = (
    ".agents",
    "skills",
    "go-naming",
    "scripts",
    "check-naming.sh",
)
NAMING_SCRIPT_SHA256 = "5eae9d2d5133cae2bb5d9b58058cc79f62d367f77efe3f6c3e837ac82dd9bca7"
SAFE_COMPONENT = re.compile(r"[A-Za-z0-9._-]+\Z", re.ASCII)


class ToolFailure(Exception):
    """A checker or repository lookup failed; the hook must fail open."""


def _kill_process_group(
    process: subprocess.Popen, process_group: int, session: int
) -> None:
    """Kill the recorded child session before reaping its leader.

    The direct child is deliberately never polled or waited before this call,
    so its PID cannot be reused. Because it began a new session with matching
    PID, PGID, and SID, the recorded group can contain only that child and its
    descendants even after the child has exited and become a zombie.
    """
    if process_group != process.pid or session != process.pid:
        return
    try:
        os.killpg(process_group, signal.SIGKILL)
    except OSError:
        pass
    try:
        process.wait(timeout=0.2)
    except (OSError, subprocess.SubprocessError):
        pass


def _run(
    command: Sequence[str],
    *,
    cwd: Path,
    deadline: float,
    pass_fds: Tuple[int, ...] = (),
) -> Tuple[int, str]:
    """Run a child with fixed stdout/stderr, per-command, and total bounds."""
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise ToolFailure
    command_deadline = time.monotonic() + min(COMMAND_TIMEOUT_SECONDS, remaining)
    process: Optional[subprocess.Popen] = None
    process_group: Optional[int] = None
    session: Optional[int] = None
    reaped = False
    selector: Optional[selectors.BaseSelector] = None
    stdout = bytearray()
    stderr_bytes = 0
    try:
        process = subprocess.Popen(
            list(command),
            cwd=cwd,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            close_fds=True,
            pass_fds=pass_fds,
            start_new_session=True,
        )
        # start_new_session=True performs setsid() before exec. Record the
        # resulting SID/PGID without querying the child, which may already be
        # a zombie by the time Popen returns.
        process_group = process.pid
        session = process.pid
        if process.stdout is None or process.stderr is None:
            raise ToolFailure
        selector = selectors.DefaultSelector()
        for stream, name in ((process.stdout, "stdout"), (process.stderr, "stderr")):
            os.set_blocking(stream.fileno(), False)
            selector.register(stream, selectors.EVENT_READ, name)

        while selector.get_map():
            wait = command_deadline - time.monotonic()
            if wait <= 0:
                raise ToolFailure
            events = selector.select(wait)
            if not events:
                raise ToolFailure
            for key, _ in events:
                try:
                    chunk = os.read(key.fileobj.fileno(), READ_CHUNK_BYTES)
                except BlockingIOError:
                    continue
                if not chunk:
                    selector.unregister(key.fileobj)
                    continue
                if key.data == "stdout":
                    if len(stdout) + len(chunk) > MAX_CHILD_STDOUT_BYTES:
                        raise ToolFailure
                    stdout.extend(chunk)
                else:
                    stderr_bytes += len(chunk)
                    if stderr_bytes > MAX_CHILD_STDERR_BYTES:
                        raise ToolFailure

        wait = command_deadline - time.monotonic()
        if wait <= 0:
            raise ToolFailure
        _kill_process_group(process, process_group, session)
        reaped = True
        returncode = process.returncode
        if returncode is None:
            raise ToolFailure
        try:
            decoded = bytes(stdout).decode("utf-8", errors="strict")
        except UnicodeError as exc:
            raise ToolFailure from exc
        return returncode, decoded
    except (OSError, subprocess.SubprocessError) as exc:
        raise ToolFailure from exc
    finally:
        if process is not None:
            if not reaped:
                if process_group is not None and session is not None:
                    _kill_process_group(process, process_group, session)
                else:
                    try:
                        process.kill()
                        process.wait(timeout=0.2)
                    except (OSError, subprocess.SubprocessError):
                        pass
            if process.stdout is not None:
                process.stdout.close()
            if process.stderr is not None:
                process.stderr.close()
        if selector is not None:
            selector.close()


def _git_root(cwd: Path, deadline: float) -> Path:
    returncode, stdout = _run(
        ["git", "rev-parse", "--show-toplevel"], cwd=cwd, deadline=deadline
    )
    if returncode != 0:
        raise ToolFailure
    root_text = stdout.strip()
    if not root_text or "\n" in root_text or "\x00" in root_text:
        raise ToolFailure
    try:
        root = Path(root_text).resolve(strict=True)
    except (OSError, RuntimeError) as exc:
        raise ToolFailure from exc
    if not root.is_dir():
        raise ToolFailure
    return root


def _patch_paths(command: str) -> List[str]:
    paths: List[str] = []
    seen: Set[str] = set()
    for line_number, line in enumerate(command.splitlines()):
        if line_number >= MAX_PATCH_LINES:
            break
        for prefix in PATCH_PREFIXES:
            if not line.startswith(prefix):
                continue
            candidate = line[len(prefix) :]
            if candidate not in seen:
                seen.add(candidate)
                paths.append(candidate)
            break
    return paths


def _safe_components(parts: Sequence[str]) -> bool:
    return all(
        part not in ("", ".", "..") and SAFE_COMPONENT.fullmatch(part) is not None
        for part in parts
    )


def _resolved_product_path(root: Path, raw_path: str) -> Optional[Tuple[str, ...]]:
    if not raw_path or "\x00" in raw_path or "\\" in raw_path:
        return None
    path = PurePosixPath(raw_path)
    raw_parts = tuple(raw_path.split("/"))
    if path.is_absolute() or not _safe_components(raw_parts):
        return None
    if len(raw_parts) < 2 or path.suffix != ".go":
        return None
    try:
        resolved = root.joinpath(*raw_parts).resolve(strict=True)
        resolved_parts = resolved.relative_to(root).parts
    except (OSError, RuntimeError, ValueError):
        return None
    if (
        len(resolved_parts) < 2
        or resolved_parts[0] not in PRODUCT_DIRS
        or Path(resolved_parts[-1]).suffix != ".go"
        or not _safe_components(resolved_parts)
    ):
        return None
    return tuple(resolved_parts)


def _directory_flags() -> int:
    required = ("O_DIRECTORY", "O_NOFOLLOW")
    if any(not hasattr(os, name) for name in required):
        raise ToolFailure
    return (
        os.O_RDONLY
        | os.O_DIRECTORY
        | os.O_NOFOLLOW
        | getattr(os, "O_CLOEXEC", 0)
    )


def _file_flags() -> int:
    if not hasattr(os, "O_NOFOLLOW"):
        raise ToolFailure
    return (
        os.O_RDONLY
        | os.O_NOFOLLOW
        | os.O_NONBLOCK
        | getattr(os, "O_CLOEXEC", 0)
    )


def _safe_close(descriptor: int) -> None:
    try:
        os.close(descriptor)
    except OSError:
        pass


def _open_product_roots(root_descriptor: int) -> Dict[str, int]:
    roots: Dict[str, int] = {}
    flags = _directory_flags()
    for product in PRODUCT_DIRS:
        try:
            roots[product] = os.open(product, flags, dir_fd=root_descriptor)
        except OSError:
            continue
    return roots


def _open_product_file(product_descriptor: int, tail: Sequence[str]) -> Optional[int]:
    if not tail:
        return None
    parent = product_descriptor
    opened_directories: List[int] = []
    try:
        for component in tail[:-1]:
            parent = os.open(component, _directory_flags(), dir_fd=parent)
            opened_directories.append(parent)
        descriptor = os.open(tail[-1], _file_flags(), dir_fd=parent)
        try:
            metadata = os.fstat(descriptor)
            if not stat.S_ISREG(metadata.st_mode) or metadata.st_size > MAX_FILE_BYTES:
                _safe_close(descriptor)
                return None
            return descriptor
        except BaseException:
            _safe_close(descriptor)
            raise
    except OSError as exc:
        raise ToolFailure from exc
    finally:
        for directory in reversed(opened_directories):
            _safe_close(directory)


def _load_verified_naming_script(root_descriptor: int) -> str:
    parent = root_descriptor
    opened_directories: List[int] = []
    descriptor = -1
    try:
        for component in NAMING_SCRIPT_COMPONENTS[:-1]:
            parent = os.open(component, _directory_flags(), dir_fd=parent)
            opened_directories.append(parent)
        descriptor = os.open(
            NAMING_SCRIPT_COMPONENTS[-1], _file_flags(), dir_fd=parent
        )
        metadata = os.fstat(descriptor)
        if (
            not stat.S_ISREG(metadata.st_mode)
            or metadata.st_size > MAX_NAMING_SCRIPT_BYTES
        ):
            raise ToolFailure
        digest = hashlib.sha256()
        contents = bytearray()
        while True:
            chunk = os.read(descriptor, READ_CHUNK_BYTES)
            if not chunk:
                break
            contents.extend(chunk)
            if len(contents) > MAX_NAMING_SCRIPT_BYTES:
                raise ToolFailure
            digest.update(chunk)
        if not hmac.compare_digest(digest.hexdigest(), NAMING_SCRIPT_SHA256):
            raise ToolFailure
        try:
            return bytes(contents).decode("utf-8", errors="strict")
        except UnicodeError as exc:
            raise ToolFailure from exc
    except (OSError, ToolFailure) as exc:
        if isinstance(exc, ToolFailure):
            raise
        raise ToolFailure from exc
    finally:
        if descriptor >= 0:
            _safe_close(descriptor)
        for directory in reversed(opened_directories):
            _safe_close(directory)


def _clean_text(value: str, limit: int = 240) -> str:
    cleaned = " ".join(value.split())
    if len(cleaned) <= limit:
        return cleaned
    return cleaned[: limit - 1] + "…"


def _check_file(
    root: Path,
    descriptor: int,
    naming_script: str,
    relative_path: str,
    deadline: float,
) -> List[str]:
    messages: List[str] = []
    descriptor_path = "/dev/fd/{0}".format(descriptor)
    if not Path("/dev/fd").is_dir():
        raise ToolFailure

    try:
        os.lseek(descriptor, 0, os.SEEK_SET)
    except OSError as exc:
        raise ToolFailure from exc
    returncode, stdout = _run(
        ["gofmt", "-l", "--", descriptor_path],
        cwd=root,
        deadline=deadline,
        pass_fds=(descriptor,),
    )
    if returncode != 0:
        raise ToolFailure
    formatted_paths = [line for line in stdout.splitlines() if line]
    if formatted_paths:
        if formatted_paths != [descriptor_path]:
            raise ToolFailure
        messages.append("{0}: gofmt required".format(relative_path))

    try:
        os.lseek(descriptor, 0, os.SEEK_SET)
    except OSError as exc:
        raise ToolFailure from exc
    returncode, stdout = _run(
        [
            "/bin/bash",
            "-c",
            naming_script,
            "check-naming.sh",
            "--json",
            "--limit",
            "5",
            descriptor_path,
        ],
        cwd=root,
        deadline=deadline,
        pass_fds=(descriptor,),
    )
    if returncode not in (0, 1):
        raise ToolFailure
    try:
        payload: Any = json.loads(stdout)
    except (json.JSONDecodeError, UnicodeError) as exc:
        raise ToolFailure from exc
    if not isinstance(payload, dict) or not isinstance(payload.get("violations"), list):
        raise ToolFailure
    for violation in payload["violations"]:
        if not isinstance(violation, dict):
            raise ToolFailure
        line = violation.get("line")
        rule = violation.get("rule")
        message = violation.get("message")
        if not isinstance(line, int) or line < 1:
            raise ToolFailure
        if not isinstance(rule, str) or not isinstance(message, str):
            raise ToolFailure
        messages.append(
            "{0}:{1} [{2}] {3}".format(
                relative_path, line, _clean_text(rule, 48), _clean_text(message)
            )
        )
    return messages


def _bounded_context(messages: Sequence[str]) -> str:
    bounded: List[str] = []
    used = 0
    for message in messages[:MAX_MESSAGES]:
        separator = 1 if bounded else 0
        remaining = MAX_CONTEXT_BYTES - used - separator
        if remaining <= 0:
            break
        encoded = message.encode("utf-8")
        if len(encoded) > remaining:
            encoded = encoded[:remaining]
            message = encoded.decode("utf-8", errors="ignore")
        if not message:
            break
        bounded.append(message)
        used += separator + len(message.encode("utf-8"))
    return "\n".join(bounded)


def _load_event() -> Optional[Dict[str, Any]]:
    raw = sys.stdin.buffer.read(MAX_INPUT_BYTES + 1)
    if len(raw) > MAX_INPUT_BYTES:
        return None
    try:
        event = json.loads(raw.decode("utf-8"))
    except (UnicodeError, json.JSONDecodeError):
        return None
    return event if isinstance(event, dict) else None


def _advisory() -> Optional[Dict[str, Any]]:
    event = _load_event()
    if event is None:
        return None
    if (
        event.get("hook_event_name") != "PostToolUse"
        or event.get("tool_name") != "apply_patch"
    ):
        return None
    tool_input = event.get("tool_input")
    if not isinstance(tool_input, dict) or not isinstance(tool_input.get("command"), str):
        return None

    raw_paths = _patch_paths(tool_input["command"])
    if not raw_paths:
        return None
    deadline = time.monotonic() + TOTAL_BUDGET_SECONDS
    root = _git_root(Path.cwd(), deadline)
    root_descriptor = -1
    naming_script = ""
    product_roots: Dict[str, int] = {}
    files: List[Tuple[int, str]] = []
    seen_resolved: Set[str] = set()
    try:
        root_descriptor = os.open(root, _directory_flags())
        naming_script = _load_verified_naming_script(root_descriptor)
        product_roots = _open_product_roots(root_descriptor)
        for raw_path in raw_paths:
            resolved_parts = _resolved_product_path(root, raw_path)
            if resolved_parts is None:
                continue
            display_path = "/".join(resolved_parts)
            if display_path in seen_resolved:
                continue
            product_descriptor = product_roots.get(resolved_parts[0])
            if product_descriptor is None:
                continue
            descriptor = _open_product_file(product_descriptor, resolved_parts[1:])
            if descriptor is None:
                continue
            seen_resolved.add(display_path)
            files.append((descriptor, display_path))
            if len(files) >= MAX_PATHS:
                break
        if not files:
            return None

        messages: List[str] = []
        for descriptor, relative_path in files:
            messages.extend(
                _check_file(
                    root, descriptor, naming_script, relative_path, deadline
                )
            )
        context = _bounded_context(messages)
        if not context:
            return None
        return {
            "hookSpecificOutput": {
                "hookEventName": "PostToolUse",
                "additionalContext": context,
            }
        }
    finally:
        for descriptor, _ in files:
            _safe_close(descriptor)
        for descriptor in product_roots.values():
            _safe_close(descriptor)
        if root_descriptor >= 0:
            _safe_close(root_descriptor)


def main() -> int:
    try:
        output = _advisory()
        if output is not None:
            encoded = json.dumps(output, ensure_ascii=False, separators=(",", ":")).encode(
                "utf-8"
            )
            sys.stdout.buffer.write(encoded + b"\n")
    except (Exception, KeyboardInterrupt):
        pass
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
