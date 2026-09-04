#!/usr/bin/env python3
"""Validate NeroCD orchestration config or records without executing them."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

sys.dont_write_bytecode = True
sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from orchestration.protocol import (
    ProtocolError,
    digest_record,
    load_json,
    validate_config,
    validate_checkpoint,
    validate_packet,
    validate_result,
    validate_review,
)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo-root", type=Path, default=Path.cwd())
    parser.add_argument("--json", action="store_true")
    subparsers = parser.add_subparsers(dest="command", required=True)
    subparsers.add_parser("config")
    digest_parser = subparsers.add_parser("digest")
    digest_parser.add_argument("path", type=Path)
    checkpoint_parser = subparsers.add_parser("checkpoint")
    checkpoint_parser.add_argument("path", type=Path)
    checkpoint_parser.add_argument(
        "--offline-recovery",
        action="store_true",
        help="replay record semantics without claiming current Git/workspace verification",
    )
    packet_parser = subparsers.add_parser("packet")
    packet_parser.add_argument("path", type=Path)
    result_parser = subparsers.add_parser("result")
    result_parser.add_argument("path", type=Path)
    result_parser.add_argument("--packet", required=True, type=Path)
    review_parser = subparsers.add_parser("review")
    review_parser.add_argument("path", type=Path)
    review_parser.add_argument("--packet", required=True, type=Path)
    review_parser.add_argument("--candidate", required=True)
    args = parser.parse_args()
    try:
        digest = None
        if args.command == "config":
            validate_config(args.repo_root.resolve())
        elif args.command == "digest":
            digest = digest_record(load_json(args.path))
        elif args.command == "checkpoint":
            validate_checkpoint(
                args.path,
                None if args.offline_recovery else args.repo_root,
            )
        elif args.command == "packet":
            validate_packet(load_json(args.path))
        elif args.command == "result":
            validate_result(load_json(args.path), load_json(args.packet), args.repo_root.resolve())
        else:
            validate_review(load_json(args.path), load_json(args.packet), args.candidate)
    except ProtocolError as exc:
        payload = {"ok": False, "error": str(exc)}
        print(json.dumps(payload) if args.json else f"validation failed: {exc}")
        return 1
    payload = {"ok": True, "kind": args.command}
    if args.command == "checkpoint":
        payload["verification"] = (
            "offline-records" if args.offline_recovery else "live-git-workspace"
        )
    if digest is not None:
        payload["digest"] = digest
    print(
        json.dumps(payload)
        if args.json
        else (
            digest
            if digest is not None
            else (
                "offline checkpoint recovery passed; current Git facts were not verified"
                if args.command == "checkpoint" and args.offline_recovery
                else f"validation passed: {args.command}"
            )
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
